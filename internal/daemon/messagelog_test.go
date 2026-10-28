package daemon_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// numberedCache stands in for kithd's cache for the message stream's own tests: a
// commit numbers several messages at once, a message changed again moves to a new
// number, Clear starts a new epoch, and each tells the streams, as db.Cache does.
type numberedCache struct {
	mu      sync.Mutex
	epoch   string
	last    int64
	changes []domain.MessageChange
	streams *daemon.Streams
	// whileRead runs once, inside the next read, after it has taken what it returns:
	// a commit landing while a reader reads.
	whileRead func()
}

// commit numbers msgs in one commit.
func (c *numberedCache) commit(msgs ...domain.Message) {
	c.mu.Lock()
	for i := range msgs {
		m := &msgs[i]
		c.changes = slices.DeleteFunc(c.changes, func(old domain.MessageChange) bool { return old.Message.ID == m.ID })
		c.last++
		c.changes = append(c.changes, domain.MessageChange{Number: c.last, Message: *m})
	}
	c.mu.Unlock()
	c.streams.MessagesChanged()
}

// clear empties the cache and starts a new epoch; the numbers go on.
func (c *numberedCache) clear(epoch string) {
	c.mu.Lock()
	c.epoch, c.changes = epoch, nil
	c.mu.Unlock()
	c.streams.MessagesChanged()
}

// skip gives n numbers to messages since trimmed.
func (c *numberedCache) skip(n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last += n
}

func (c *numberedCache) at() (string, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch, c.last
}

// held is every message the cache holds numbered after since.
func (c *numberedCache) held(since int64) map[domain.EventID]domain.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[domain.EventID]domain.Message{}
	for i := range c.changes {
		if ch := &c.changes[i]; ch.Number > since {
			out[ch.Message.ID] = ch.Message
		}
	}
	return out
}

func (c *numberedCache) LastChange(context.Context) (string, int64, error) {
	epoch, last := c.at()
	return epoch, last, nil
}

func (c *numberedCache) ChangesAfter(_ context.Context, after int64, limit int) (string, []domain.MessageChange, error) {
	c.mu.Lock()
	var out []domain.MessageChange
	for i := range c.changes {
		if ch := &c.changes[i]; ch.Number > after && len(out) < limit {
			out = append(out, *ch)
		}
	}
	epoch, during := c.epoch, c.whileRead
	c.whileRead = nil
	c.mu.Unlock()
	if during != nil {
		during()
	}
	return epoch, out, nil
}

// serveNumbered serves a daemon over a numbered cache.
func serveNumbered(t *testing.T) (*harness, *numberedCache) {
	t.Helper()
	cache := &numberedCache{epoch: "one"}
	h := serve(t, &fakeBackend{Nop: nopWithChannels()}, func(d *daemon.Daemon) {
		cache.streams = d.Streams
		d.MessageLog = cache
	})
	return h, cache
}

// frame is one message-stream frame as a client reads it.
type frame struct {
	msg       *domain.Message
	epoch     string
	change    int64
	readAgain bool
}

// openMessages opens the message stream from since and passes on its frames until the
// test ends.
func openMessages(t *testing.T, h *harness, epoch string, change int64) <-chan frame {
	t.Helper()
	c := backendv1connect.NewBackendServiceClient(daemon.Dial(h.path), "http://kithd")
	st, err := c.Messages(t.Context(), connect.NewRequest(&v1.MessagesRequest{SinceEpoch: epoch, SinceChange: change}))
	if err != nil {
		t.Fatalf("open the message stream: %v", err)
	}
	st.ResponseHeader() // subscribed
	out := make(chan frame, 1024)
	go func() {
		defer close(out)
		defer func() { _ = st.Close() }()
		for st.Receive() {
			f := frame{epoch: st.Msg().GetEpoch(), change: st.Msg().GetChange(), readAgain: st.Msg().GetReadAgain()}
			if m := st.Msg().GetMessage(); m != nil {
				msg := pc.ProtoToMessage(m)
				f.msg = &msg
			}
			select {
			case out <- f:
			case <-t.Context().Done():
				return
			}
		}
	}()
	return out
}

