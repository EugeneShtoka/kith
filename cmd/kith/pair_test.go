package main

import (
	"bytes"
	"crypto/sha256"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

func bridgeID(b byte) devicekey.ID { return devicekey.ID(sha256.Sum256([]byte{b})) }

// What `kith pair` prints is what `kith join` reads: the QR code and the line both
// carry the bridge's ticket whole.
func TestATicketShownIsTheInviteJoined(t *testing.T) {
	t.Parallel()
	token := pairing.NewToken()
	ticket := domain.PairingTicket{
		Token: token.String(), Addresses: []string{"192.168.1.5:7443", "[fd00::5]:7443"},
		ID: bridgeID(1).String(), Expires: time.Unix(1_800_000_000, 0),
	}
	var out bytes.Buffer
	showTicket(&out, ticket)
	_, line, ok := strings.Cut(out.String(), "kith join '")
	uri, _, closed := strings.Cut(line, "'")
	if !ok || !closed {
		t.Fatalf("no kith join line in:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "█") {
		t.Errorf("no QR code drawn:\n%s", out.String())
	}
	got, err := pairing.ParseURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	want := pairing.Invite{Addresses: ticket.Addresses, ID: bridgeID(1), Token: token, Expires: ticket.Expires}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("joined %+v, want %+v", got, want)
	}
	// A ticket that does not read is said, never drawn.
	out.Reset()
	showTicket(&out, domain.PairingTicket{Token: "x", ID: ticket.ID, Addresses: ticket.Addresses})
	if strings.Contains(out.String(), "kith join") {
		t.Errorf("a ticket with a bad token was shown: %s", out.String())
	}
}

func TestOnlyYesConfirms(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]bool{
		"y\n": true, "Y\n": true, " yes \n": true, "YES": true,
		"\n": false, "": false, "n\n": false, "no": false, "yy": false, "y es": false,
	} {
		if got := isYes(line); got != want {
			t.Errorf("isYes(%q) = %v, want %v", line, got, want)
		}
	}
}
