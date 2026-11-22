package mailbox_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mailbox"
	"github.com/EugeneShtoka/kith/internal/mesh"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// A pairing takes one answer, before it expires; a bridge answering is no longer let
// in to answer it; the verdict is kept until the pairing is closed.
func TestAPairingTakesOneAnswerBeforeItExpires(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := openStore(t, filepath.Join(t.TempDir(), "mailbox.db"), make([]byte, sealeddb.KeyLen))
	now := time.Now()
	a, b := devicekey.ID{1}, devicekey.ID{2}
	if err := s.OpenPairing(ctx, "tok", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !s.PairingOpen(ctx, now) || s.PairingOpen(ctx, now.Add(time.Hour)) {
		t.Error("a pairing is open until it expires, and only until then")
	}
	if err := s.AnswerPairing(ctx, "tok", a, now.Add(time.Hour)); !errors.Is(err, mailbox.ErrNoPairing) {
		t.Errorf("an answer past the expiry: %v, want ErrNoPairing", err)
	}
	if err := s.AnswerPairing(ctx, "tok", a, now); err != nil {
		t.Fatal(err)
	}
	if err := s.AnswerPairing(ctx, "tok", b, now); !errors.Is(err, mailbox.ErrNoPairing) {
		t.Errorf("a second answer: %v, want ErrNoPairing", err)
	}
	if s.PairingOpen(ctx, now) {
		t.Error("an answered pairing still lets a key in")
	}
	if err := s.DecidePairing(ctx, "tok", mailbox.Accepted); err != nil {
		t.Fatal(err)
	}
	if p, err := s.PairingAt(ctx, "tok"); err != nil || !p.Answered || p.Device != a || p.Verdict != mailbox.Accepted {
		t.Errorf("the pairing is %+v (%v); want answered by a, accepted", p, err)
	}
	if err := s.ClosePairing(ctx, "tok"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PairingAt(ctx, "tok"); !errors.Is(err, mailbox.ErrNoPairing) {
		t.Errorf("a closed pairing: %v, want ErrNoPairing", err)
	}
}

// Two bridges of one mesh that never sync with each other hand a read across a mailbox
// that restarted between them: the second, admitted by the first's word, is still
// admitted after the restart, and the read is still there to take. Compacting keeps it.
func TestABridgeTakesAReadFromAMailboxThatRestarted(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path, key := filepath.Join(t.TempDir(), "mailbox.db"), make([]byte, sealeddb.KeyLen)
	self := devicekey.ID{9}
	a, b := newBridge(t, "a"), newBridge(t, "b")
	a.join(t, b)
	for _, br := range []*bridge{a, b} {
		if err := br.store.AddMailbox(ctx, br.key, domain.Device{ID: self.String(), Name: "mailbox"}, []string{"mailbox.example:7444"}); err != nil {
			t.Fatal(err)
		}
	}
	read := domain.ReadPosition{Room: "telegram:200/300", Event: "e1", At: time.Now().UnixMilli()}
	if err := a.store.Read(ctx, a.key, []domain.ReadPosition{read}); err != nil {
		t.Fatal(err)
	}

	s := openStore(t, path, key)
	if err := s.Admit(ctx, a.id); err != nil {
		t.Fatal(err)
	}
	stop := session(t, s, self, a)
	waitFor(t, "b admitted by a's word", func() bool { return s.Admits(b.id) })
	stop()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openStore(t, path, key)
	if !s.Admits(a.id) || !s.Admits(b.id) {
		t.Fatal("the restarted mailbox forgot whom it admits")
	}
	if err := s.Compact(ctx, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.Size() == 0 {
		t.Error("the restarted mailbox holds nothing")
	}
	stop = session(t, s, self, b)
	defer stop()
	waitFor(t, "a's read at b", func() bool { return slices.Contains(b.store.Reads(), read) })
}

func openStore(t *testing.T, path string, key []byte) *mailbox.Store {
	t.Helper()
	s, err := mailbox.Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// session runs br's sessions with the mailbox s (self) over memory, again as each
// ends, until the returned stop.
func session(t *testing.T, s *mailbox.Store, self devicekey.ID, br *bridge) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			one, end := context.WithCancel(ctx)
			up, down := make(chan *meshv1.SyncFrame, 64), make(chan *meshv1.SyncFrame, 64)
			served := make(chan struct{})
			go func() {
				defer close(served)
				_ = s.Serve(one, self, br.id, chans{in: up, out: down, ctx: one}, time.Now)
				end()
			}()
			_ = br.store.SyncMailbox(one, br.key, br.id, self, chans{in: down, out: up, ctx: one}, slog.New(slog.DiscardHandler))
			end()
			<-served
		}
	}()
	return func() { cancel(); <-done }
}

// bridge is a kithd's mesh store and key.
type bridge struct {
	key   *ecdsa.PrivateKey
	id    devicekey.ID
	store *mesh.Store
}

func newBridge(t *testing.T, name string) *bridge {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	dbKey := make([]byte, sealeddb.KeyLen)
	_, _ = rand.Read(dbKey)
	store, err := mesh.Open(t.Context(), filepath.Join(t.TempDir(), name+".db"), dbKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ChangeRoster(t.Context(), key, []domain.Device{{ID: id.String(), Name: name, Role: domain.RoleBridge}}, nil); err != nil {
		t.Fatal(err)
	}
	return &bridge{key: key, id: id, store: store}
}

// join has other join the mesh of br, as kith join --bridge does once paired.
func (br *bridge) join(t *testing.T, other *bridge) {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(other.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	if err := br.store.Bridge(t.Context(), br.key, domain.Device{ID: other.id.String(), Name: "b"}, der, other.store.Sealing(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ab, ba := make(chan *meshv1.SyncFrame, 64), make(chan *meshv1.SyncFrame, 64)
	go func() {
		_ = br.store.Sync(ctx, br.key, other.id, false, chans{in: ba, out: ab, ctx: ctx}, slog.New(slog.DiscardHandler))
	}()
	if _, err := other.store.Join(ctx, other.id, br.id, other.key, chans{in: ab, out: ba, ctx: ctx}); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

// chans is one side of an in-memory sync stream.
type chans struct {
	in, out chan *meshv1.SyncFrame
	ctx     context.Context
}

func (c chans) Send(f *meshv1.SyncFrame) error {
	select {
	case c.out <- f:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

func (c chans) Receive() (*meshv1.SyncFrame, error) {
	select {
	case f := <-c.in:
		return f, nil
	case <-c.ctx.Done():
		return nil, io.EOF
	}
}
