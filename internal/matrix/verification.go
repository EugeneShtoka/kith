package matrix

import (
	"context"
	"fmt"
	"sync"

	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// verificationBuffer bounds the verification→UI channel (low-frequency events).
const verificationBuffer = 8

// verifyState is the backend's SAS verification state. Callbacks spawn goroutines
// (the helper holds its lock while calling them) that touch the crypto store, so
// Stop waits for them before closing it.
type verifyState struct {
	// mu guards what enableVerification publishes: a degraded start publishes it
	// while RPCs read it.
	mu     sync.RWMutex
	helper *verificationhelper.VerificationHelper
	// err is why helper is nil (non-fatal).
	err error
	// base is the app-lifetime context callbacks arrive without; ctx and cancel bound
	// the work they start.
	base   context.Context //nolint:containedctx // callbacks arrive with no context of their own
	ctx    context.Context //nolint:containedctx // the lifetime of work started from a callback
	cancel context.CancelFunc

	// workMu orders goVerify's Add against stop's Wait.
	workMu  sync.Mutex
	stopped bool
	work    sync.WaitGroup

	events chan domain.Verification
}

// publish makes a wired helper visible atomically.
func (v *verifyState) publish(base, ctx context.Context, cancel context.CancelFunc, helper *verificationhelper.VerificationHelper) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.base, v.ctx, v.cancel, v.helper = base, ctx, cancel, helper
}

// setErr records why verification is unavailable, or that it is not.
func (v *verifyState) setErr(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.err = err
}

// stop refuses new work, cancels what runs and waits for it. Its two locks are
// never nested.
func (v *verifyState) stop() {
	v.workMu.Lock()
	v.stopped = true
	v.workMu.Unlock()
	v.mu.RLock()
	cancel := v.cancel
	v.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	v.work.Wait()
}

// enableVerification wires the SAS helper into the crypto machine; run before Start
// (it registers sync handlers). Failure is non-fatal (see VerificationUnavailable).
func (b *InProc) enableVerification(ctx context.Context, helper *cryptohelper.CryptoHelper) error {
	if helper == nil {
		return api.ErrNoEncryption
	}
	verifyCtx, cancel := context.WithCancel(ctx)
	cb := verifyCallbacks{
		ids: b.ids,
		out: b.ver.events,
		// The helper calls these holding its lock: re-entry and slow I/O go to a goroutine.
		startSAS: b.startSAS,
		onDone:   func(id.VerificationTransactionID) { b.goVerify(b.gossipRestore) },
	}
	vh := verificationhelper.NewVerificationHelper(
		b.client, helper.Machine(), verificationhelper.NewInMemoryVerificationStore(),
		cb, false /*QR show*/, false /*QR scan*/, true, /*SAS*/
	)
	if err := vh.Init(ctx); err != nil {
		cancel()
		return fmt.Errorf("matrix: init verification: %w", err)
	}
	b.ver.publish(ctx, verifyCtx, cancel, vh)
	return nil
}

// verifier is the verification machine, or nil when it was never wired up.
func (b *InProc) verifier() *verificationhelper.VerificationHelper {
	b.ver.mu.RLock()
	defer b.ver.mu.RUnlock()
	return b.ver.helper
}

// goVerify runs fn on a goroutine Stop waits for, or not at all once Stop has begun.
func (b *InProc) goVerify(fn func()) {
	b.ver.workMu.Lock()
	if b.ver.stopped {
		b.ver.workMu.Unlock()
		return
	}
	b.ver.work.Add(1)
	b.ver.workMu.Unlock()

	go func() {
		defer b.ver.work.Done()
		fn()
	}()
}

// startSAS runs StartSAS on a goroutine, since it re-enters the locked helper. A
// failed Init leaves handlers live with no helper published, hence the nil check.
func (b *InProc) startSAS(txnID id.VerificationTransactionID) {
	vh := b.verifier()
	if vh == nil {
		return
	}
	b.goVerify(func() {
		ctx := b.verifyContext()
		b.warnIf(ctx, vh.StartSAS(ctx, txnID), "start SAS verification", "txn", txnID)
	})
}

// Verifications streams interactive device-verification steps to the UI.
func (b *InProc) Verifications() <-chan domain.Verification { return b.ver.events }

// VerificationUnavailable reports why SAS verification could not be set up, or nil.
func (b *InProc) VerificationUnavailable() error {
	b.ver.mu.RLock()
	defer b.ver.mu.RUnlock()
	return b.ver.err
}

// gossipRestore requests the backup key from the just-verified device and imports
// the backup. Failure only logs (--restore-keys remains).
func (b *InProc) gossipRestore() {
	ctx := b.verifyContext()
	count, err := b.restoreFromGossip(ctx)
	if err != nil || count <= 0 {
		b.warnIf(ctx, err, "restore room keys from backup after verification")
		return
	}
	v := domain.Verification{
		Kind:   domain.VerificationRestored,
		Reason: fmt.Sprintf("restored %d room keys from backup — reopen rooms to see history", count),
	}
	select {
	case b.ver.events <- v:
	case <-ctx.Done():
	}
}

