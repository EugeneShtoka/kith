package matrix

import (
	"context"
	"errors"
	"sync"
	"testing"

	"maunium.net/go/mautrix/crypto/cryptohelper"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The crypto machine is published under a lock: on a degraded start EnableEncryption
// runs on a worker goroutine beside the sweeper and RPC handlers. Meaningful under -race.
func TestTheCryptoMachineIsPublishedSafely(t *testing.T) {
	t.Parallel()

	b := &InProc{}
	b.ver.events = make(chan domain.Verification, verificationBuffer)
	helper := &cryptohelper.CryptoHelper{}
	ctx := context.Background()

	var wg sync.WaitGroup
	start := make(chan struct{})

	// The writer: EnableEncryption on the degraded-start goroutine.
	wg.Go(func() {
		<-start
		b.publishCrypto(helper)
		verifyCtx, cancel := context.WithCancel(ctx)
		b.ver.publish(ctx, verifyCtx, cancel, nil)
		b.ver.setErr(errors.New("verification unavailable in this test"))
	})

	// The readers: sweeper, RPC handlers, Stop, decrypt path.
	readers := []func(){
		func() { _ = b.cryptoHelper() },
		func() { _ = b.verifier() },
		func() { _ = b.VerificationUnavailable() },
		func() { _ = b.verifyContext() },
		func() { b.ver.mu.RLock(); _ = b.ver.cancel; b.ver.mu.RUnlock() },
	}
	for _, read := range readers {
		wg.Go(func() {
			<-start
			for range 200 {
				read()
			}
		})
	}

	close(start)
	wg.Wait()

	if got := b.cryptoHelper(); got != helper {
		t.Errorf("cryptoHelper() = %v, want the published helper", got)
	}
	b.ver.stop()
}

// Before anything is published, every reader answers "no encryption" rather than nil-ing.
func TestTheMachineAnswersBeforeItExists(t *testing.T) {
	t.Parallel()

	b := &InProc{}
	if got := b.cryptoHelper(); got != nil {
		t.Errorf("cryptoHelper() = %v on a backend with no encryption, want nil", got)
	}
	if got := b.verifier(); got != nil {
		t.Errorf("verifier() = %v, want nil", got)
	}
	if got := b.verifyContext(); got == nil {
		t.Error("verifyContext() = nil, want a usable context")
	}
	if _, err := b.StartVerification(context.Background()); !errors.Is(err, api.ErrNoEncryption) {
		t.Errorf("StartVerification() error = %v, want %v", err, api.ErrNoEncryption)
	}
}
