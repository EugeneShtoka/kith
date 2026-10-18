package instagram

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A send tried again with the same TxnID goes with the same offline threading ID
// (Instagram's own retry key), and a new message with a new one.
func TestARetryIsTheSameMessage(t *testing.T) {
	t.Parallel()
	a := New(nil, newSecrets(), nil, nil)
	first := a.otid("t1")
	if again := a.otid("t1"); again != first {
		t.Errorf("a retry went as %s, then %s", first, again)
	}
	a.forgetTxn("t1")
	if next := a.otid("t1"); next == first {
		t.Error("a new message went with an old one's ID")
	}
	if one, two := a.otid(""), a.otid(""); one == two {
		t.Error("two sends without a TxnID shared an ID")
	}
}

// With the account not connected, nothing is sent or cached, and the scheduler hears
// the network is off (it tries again later).
func TestNothingIsSentWhileNotConnected(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.thread(dm())
	room := roomID(self, danaChat)
	ctx := context.Background()
	for name, err := range map[string]error{
		"send":    r.a.Send(ctx, room, domain.Draft{Body: "lost"}),
		"react":   r.a.SendReaction(ctx, room, eventID(self, danaChat, "m1"), "👍"),
		"unsend":  r.a.Redact(ctx, room, eventID(self, danaChat, "m1"), ""),
		"typing":  r.a.SendTyping(ctx, room, true, 0),
		"no chat": r.a.Send(ctx, "instagram:nope", domain.Draft{Body: "x"}),
	} {
		if err == nil || (name != "no chat" && !errors.Is(err, api.ErrNetworkOff)) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if msgs := r.messages(danaChat); len(msgs) != 0 {
		t.Errorf("cached %+v", msgs)
	}
}

// instagram answers a connected account's GraphQL calls by their name, keeping each
// call's variables; a name in refuse is answered with an error.
type instagram struct {
	mu     sync.Mutex
	calls  []igCall
	data   map[string]string
	refuse map[string]bool
}

type igCall struct {
	name string
	vars map[string]any
}

func (f *instagram) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == uploadPath {
		return f.upload(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	name := req.Header.Get("x-fb-friendly-name")
	var vars map[string]any
	if err := json.Unmarshal([]byte(form.Get("variables")), &vars); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, igCall{name, vars})
	answer := `{"data":` + cmp.Or(f.data[name], "{}") + `}`
	if f.refuse[name] {
		answer = `{"errors":[{"message":"refused"}]}`
	}
	f.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(answer)), Request: req,
	}, nil
}

// called is the variables of each call named name, in order.
func (f *instagram) called(name string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.name == name {
			out = append(out, c.vars)
		}
	}
	return out
}

