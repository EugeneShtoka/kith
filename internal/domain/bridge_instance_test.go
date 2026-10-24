package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Two bridges for one network on one homeserver get an instance suffix (mautrix convention).
func TestInstanceSuffixedBridgeBots(t *testing.T) {
	t.Parallel()

	bridged := map[string]domain.Protocol{
		"matrix:@whatsappbot_bg:example.org":       domain.ProtocolWhatsApp,
		"matrix:@whatsappbot_il:example.org":       domain.ProtocolWhatsApp,
		"matrix:@whatsapp_bg_44770123:example.org": domain.ProtocolWhatsApp,
		"matrix:@whatsapp_work_150123:example.org": domain.ProtocolWhatsApp,
		"matrix:@linkedinbot:example.org":          domain.ProtocolLinkedIn,
		"matrix:@slackbot:example.org":             domain.ProtocolSlack,
		"matrix:@telegrambot:example.org":          domain.ProtocolTelegram,
		"matrix:@gmessagesbot:example.org":         domain.ProtocolGMessages,
		"matrix:@metabot:example.org":              domain.ProtocolMessenger,
		"matrix:@signalbot:example.org":            domain.ProtocolSignal,
		// A dash or a dot separates an instance just as well as an underscore.
		"matrix:@slackbot-work:example.org": domain.ProtocolSlack,
		"matrix:@metabot.two:example.org":   domain.ProtocolMessenger,
	}
	for mxid, want := range bridged {
		if got := domain.ProtocolOf(mxid); got != want {
			t.Errorf("ProtocolOf(%q) = %q, want %q", mxid, got, want)
		}
	}

	// A suffix must start with a separator, or it would match any name with the prefix.
	for _, mxid := range []string{
		"matrix:@whatsappbotanist:example.org",
		"matrix:@robot:example.org",
		"matrix:@eugene:example.org",
		"matrix:@slackbotany:example.org",
	} {
		if got := domain.ProtocolOf(mxid); got != domain.ProtocolMatrix {
			t.Errorf("ProtocolOf(%q) = %q, want a person", mxid, got)
		}
	}
}
