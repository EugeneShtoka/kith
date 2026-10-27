package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// errNoAccount: the account is not configured.
var errNoAccount = errors.New("whatsapp: no such [[whatsapp.account]]")

// errAlreadyLinked: pairing again would give the phone two kith devices.
var errAlreadyLinked = errors.New("whatsapp: already linked (unlink kith on the phone, under Linked devices, to link it anew)")

// errInstead: the person took the other way a QR step offered.
var errInstead = errors.New("whatsapp: linking with a code instead")

// errNoScan: every QR code the pairing had ran out unscanned.
var errNoScan = errors.New("whatsapp: no phone scanned the QR code in time; log in again to get new ones")

// link is how a pairing gets the phone to accept it: it returns once the phone has
// what it needs (a QR code scanned, a code to type shown); the outcome follows as a
// pairing event.
type link func(qr <-chan whatsmeow.QRChannelItem, client *whatsmeow.Client, account Account) error

// pair links a configured account as a new device, the phone accepting it by link.
// The account then runs as if linked at startup.
func (a *Adapter) pair(ctx context.Context, name string, by link) (string, error) {
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
	if err = client.Connect(); err != nil {
		return "", fmt.Errorf("whatsapp: connect to pair: %w", err)
	}
	linked := false
	defer func() {
		if !linked {
			client.Disconnect()
		}
	}()

	err = by(qr, client, account)
	drain(qr) // whatsmeow sends on: were the channel full, it would disconnect the pairing
	if err == nil {
		err = accepted(ctx, outcome)
	}
	if err != nil {
		return "", err
	}
	if device.ID == nil || device.ID.User != account.Digits {
		// Paired from another number's phone: unlink it.
		_ = client.Logout(ctx)
		return "", fmt.Errorf("whatsapp: the code was scanned or entered on another number's phone, not %s's", account.Name)
	}
	linked = true
	a.connect(account, client)
	return domain.NativePerson(domain.ProtocolWhatsApp, device.ID.ToNonAD().String()), nil
}

// accepted waits for the phone's answer to the pairing.
func accepted(ctx context.Context, outcome <-chan error) error {
	select {
	case err := <-outcome:
		if err != nil {
			return fmt.Errorf("whatsapp: the phone refused: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("whatsapp: no answer from the phone: %w", ctx.Err())
	}
}

// drain empties the QR channel in the background once pairing no longer reads it.
func drain(qr <-chan whatsmeow.QRChannelItem) {
	go func() {
		for range qr { //nolint:revive // draining
		}
	}()
}

// showQRs shows each QR code the pairing hands out until the next, offering a code
// typed on the phone instead. It returns once the phone scanned one, or with
// errInstead once the person takes the other way.
func showQRs(ctx context.Context, qr <-chan whatsmeow.QRChannelItem, talk api.LoginTalk) error {
	for {
		select {
		case item, ok := <-qr:
			if !ok {
				return errors.New("whatsapp: pairing ended before the phone scanned a code")
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				if err := talk.ShowQR(ctx, item.Code, scanSteps(item.Timeout), insteadOfQR); err != nil {
					return err
				}
			case whatsmeow.QRChannelSuccess.Event:
				return nil
			case whatsmeow.QRChannelTimeout.Event:
				return errNoScan
			case whatsmeow.QRChannelScannedWithoutMultidevice.Event:
				return errors.New("whatsapp: the phone's WhatsApp is too old to link devices; update it and log in again")
			case whatsmeow.QRChannelEventError:
				return fmt.Errorf("whatsapp: pairing failed: %w", item.Error)
			case whatsmeow.QRChannelClientOutdated.Event, whatsmeow.QRChannelErrUnexpectedEvent.Event:
				return fmt.Errorf("whatsapp: pairing failed: %s", item.Event)
			}
		case <-talk.Instead():
			return errInstead
		case <-ctx.Done():
			return fmt.Errorf("whatsapp: pairing: %w", ctx.Err())
		}
	}
}

// askForCode requests a phone-number pairing code once the flow is ready.
func askForCode(ctx context.Context, client *whatsmeow.Client, qr <-chan whatsmeow.QRChannelItem, digits string, code func(string) error) error {
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

// displayName is kith's name under Linked devices; WhatsApp wants "Browser (OS)".
func displayName() string {
	os := map[string]string{"linux": "Linux", "darwin": "macOS", "windows": "Windows", "freebsd": "FreeBSD"}[runtime.GOOS]
	if os == "" {
		os = runtime.GOOS
	}
	return "Chrome (" + os + ")"
}

// errPairing: another `kith login whatsapp` is linking the account.
var errPairing = errors.New("whatsapp: already being linked (finish or cancel the other `kith login whatsapp`)")

// beginPairing claims an account for pairing; false when already claimed.
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

func report(outcome chan<- error, err error) {
	select {
	case outcome <- err:
	default:
	}
}
