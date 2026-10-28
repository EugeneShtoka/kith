package whatsapp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/EugeneShtoka/kith/internal/apitest"
)

// scanning runs showQRs over a QR channel the test feeds, as whatsmeow would.
func scanning(ctx context.Context, talk *apitest.Talk) (chan<- whatsmeow.QRChannelItem, <-chan error) {
	qr := make(chan whatsmeow.QRChannelItem)
	done := make(chan error, 1)
	go func() { done <- showQRs(ctx, qr, talk) }()
	return qr, done
}

func code(c string, holds time.Duration) whatsmeow.QRChannelItem {
	return whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: c, Timeout: holds}
}

func ended(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("showQRs did not return")
		return nil
	}
}

// Each code whatsmeow hands out is shown in turn, each offering a code typed on the
// phone instead and saying how long it holds; a scan ends the showing.
func TestEachQRCodeIsShownUntilAScan(t *testing.T) {
	t.Parallel()
	talk := &apitest.Talk{}
	qr, done := scanning(context.Background(), talk)
	qr <- code("2@first", 60*time.Second)
	qr <- code("2@second", 20*time.Second)
	qr <- whatsmeow.QRChannelSuccess
	if err := ended(t, done); err != nil {
		t.Fatalf("a scanned code = %v, want nil", err)
	}
	if !slices.Equal(talk.Shown, []string{"2@first", "2@second"}) {
		t.Errorf("shown = %q, want both codes in turn", talk.Shown)
	}
	if !slices.Equal(talk.Offered, []string{insteadOfQR, insteadOfQR}) {
		t.Errorf("offered = %q, want the typed code with each", talk.Offered)
	}
	if note := scanSteps(20 * time.Second); !strings.Contains(note, "20 seconds") {
		t.Errorf("the note %q does not say for how long", note)
	}
}

// How a QR showing ends without a scan: the person takes the typed code, every code
// ran out, the phone or whatsmeow refused, the channel closed, or the login ended.
func TestAQRShowingThatEndsWithoutAScan(t *testing.T) {
	t.Parallel()
	refused := errors.New("refused by the phone")
	for _, tc := range []struct {
		name string
		then func(qr chan<- whatsmeow.QRChannelItem, talk *apitest.Talk, cancel context.CancelFunc)
		want func(error) bool
	}{
		{"the typed code taken", func(_ chan<- whatsmeow.QRChannelItem, talk *apitest.Talk, _ context.CancelFunc) {
			close(talk.Took)
		}, func(err error) bool { return errors.Is(err, errInstead) }},
		{"every code ran out", func(qr chan<- whatsmeow.QRChannelItem, _ *apitest.Talk, _ context.CancelFunc) {
			qr <- whatsmeow.QRChannelTimeout
		}, func(err error) bool { return errors.Is(err, errNoScan) }},
		{"a pairing error", func(qr chan<- whatsmeow.QRChannelItem, _ *apitest.Talk, _ context.CancelFunc) {
			qr <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: refused}
		}, func(err error) bool { return errors.Is(err, refused) }},
		{"an old WhatsApp scanned it", func(qr chan<- whatsmeow.QRChannelItem, _ *apitest.Talk, _ context.CancelFunc) {
			qr <- whatsmeow.QRChannelScannedWithoutMultidevice
		}, func(err error) bool { return err != nil }},
		{"the channel closed", func(qr chan<- whatsmeow.QRChannelItem, _ *apitest.Talk, _ context.CancelFunc) {
			close(qr)
		}, func(err error) bool { return err != nil }},
		{"the login ended", func(_ chan<- whatsmeow.QRChannelItem, _ *apitest.Talk, cancel context.CancelFunc) {
			cancel()
		}, func(err error) bool { return errors.Is(err, context.Canceled) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			talk := &apitest.Talk{Took: make(chan struct{})}
			qr, done := scanning(ctx, talk)
			qr <- code("2@only", 60*time.Second)
			tc.then(qr, talk, cancel)
			if err := ended(t, done); !tc.want(err) {
				t.Errorf("ended with %v", err)
			}
		})
	}
}
