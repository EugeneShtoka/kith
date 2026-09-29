package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Two bridges for one network on one homeserver need distinct bot names, and mautrix's
// convention is an instance suffix.
func TestInstanceSuffixedBridgeBots(t *testing.T) {
	t.Parallel()

	bridged := map[string]domain.Protocol{
		"@whatsappbot_bg:example.org":       domain.ProtocolWhatsApp,
		"@whatsappbot_il:example.org":       domain.ProtocolWhatsApp,
		"@whatsapp_bg_44770123:example.org": domain.ProtocolWhatsApp,
		"@whatsapp_il_97250123:example.org": domain.ProtocolWhatsApp,
		"@linkedinbot:example.org":          domain.ProtocolLinkedIn,
		"@slackbot:example.org":             domain.ProtocolSlack,
		"@telegrambot:example.org":          domain.ProtocolTelegram,
		"@gmessagesbot:example.org":         domain.ProtocolGMessages,
		"@metabot:example.org":              domain.ProtocolMessenger,
		"@signalbot:example.org":            domain.ProtocolSignal,
		// A dash or a dot separates an instance just as well as an underscore.
		"@slackbot-work:example.org": domain.ProtocolSlack,
		"@metabot.two:example.org":   domain.ProtocolMessenger,
	}
	for mxid, want := range bridged {
		if got := domain.ProtocolOf(mxid); got != want {
			t.Errorf("ProtocolOf(%q) = %q, want %q", mxid, got, want)
		}
	}

	// A suffix has to start with a separator, or this stops being a rule about bots and
	// becomes a prefix search over everybody's name.
	for _, mxid := range []string{
		"@whatsappbotanist:example.org",
		"@robot:example.org",
		"@eugene:example.org",
		"@slackbotany:example.org",
	} {
		if got := domain.ProtocolOf(mxid); got != domain.ProtocolMatrix {
			t.Errorf("ProtocolOf(%q) = %q, want a person", mxid, got)
		}
	}
}
