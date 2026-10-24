package domain_test

import (
	"reflect"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every spelling of a link to a person or a group.
func TestParseWhatsAppLinkReadsEverySpelling(t *testing.T) {
	t.Parallel()

	phone := func(digits, text string) domain.WhatsAppLink {
		return domain.WhatsAppLink{Kind: domain.WhatsAppLinkPhone, Phone: digits, Text: text}
	}
	invite := domain.WhatsAppLink{Kind: domain.WhatsAppLinkInvite, Invite: "Ab12Cd34Ef56Gh78Ij90Kl"}
	for uri, want := range map[string]domain.WhatsAppLink{
		"whatsapp://send?phone=15550100009":                         phone("15550100009", ""),
		"whatsapp://send?phone=15550100009&text=hi%20there":         phone("15550100009", "hi there"),
		"whatsapp://send/?phone=%2B1%20555%20010-0009":              phone("15550100009", ""),
		"https://wa.me/15550100009":                                 phone("15550100009", ""),
		"https://wa.me/15550100009?text=hi+there":                   phone("15550100009", "hi there"),
		"wa.me/15550100009/":                                        phone("15550100009", ""),
		"https://api.whatsapp.com/send?phone=15550100009&text=yo":   phone("15550100009", "yo"),
		"https://api.whatsapp.com/send/?phone=15550100009":          phone("15550100009", ""),
		"https://web.whatsapp.com/send?phone=15550100009":           phone("15550100009", ""),
		"http://www.wa.me/15550100009":                              phone("15550100009", ""),
		"whatsapp://chat?code=Ab12Cd34Ef56Gh78Ij90Kl":               invite,
		"https://chat.whatsapp.com/Ab12Cd34Ef56Gh78Ij90Kl":          invite,
		"https://chat.whatsapp.com/invite/Ab12Cd34Ef56Gh78Ij90Kl":   invite,
		"chat.whatsapp.com/Ab12Cd34Ef56Gh78Ij90Kl?utm_source=share": invite,
	} {
		got, ok := domain.ParseWhatsAppLink(uri)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseWhatsAppLink(%q) = (%+v, %v), want %+v", uri, got, ok, want)
		}
	}
}

// Links that name nobody kith can open are refused.
func TestParseWhatsAppLinkRefusesWhatIsNotOne(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"",
		"https://example.org/15550100009",
		"tg://resolve?domain=novosalsa",
		"whatsapp://send?text=hello",     // a share: nobody named
		"https://wa.me/?text=hello",      // the same, on the web
		"https://wa.me/message/ABCDEF12", // a business's short link
		"https://wa.me/1234",             // too short to be a number
		"https://wa.me/1555010000912345", // longer than E.164
		"https://api.whatsapp.com/catalog/15550100009",
		"https://chat.whatsapp.com/",
		"https://chat.whatsapp.com/bad/code",
		"whatsapp://chat?code=",
		"whatsapp://call?phone=15550100009",
	} {
		if got, ok := domain.ParseWhatsAppLink(uri); ok {
			t.Errorf("ParseWhatsAppLink(%q) = %+v, want not a WhatsApp link", uri, got)
		}
	}
}

// A link's network is the one whose parser reads it.
func TestLinkNetworkNamesTheLinksNetwork(t *testing.T) {
	t.Parallel()

	for uri, want := range map[string]domain.Protocol{
		"tg://resolve?domain=novosalsa": domain.ProtocolTelegram,
		"https://t.me/+15550100009":     domain.ProtocolTelegram,
		"https://wa.me/15550100009":     domain.ProtocolWhatsApp,
		"whatsapp://chat?code=AbCdEf":   domain.ProtocolWhatsApp,
	} {
		if got, ok := domain.LinkNetwork(uri); !ok || got != want {
			t.Errorf("LinkNetwork(%q) = (%q, %v), want %q", uri, got, ok, want)
		}
	}
	if got, ok := domain.LinkNetwork("https://matrix.to/#/!a:x"); ok {
		t.Errorf("a Matrix link's network = %q, want none: the client joins Matrix rooms itself", got)
	}
}
