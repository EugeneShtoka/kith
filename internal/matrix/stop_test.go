package matrix

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
)

// Regression: Stop is called twice (serve's return, run's defer) and possibly before
// Resume; both panicked.
func TestStopIsIdempotentAndSafeBeforeResume(t *testing.T) {
	t.Parallel()

	b := New(nil) // never logged in: client is nil by construction

	b.Stop()
	b.Stop()
}

// A verification request with no verification helper (Init failed after registering handlers)
// must not crash.
func TestStartSASWithoutAVerificationHelperIsANoOp(t *testing.T) {
	t.Parallel()

	b := New(nil) // verify is nil: Init either failed or never ran
	b.ver.base = context.Background()

	// A dispatch with no helper would dereference nil on its own goroutine, which
	// nothing recovers: the test binary would crash here.
	b.startSAS("t1")
	b.ver.work.Wait()
}

// VerificationUnavailable is nil when verification setup was never attempted.
func TestVerificationUnavailableIsNilBeforeSetup(t *testing.T) {
	t.Parallel()

	if err := New(nil).VerificationUnavailable(); err != nil {
		t.Errorf("VerificationUnavailable() = %v, want nil before EnableEncryption runs", err)
	}
}

// enableVerification refuses without a crypto machine.
func TestEnableVerificationWithoutCryptoIsReported(t *testing.T) {
	t.Parallel()

	b := New(nil)
	b.ver.setErr(b.enableVerification(context.Background(), nil))

	if !errors.Is(b.VerificationUnavailable(), api.ErrNoEncryption) {
		t.Errorf("VerificationUnavailable() = %v, want ErrNoEncryption", b.VerificationUnavailable())
	}
	if b.verifier() != nil {
		t.Error("verify should stay nil when enableVerification refused")
	}
}

// Stop unblocks and joins verification work (gossipRestore's blocking send) before
// closing the crypto store.
func TestStopUnblocksAndJoinsVerificationWork(t *testing.T) {
	t.Parallel()

	// Unbuffered: a restore finishing with no UI left to receive it.
	b := &InProc{}
	b.ver.events = make(chan domain.Verification)
	b.ver.base = context.Background()
	b.ver.ctx, b.ver.cancel = context.WithCancel(context.Background())

	// Stands in for gossipRestore.
	parked, finished := make(chan struct{}), make(chan struct{})
	b.goVerify(func() {
		close(parked)
		select {
		case b.ver.events <- domain.Verification{Kind: domain.VerificationRestored}:
		case <-b.verifyContext().Done():
		}
		// Cancellation frees the send; the work still holds the store.
		time.Sleep(150 * time.Millisecond)
		close(finished)
	})
	<-parked

	done := make(chan struct{})
	go func() { b.Stop(); close(done) }()
	select {
	case <-done:
		select {
		case <-finished:
		default:
			t.Error("Stop returned while verification work was still running — the crypto store closes next")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop never returned: the blocking send was never given an exit")
	}
}

// Once Stop has begun, a late callback starts nothing (no Add after Wait).
func TestVerificationWorkIsRefusedAfterStop(t *testing.T) {
	t.Parallel()

	b := &InProc{}
	b.ver.events = make(chan domain.Verification, 1)
	b.ver.base = context.Background()
	b.ver.ctx, b.ver.cancel = context.WithCancel(context.Background())
	b.Stop()

	ran := make(chan struct{})
	b.goVerify(func() { close(ran) })
	select {
	case <-ran:
		t.Error("a callback arriving after Stop started work anyway")
	case <-time.After(100 * time.Millisecond):
	}
}

// A decrypt retry that finishes after Stop has begun is dropped, not handed to the
// sync handlers that would write the closing cache.
func TestALateDecryptionIsDroppedOnceStopping(t *testing.T) {
	t.Parallel()

	srv, _ := sealedServer(t)
	b := backendOn(t, srv)
	syncer := mautrix.NewDefaultSyncer()
	var seen int
	syncer.OnEventType(event.EventMessage, func(context.Context, *event.Event) { seen++ })
	b.client.Syncer = syncer
	evt := &event.Event{Type: event.EventMessage, RoomID: "!a:x", ID: "$late"}

	b.postDecrypt(context.Background(), evt)
	if seen != 1 {
		t.Fatalf("before Stop the event reached %d handlers, want 1", seen)
	}
	b.Stop()
	b.postDecrypt(context.Background(), evt)
	if seen != 1 {
		t.Fatal("a decryption finishing after Stop reached the handlers")
	}
}

// A decryption already past the stopping check when Stop begins is waited for: the
// stores it writes close right after Stop returns.
func TestStopWaitsForADispatchInFlight(t *testing.T) {
	t.Parallel()

	srv, _ := sealedServer(t)
	b := backendOn(t, srv)
	syncer := mautrix.NewDefaultSyncer()
	entered, release := make(chan struct{}), make(chan struct{})
	syncer.OnEventType(event.EventMessage, func(context.Context, *event.Event) {
		close(entered)
		<-release
	})
	b.client.Syncer = syncer

	go b.postDecrypt(context.Background(), &event.Event{Type: event.EventMessage, RoomID: "!a:x", ID: "$late"})
	<-entered
	stopped := make(chan struct{})
	go func() {
		b.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a dispatch was still writing")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never returned after the dispatch finished")
	}
}