func nextFrame(t *testing.T, frames <-chan frame) frame {
	t.Helper()
	select {
	case f := <-frames:
		return f
	case <-time.After(settle):
		t.Fatal("no frame")
		return frame{}
	}
}

func message(id string) domain.Message {
	return domain.Message{ID: domain.EventID(mx + "$" + id), RoomID: mx + "!a:x", Body: id}
}

// A reader resuming from any number while commits go on gets every message changed
// after it, each once at its latest number, in order, as the cache holds it: whatever
// the commits, their sizes against a read's batch, and where the reader joins.
func TestAResumingReaderGetsEachChangeAfterItsNumberOnce(t *testing.T) {
	t.Parallel()
	for seed := range uint64(8) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			h, cache := serveNumbered(t)
			r := rand.New(rand.NewPCG(seed, 2))
			commit := func() {
				n := 1 + r.IntN(3)
				if r.IntN(20) == 0 {
					n = 300 // more than a read sends at once
				}
				var msgs []domain.Message
				for range n {
					m := message(fmt.Sprint(r.IntN(40)))
					m.Body = fmt.Sprint(r.Int())
					msgs = append(msgs, m)
				}
				cache.commit(msgs...)
			}
			for range r.IntN(30) {
				commit()
			}
			epoch, last := cache.at()
			since := r.Int64N(last + 1)
			frames := openMessages(t, h, epoch, since)
			h.waitStreams(1)
			for range 1 + r.IntN(30) {
				commit()
				if r.IntN(3) == 0 {
					time.Sleep(time.Millisecond) // let the reader catch up, or not
				}
			}
			_, end := cache.at()
			want := cache.held(since)
			got := map[domain.EventID]domain.Message{}
			for at := since; at < end; {
				f := nextFrame(t, frames)
				if f.readAgain || f.msg == nil || f.epoch != epoch {
					t.Fatalf("frame %+v resuming from %d in %s", f, since, epoch)
				}
				if f.change <= at {
					t.Fatalf("change %d after %d", f.change, at)
				}
				at = f.change
				got[f.msg.ID] = *f.msg // a message changed again comes again, later
			}
			if len(got) != len(want) {
				t.Fatalf("read %d messages, the cache holds %d changed after %d", len(got), len(want), since)
			}
			for id, m := range want {
				if got[id].Body != m.Body {
					t.Fatalf("%s reads %q, the cache holds %q", id, got[id].Body, m.Body)
				}
			}
		})
	}
}

// A reader that cannot resume from its number is told to read again, at the newest
// number, and then reads on: another epoch, a number the cache never gave, or one so
// far behind that reading again is cheaper.
func TestAReaderThatCannotResumeIsToldToReadAgain(t *testing.T) {
	t.Parallel()
	for name, since := range map[string]func(epoch string, last int64) (string, int64){
		"another epoch":   func(_ string, last int64) (string, int64) { return "gone", last },
		"from the future": func(epoch string, last int64) (string, int64) { return epoch, last + 1 },
		"too far behind":  func(epoch string, _ int64) (string, int64) { return epoch, 0 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, cache := serveNumbered(t)
			cache.commit(message("old"))
			cache.skip(6000)
			epoch, last := cache.at()
			fromEpoch, from := since(epoch, last)
			frames := openMessages(t, h, fromEpoch, from)
			if f := nextFrame(t, frames); !f.readAgain || f.msg != nil || f.epoch != epoch || f.change != last {
				t.Fatalf("first frame %+v, want read again at %s/%d", f, epoch, last)
			}
			cache.commit(message("new"))
			if f := nextFrame(t, frames); f.msg == nil || f.msg.Body != "new" {
				t.Fatalf("then %+v, want the new message", f)
			}
		})
	}
}

