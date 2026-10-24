package domain_test

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every spelling of a link to a person or a group.
func TestParseSignalLinkReadsEverySpelling(t *testing.T) {
	t.Parallel()

	link := bytes.Repeat([]byte{0xfb}, 48)
	invite := []byte{0x0a, 0x02, 0xff, 0xfe}
	eu := base64.RawURLEncoding.EncodeToString(link)
	group := base64.RawURLEncoding.EncodeToString(invite)
	for uri, want := range map[string]domain.SignalLink{
		"https://signal.me/#p/+15550100009":    {Kind: domain.SignalLinkPhone, Phone: "15550100009"},
		"sgnl://signal.me/#p/+15550100009":     {Kind: domain.SignalLinkPhone, Phone: "15550100009"},
		"signal.me/#p/15550100009":             {Kind: domain.SignalLinkPhone, Phone: "15550100009"},
		"https://signal.me/#eu/" + eu:          {Kind: domain.SignalLinkUsername, Username: link},
		"sgnl://signal.me/#eu/" + eu:           {Kind: domain.SignalLinkUsername, Username: link},
		"https://signal.group/#" + group:       {Kind: domain.SignalLinkGroup, Invite: invite},
		"sgnl://signal.group/#" + group + "==": {Kind: domain.SignalLinkGroup, Invite: invite},
		"HTTPS://Signal.Group/#" + group:       {Kind: domain.SignalLinkGroup, Invite: invite},
	} {
		got, ok := domain.ParseSignalLink(uri)
		if !ok || got.Kind != want.Kind || got.Phone != want.Phone ||
			!bytes.Equal(got.Username, want.Username) || !bytes.Equal(got.Invite, want.Invite) {
			t.Errorf("ParseSignalLink(%q) = (%+v, %v), want %+v", uri, got, ok, want)
		}
		if network, ok := domain.LinkNetwork(uri); !ok || network != domain.ProtocolSignal {
			t.Errorf("LinkNetwork(%q) = (%q, %v), want Signal", uri, network, ok)
		}
	}
}

// Links that name nobody kith can open are refused.
func TestParseSignalLinkRefusesWhatIsNotOne(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"",
		"https://signal.me/",
		"https://signal.me/#p/+1234",             // too short to be a number
		"https://signal.me/#p/+1555010000912345", // longer than E.164
		"https://signal.me/#eu/" + base64.RawURLEncoding.EncodeToString(make([]byte, 47)),
		"https://signal.me/#u/someone.42",
		"https://signal.group/",
		"https://signal.group/#not*base64",
		"https://signal.org/#p/+15550100009",
		"sgnl://linkdevice?uuid=abc&pub_key=def",
	} {
		if got, ok := domain.ParseSignalLink(uri); ok {
			t.Errorf("ParseSignalLink(%q) = %+v, want not a Signal link", uri, got)
		}
	}
}