// verifyContext is the lifetime of work started from a verification callback.
func (b *InProc) verifyContext() context.Context {
	b.ver.mu.RLock()
	defer b.ver.mu.RUnlock()
	if b.ver.ctx != nil {
		return b.ver.ctx
	}
	if b.ver.base != nil {
		return b.ver.base
	}
	return context.Background()
}

// StartVerification asks this account's other devices to verify this one and
// returns the transaction ID; VerificationReady then drives SAS.
func (b *InProc) StartVerification(ctx context.Context) (string, error) {
	vh := b.verifier()
	if vh == nil {
		return "", api.ErrNoEncryption
	}
	if b.client == nil || b.client.UserID == "" {
		return "", api.ErrNoEncryption
	}
	txnID, err := vh.StartVerification(ctx, b.client.UserID)
	if err != nil {
		return "", fmt.Errorf("matrix: start verification: %w", err)
	}
	return string(txnID), nil
}

// AcceptVerification accepts an incoming verification request.
func (b *InProc) AcceptVerification(ctx context.Context, txnID string) error {
	return b.withVerifier("accept verification", func(vh *verificationhelper.VerificationHelper) error {
		return vh.AcceptVerification(ctx, id.VerificationTransactionID(txnID))
	})
}

// ConfirmSAS confirms the displayed short authentication string matches.
func (b *InProc) ConfirmSAS(ctx context.Context, txnID string) error {
	return b.withVerifier("confirm SAS", func(vh *verificationhelper.VerificationHelper) error {
		return vh.ConfirmSAS(ctx, id.VerificationTransactionID(txnID))
	})
}

// CancelVerification aborts an in-progress verification (user declined/mismatch).
func (b *InProc) CancelVerification(ctx context.Context, txnID string) error {
	return b.withVerifier("cancel verification", func(vh *verificationhelper.VerificationHelper) error {
		return vh.CancelVerification(ctx, id.VerificationTransactionID(txnID),
			event.VerificationCancelCodeUser, "canceled by user")
	})
}

// withVerifier runs fn on the verification machine, ErrNoEncryption without one.
func (b *InProc) withVerifier(op string, fn func(*verificationhelper.VerificationHelper) error) error {
	vh := b.verifier()
	if vh == nil {
		return api.ErrNoEncryption
	}
	if err := fn(vh); err != nil {
		return fmt.Errorf("matrix: %s: %w", op, err)
	}
	return nil
}

// verifyCallbacks adapts mautrix verification callbacks into domain events. It must
// never call back into the helper synchronously.
type verifyCallbacks struct {
	ids      ids
	out      chan<- domain.Verification
	startSAS func(txnID id.VerificationTransactionID)
	onDone   func(txnID id.VerificationTransactionID)
}

func (c verifyCallbacks) VerificationRequested(ctx context.Context, txnID id.VerificationTransactionID, from id.UserID, fromDevice id.DeviceID) {
	c.emit(ctx, domain.Verification{
		Kind:   domain.VerificationRequested,
		TxnID:  string(txnID),
		From:   c.ids.user(from),
		Device: string(fromDevice),
	})
}

func (c verifyCallbacks) VerificationReady(_ context.Context, txnID id.VerificationTransactionID, _ id.DeviceID, supportsSAS, _ bool, _ *verificationhelper.QRCode) {
	// StartSAS is a no-op if the other device already started it.
	if supportsSAS {
		c.startSAS(txnID)
	}
}

func (c verifyCallbacks) VerificationCancelled(ctx context.Context, txnID id.VerificationTransactionID, _ event.VerificationCancelCode, reason string) {
	c.emit(ctx, domain.Verification{Kind: domain.VerificationCanceled, TxnID: string(txnID), Reason: reason})
}

func (c verifyCallbacks) VerificationDone(ctx context.Context, txnID id.VerificationTransactionID, _ event.VerificationMethod) {
	c.emit(ctx, domain.Verification{Kind: domain.VerificationDone, TxnID: string(txnID)})
	if c.onDone != nil {
		c.onDone(txnID)
	}
}

func (c verifyCallbacks) ShowSAS(ctx context.Context, txnID id.VerificationTransactionID, emojis []rune, emojiDescriptions []string, decimals []int) {
	sas := make([]domain.SASEmoji, len(emojis))
	for i, r := range emojis {
		var name string
		if i < len(emojiDescriptions) {
			name = emojiDescriptions[i]
		}
		sas[i] = domain.SASEmoji{Glyph: string(r), Name: name}
	}
	c.emit(ctx, domain.Verification{Kind: domain.VerificationSAS, TxnID: string(txnID), Emojis: sas, Decimals: decimals})
}

// emit hands a step to the UI, blocking until taken or ctx ends: steps are never dropped.
func (c verifyCallbacks) emit(ctx context.Context, v domain.Verification) {
	select {
	case c.out <- v:
	case <-ctx.Done():
	}
}