// A reader opening with no number starts at the newest: what came before is the
// client's to read, as when the stream was live only.
func TestAReaderWithoutANumberStartsAtTheNewest(t *testing.T) {
	t.Parallel()
	h, cache := serveNumbered(t)
	cache.commit(message("before"))
	frames := openMessages(t, h, "", 0)
	h.waitStreams(1)
	cache.commit(message("after"))
	if f := nextFrame(t, frames); f.readAgain || f.msg == nil || f.msg.Body != "after" {
		t.Fatalf("first frame %+v, want the message after it opened", f)
	}
}

// A cache cleared under a reader tells it to read again in the new epoch, at its own
// number, and its messages follow.
func TestAClearedCacheTellsTheReaderToReadAgain(t *testing.T) {
	t.Parallel()
	h, cache := serveNumbered(t)
	frames := openMessages(t, h, "", 0)
	h.waitStreams(1)
	cache.commit(message("kept"))
	_ = nextFrame(t, frames)
	_, at := cache.at()
	cache.clear("two")
	if f := nextFrame(t, frames); !f.readAgain || f.epoch != "two" || f.change != at {
		t.Fatalf("after the clear %+v, want read again in two at %d", f, at)
	}
	cache.commit(message("refetched"))
	if f := nextFrame(t, frames); f.msg == nil || f.msg.Body != "refetched" || f.epoch != "two" {
		t.Fatalf("then %+v, want the refetched message in two", f)
	}
}

// Two readers both read every change; neither waits on the other.
func TestEveryReaderReadsEveryChange(t *testing.T) {
	t.Parallel()
	h, cache := serveNumbered(t)
	first, second := openMessages(t, h, "", 0), openMessages(t, h, "", 0)
	h.waitStreams(2)
	for i := range 50 {
		cache.commit(message(fmt.Sprint(i)))
	}
	for _, frames := range []<-chan frame{first, second} {
		for i := range 50 {
			if f := nextFrame(t, frames); f.msg == nil || f.msg.Body != fmt.Sprint(i) {
				t.Fatalf("frame %d: %+v", i, f)
			}
		}
	}
}

// The message streams end when the backend's messages do, as when they were its
// fan-out: the client reattaches.
func TestTheMessageStreamEndsWithTheBackendsMessages(t *testing.T) {
	t.Parallel()
	msgs := make(chan domain.Message)
	cache := &numberedCache{epoch: "one"}
	h := serve(t, &fakeBackend{Nop: apitest.Nop{Msgs: msgs}}, func(d *daemon.Daemon) {
		cache.streams = d.Streams
		d.MessageLog = cache
	})
	frames := openMessages(t, h, "", 0)
	h.waitStreams(1)
	close(msgs)
	select {
	case f, open := <-frames:
		if open {
			t.Fatalf("frame %+v, want the stream ended", f)
		}
	case <-time.After(settle):
		t.Fatal("the stream did not end")
	}
}

// A commit larger than one read arrives whole, with nothing after it to wake the reader.
func TestACommitLargerThanAReadArrivesWhole(t *testing.T) {
	t.Parallel()
	h, cache := serveNumbered(t)
	frames := openMessages(t, h, "", 0)
	h.waitStreams(1)
	var msgs []domain.Message
	for i := range 600 {
		msgs = append(msgs, message(fmt.Sprint(i)))
	}
	cache.commit(msgs...)
	for i := range 600 {
		if f := nextFrame(t, frames); f.msg == nil || f.msg.Body != fmt.Sprint(i) {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
}

// A commit landing while the reader reads wakes it after: nothing else need follow.
func TestACommitDuringAReadWakesTheReader(t *testing.T) {
	t.Parallel()
	h, cache := serveNumbered(t)
	frames := openMessages(t, h, "", 0)
	h.waitStreams(1)
	cache.mu.Lock()
	cache.whileRead = func() { cache.commit(message("during")) }
	cache.mu.Unlock()
	cache.commit(message("first"))
	for _, want := range []string{"first", "during"} {
		if f := nextFrame(t, frames); f.msg == nil || f.msg.Body != want {
			t.Fatalf("frame %+v, want %s", f, want)
		}
	}
}