// field is a variable by its dotted path, as a string.
func field(vars map[string]any, path string) string {
	var v any = vars
	for k := range strings.SplitSeq(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[k]
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// connect gives the rig's connection a client whose calls Instagram (the fake) answers.
func (r *rig) connect() *instagram {
	f := &instagram{data: map[string]string{}, refuse: map[string]bool{}}
	client := instameow.NewClient(instameow.ClientParams{Cookies: r.c.session.Cookies, Log: zerolog.Nop()})
	client.GetHTTP().SetTransportOverride(f) // after its own: instameow sets an impersonating transport
	r.c.mu.Lock()
	r.c.client = client
	r.c.mu.Unlock()
	return f
}

func drain[T any](ch <-chan T) {
	for len(ch) > 0 {
		<-ch
	}
}

const (
	sendText = "IGDirectTextSendMutation"
	editText = "IGDirectEditMessageMutation"
	unsend   = "IGDMessageUnsendDialogOffMsysMutation"
	react    = "IGDirectReactionSendMutation"
	markRead = "useIGDMarkThreadAsReadMutation"
	validate = "useIGDMarkThreadAsReadValidationMutation"
)

// sent is Instagram's answer to a send: the message as it named it.
func sent(id string, at time.Duration) string {
	return fmt.Sprintf(`{"xig_direct_text_send_with_slide_messaging_response":{"id":%q,"timestamp_ms":"%d"}}`, id, t0.Add(at).UnixMilli())
}

// A send names the thread, the text and what it replies to; what Instagram accepted is
// cached under the ID it gave, as the account's, reading the thread. A refused send
// caches nothing and its retry goes as the same message.
func TestASendIsCachedAsInstagramNamedIt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	f := r.connect()
	r.thread(dm())
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", danaFBID, time.Minute, "lunch?")})
	drain(r.a.Messages())
	room, ctx := roomID(self, danaChat), context.Background()

	f.data[sendText] = sent("m2", 2*time.Minute)
	if err := r.a.Send(ctx, room, domain.Draft{Body: "yes", ReplyTo: eventID(self, danaChat, "m1"), TxnID: "t1"}); err != nil {
		t.Fatal(err)
	}
	calls := f.called(sendText)
	if len(calls) != 1 || field(calls[0], "ig_thread_igid") != danaChat || field(calls[0], "text.sensitive_string_value") != "yes" ||
		field(calls[0], "reply_to_message_id") != "m1" || field(calls[0], "offline_threading_id") == "" {
		t.Fatalf("sent %v", calls)
	}
	got, ok, err := r.cache.MessageByID(ctx, room, eventID(self, danaChat, "m2"))
	if err != nil || !ok || got.Body != "yes" || got.Sender != person(self) || got.ReplyTo != eventID(self, danaChat, "m1") ||
		!got.Timestamp.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("cached %+v (%v, %v)", got, ok, err)
	}
	if n, _, counted, err := r.cache.CountUnread(ctx, []string{person(self)}, room); err != nil || !counted || n != 0 {
		t.Errorf("unread after my answer = %d (counted %v, %v)", n, counted, err)
	}
	if m := <-r.a.Messages(); m.ID != got.ID {
		t.Errorf("announced %+v", m)
	}

	f.refuse[sendText] = true
	if err := r.a.Send(ctx, room, domain.Draft{Body: "again", TxnID: "t2"}); err == nil {
		t.Fatal("a refused send succeeded")
	}
	if n := len(r.messages(danaChat)); n != 2 {
		t.Errorf("a refused send was cached: %d messages", n)
	}
	f.refuse[sendText] = false
	f.data[sendText] = sent("m3", 3*time.Minute)
	if err := r.a.Send(ctx, room, domain.Draft{Body: "again", TxnID: "t2"}); err != nil {
		t.Fatal(err)
	}
	calls = f.called(sendText)
	first, refused, retry := field(calls[0], "offline_threading_id"), field(calls[1], "offline_threading_id"), field(calls[2], "offline_threading_id")
	if refused != retry || refused == first {
		t.Errorf("offline threading IDs %s, %s, %s: the retry is not the refused message", first, refused, retry)
	}
}

// An edit names the account's message and its thread; the cache has the new text.
func TestAnEditReplacesTheText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	f := r.connect()
	r.thread(dm())
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", 1000, time.Minute, "lunch at 1")})
	room, ctx := roomID(self, danaChat), context.Background()
	if err := r.a.Send(ctx, room, domain.Draft{Body: "lunch at 2", Edits: eventID(self, danaChat, "m1")}); err != nil {
		t.Fatal(err)
	}
	calls := f.called(editText)
	if len(calls) != 1 || field(calls[0], "target_message_id") != "m1" || field(calls[0], "thread_id") != danaChat || field(calls[0], "body.sensitive_string_value") != "lunch at 2" {
		t.Fatalf("edited %v", calls)
	}
	if got, _, _ := r.cache.MessageByID(ctx, room, eventID(self, danaChat, "m1")); got.Body != "lunch at 2" || !got.Edited {
		t.Errorf("cached %+v", got)
	}
	if err := r.a.Send(ctx, room, domain.Draft{Body: "x", Edits: "signal:nope"}); err == nil {
		t.Error("edited a message that is not Instagram's")
	}
	f.refuse[editText] = true
	if err := r.a.Send(ctx, room, domain.Draft{Body: "lunch at 3", Edits: eventID(self, danaChat, "m1")}); err == nil {
		t.Error("a refused edit succeeded")
	}
	if got, _, _ := r.cache.MessageByID(ctx, room, eventID(self, danaChat, "m1")); got.Body != "lunch at 2" {
		t.Errorf("a refused edit was cached: %+v", got)
	}
}

