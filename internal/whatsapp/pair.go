package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// errNoAccount: pairing was asked for an account the config does not list.
var errNoAccount = errors.New("whatsapp: no such [[whatsapp.account]]")

// errAlreadyLinked: the account is linked already; pairing again would leave the
// phone with two kith devices.
var errAlreadyLinked = errors.New("whatsapp: already linked (unlink kith on the phone, under Linked devices, to link it anew)")

// PairWhatsApp links one configured account as a new linked device (api.WhatsAppLink).
// code hears the code to type on the phone; the account then runs as if it had been
// linked at startup.
func (a *Adapter) PairWhatsApp(ctx context.Context, name string, code func(string) error) (string, error) {
	accounts := a.accountsNow()
	i := slices.IndexFunc(accounts, func(acc Account) bool { return acc.Name == name })
	if i < 0 {
		return "", fmt.Errorf("%w: %q", errNoAccount, name)
	}
	account := accounts[i]
	if !a.beginPairing(account) {
		return "", fmt.Errorf("%w: %s", errPairing, account.Name)
	}
	defer a.endPairing(account)
	if existing, err := a.store.device(ctx, account.Digits); err != nil {
		return "", err
	} else if existing != nil {
		return "", fmt.Errorf("%w: %s", errAlreadyLinked, account.Name)
	}

	device := a.store.container.NewDevice()
	client := whatsmeow.NewClient(device, newLogger(a.log, account.Name))
	qr, err := client.GetQRChannel(ctx)
	if err != nil {
		return "", fmt.Errorf("whatsapp: start pairing: %w", err)
	}
	outcome := make(chan error, 1)
	handler := client.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.PairSuccess:
			report(outcome, nil)
		case *events.PairError:
			report(outcome, e.Error)
		}
	})
	defer client.RemoveEventHandler(handler)
	if err := client.Connect(); err != nil {
		return "", fmt.Errorf("whatsapp: connect to pair: %w", err)
	}
	linked := false
	defer func() {
		if !linked {
			client.Disconnect()
		}
	}()

	if err := askForCode(ctx, client, qr, account.Digits, code); err != nil {
		return "", err
	}
	select {
	case err := <-outcome:
		if err != nil {
			return "", fmt.Errorf("whatsapp: the phone refused: %w", err)
		}
	case <-ctx.Done():
		return "", fmt.Errorf("whatsapp: no answer from the phone: %w", ctx.Err())
	}
	if device.ID == nil || device.ID.User != account.Digits {
		// Typed on another number's phone: that phone now lists kith, not this one.
		_ = client.Logout(ctx)
		return "", fmt.Errorf("whatsapp: the code was entered on another number's phone, not %s's", account.Name)
	}
	linked = true
	a.connect(account, client)
	return domain.NativePerson(domain.ProtocolWhatsApp, device.ID.ToNonAD().String()), nil
}

// askForCode waits for the pairing flow to be ready, asks for a phone-number code
// and hands it to code. The QR channel is drained after: whatsmeow keeps sending on it.
func askForCode(ctx context.Context, client *whatsmeow.Client, qr <-chan whatsmeow.QRChannelItem, digits string, code func(string) error) error {
	defer func() {
		go func() {
			for range qr { //nolint:revive // draining
			}
		}()
	}()
	for {
		select {
		case item, ok := <-qr:
			if !ok {
				return errors.New("whatsapp: pairing ended before a code could be asked for")
			}
			switch item.Event {
			case "code":
				c, err := client.PairPhone(ctx, digits, true, whatsmeow.PairClientChrome, displayName())
				if err != nil {
					return fmt.Errorf("whatsapp: ask for a pairing code: %w", err)
				}
				return code(c)
			case whatsmeow.QRChannelEventError, "timeout", "err-client-outdated", "err-scanned-without-multidevice":
				return fmt.Errorf("whatsapp: pairing failed: %s", item.Event)
			}
		case <-ctx.Done():
			return fmt.Errorf("whatsapp: pairing: %w", ctx.Err())
		}
	}
}

// displayName is how the phone lists kith under Linked devices. WhatsApp wants
// "Browser (OS)".
func displayName() string {
	os := map[string]string{"linux": "Linux", "darwin": "macOS", "windows": "Windows", "freebsd": "FreeBSD"}[runtime.GOOS]
	if os == "" {
		os = runtime.GOOS
	}
	return "Chrome (" + os + ")"
}

// errPairing: the account is being linked by another `kith login whatsapp` already.
var errPairing = errors.New("whatsapp: already being linked (finish or cancel the other `kith login whatsapp`)")

// beginPairing claims an account for one pairing; false when another holds it.
func (a *Adapter) beginPairing(account Account) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pairing[account.Digits] {
		return false
	}
	a.pairing[account.Digits] = true
	return true
}

func (a *Adapter) endPairing(account Account) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pairing, account.Digits)
}

// report sends the first outcome and drops the rest.
func report(outcome chan<- error, err error) {
	select {
	case outcome <- err:
	default:
	}
}