// The account's reaction goes on a message (replacing its last) and comes off by
// naming its emoji; someone else's cannot be taken off.
func TestReactionsGoOnAndComeOff(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	f := r.connect()
	r.thread(dm())
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", danaFBID, time.Minute, "lunch?")})
	r.delta(danaChat, &slidetypes.CreateReactionEvent{MessageID: "m1", Reaction: slidetypes.Reaction{Reaction: "😂", SenderFBID: danaFBID}})
	room, target, ctx := roomID(self, danaChat), eventID(self, danaChat, "m1"), context.Background()
	for _, key := range []string{"👍", "❤️"} {
		if err := r.a.SendReaction(ctx, room, target, key); err != nil {
			t.Fatal(err)
		}
	}
	calls := f.called(react)
	if len(calls) != 2 || field(calls[1], "input.emoji") != "❤️" || field(calls[1], "input.message_id") != "m1" ||
		field(calls[1], "input.thread_id") != danaChat || field(calls[1], "input.reaction_status") != string(slidetypes.ReactionStatusCreated) {
		t.Fatalf("reacted %v", calls)
	}
	mine := func() []string {
		rs, err := r.cache.Reactions(ctx, room)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, x := range rs {
			if x.Sender == person(self) {
				keys = append(keys, x.Key)
			}
		}
		return keys
	}
	if keys := mine(); !slices.Equal(keys, []string{"❤️"}) {
		t.Errorf("my reactions = %v", keys)
	}

	if err := r.a.Redact(ctx, room, domain.EventID(string(target)+":reaction:"+fbid(danaFBID)), ""); err == nil {
		t.Error("took Dana's reaction off")
	}
	if err := r.a.Redact(ctx, room, domain.EventID(string(target)+":reaction:"+self), ""); err != nil {
		t.Fatal(err)
	}
	calls = f.called(react)
	if len(calls) != 3 || field(calls[2], "input.emoji") != "❤️" || field(calls[2], "input.reaction_status") != string(slidetypes.ReactionStatusDeleted) {
		t.Fatalf("took off %v", calls)
	}
	if keys := mine(); len(keys) != 0 {
		t.Errorf("my reactions after taking it off = %v", keys)
	}
	if err := r.a.Redact(ctx, room, domain.EventID(string(target)+":reaction:"+self), ""); err != nil || len(f.called(react)) != 3 {
		t.Errorf("taking off a reaction not there: %v, %d calls", err, len(f.called(react)))
	}
}

// Unsending names the thread by its long ID; until the inbox has listed it, nothing is
// sent. What Instagram refused stays.
func TestUnsendingNamesTheLongThread(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	f := r.connect()
	r.thread(dm())
	unlisted := group()
	unlisted.ThreadID = ""
	r.thread(unlisted)
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", 1000, time.Minute, "oops")})
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m2", 1000, 2*time.Minute, "keep")})
	room, ctx := roomID(self, danaChat), context.Background()
	if err := r.a.Redact(ctx, room, eventID(self, danaChat, "m1"), ""); err != nil {
		t.Fatal(err)
	}
	calls := f.called(unsend)
	if len(calls) != 1 || field(calls[0], "message_id") != "m1" || !strings.Contains(fmt.Sprint(calls[0]["send_data"]), danaLong) {
		t.Fatalf("unsent %v", calls)
	}
	if got, _, _ := r.cache.MessageByID(ctx, room, eventID(self, danaChat, "m1")); !got.Redacted {
		t.Errorf("cached %+v", got)
	}
	if err := r.a.Redact(ctx, roomID(self, groupChat), eventID(self, groupChat, "m9"), ""); !errors.Is(err, errNoLongID) || len(f.called(unsend)) != 1 {
		t.Errorf("unsend in an unlisted thread: %v", err)
	}
	f.refuse[unsend] = true
	if err := r.a.Redact(ctx, room, eventID(self, danaChat, "m2"), ""); err == nil {
		t.Error("a refused unsend succeeded")
	}
	if got, _, _ := r.cache.MessageByID(ctx, room, eventID(self, danaChat, "m2")); got.Redacted {
		t.Error("a refused unsend was cached")
	}
}

// Reading a thread in kith tells Instagram, naming the long thread, the message and
// when it was sent; a read Instagram refuses stays read in kith.
func TestReadingTellsInstagram(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	f := r.connect()
	r.thread(dm())
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m1", danaFBID, time.Minute, "one")})
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m2", danaFBID, 2*time.Minute, "two")})
	room, ctx := roomID(self, danaChat), context.Background()
	if err := r.a.MarkRead(ctx, room, eventID(self, danaChat, "m1"), false); err != nil {
		t.Fatal(err)
	}
	reads, checks := f.called(markRead), f.called(validate)
	if len(reads) != 1 || field(reads[0], "metadata.ig_thread_igid") != danaLong || field(reads[0], "data.message_id") != "m1" {
		t.Fatalf("read %v", reads)
	}
	if want := strconv.FormatInt(t0.Add(time.Minute).UnixMilli(), 10); len(checks) != 1 || field(checks[0], "data.message_timestamp_ms") != want {
		t.Errorf("validated %v, want the message's time %s", checks, want)
	}
	f.refuse[markRead] = true
	if err := r.a.MarkRead(ctx, room, eventID(self, danaChat, "m2"), false); err != nil {
		t.Fatal(err)
	}
	if n := r.unread(danaChat); n != 0 {
		t.Errorf("unread after a read Instagram refused = %d", n)
	}
	if len(f.called(validate)) != 1 {
		t.Error("a refused read was validated")
	}
}
