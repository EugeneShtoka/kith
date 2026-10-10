package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestProtocolOf(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mxid string
		want domain.Protocol
	}{
		// Shapes observed in this account's own cache.
		"whatsapp ghost":  {"@whatsapp_447009001234:example.org", domain.ProtocolWhatsApp},
		"telegram ghost":  {"@telegram_1234567:example.org", domain.ProtocolTelegram},
		"slack ghost":     {"@slack_t0abc1234-u09wxyz:example.org", domain.ProtocolSlack},
		"meta ghost":      {"@meta_100001234567890:example.org", domain.ProtocolMessenger},
		"gmessages ghost": {"@gmessages_2b3c4d:example.org", domain.ProtocolGMessages},
		// linkedin's bridge doubles the separator; the token still matches.
		"linkedin ghost": {"@linkedin__abc123:example.org", domain.ProtocolLinkedIn},

		// Bridge bots carry the token with a "bot" suffix and no separator.
		"slack bot":    {"@slackbot:example.org", domain.ProtocolSlack},
		"whatsapp bot": {"@whatsappbot:example.org", domain.ProtocolWhatsApp},
		// Some bridges prefix the whole localpart with an underscore.
		"underscore-prefixed ghost": {"@_discord_11223344:example.org", domain.ProtocolDiscord},
		// Case is not significant in the bridge token.
		"mixed case": {"@WhatsApp_44700:example.org", domain.ProtocolWhatsApp},

		// Everything else is a native Matrix user.
		"native":               {"@someone:example.org", domain.ProtocolMatrix},
		"native with usebot":   {"@robot:example.org", domain.ProtocolMatrix},
		"unknown bridge":       {"@zulip_42:example.org", domain.ProtocolMatrix},
		"underscore localpart": {"@my_name:example.org", domain.ProtocolMatrix},
		"leading separator":    {"@_private:example.org", domain.ProtocolMatrix},
		"no server":            {"@whatsapp_44700", domain.ProtocolWhatsApp},
		"malformed":            {"nonsense", domain.ProtocolMatrix},
		"empty":                {"", domain.ProtocolMatrix},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := domain.ProtocolOf(tc.mxid); got != tc.want {
				t.Errorf("ProtocolOf(%q) = %q, want %q", tc.mxid, got, tc.want)
			}
		})
	}
}

func TestProtocolIsBridged(t *testing.T) {
	t.Parallel()

	if domain.ProtocolMatrix.IsBridged() {
		t.Error("Matrix must not report as bridged")
	}
	if !domain.ProtocolWhatsApp.IsBridged() {
		t.Error("WhatsApp must report as bridged")
	}
	if got := domain.ProtocolGMessages.String(); got != "Google Messages" {
		t.Errorf("String() = %q, want %q", got, "Google Messages")
	}
}

// The one network where a reply is always a thread.
func TestRepliesAreThreads(t *testing.T) {
	t.Parallel()

	for _, p := range []domain.Protocol{domain.ProtocolSlack} {
		if !p.RepliesAreThreads() {
			t.Errorf("%s: RepliesAreThreads() = false, want true", p)
		}
	}
	for _, p := range []domain.Protocol{
		domain.ProtocolMatrix, domain.ProtocolWhatsApp, domain.ProtocolTelegram,
		domain.ProtocolSignal, domain.ProtocolMessenger, domain.ProtocolDiscord,
	} {
		if p.RepliesAreThreads() {
			t.Errorf("%s: RepliesAreThreads() = true, want false — it has a reply that is not a thread", p)
		}
	}
}

// A bridge's own bot is not one of the people it puts in rooms.
func TestIsBridgeBot(t *testing.T) {
	t.Parallel()
	for mxid, want := range map[string]bool{
		"@telegrambot:x":      true,
		"@whatsappbot_il:x":   true,
		"@LinkedInBot:x":      true,
		"@telegram_123456:x":  false,
		"@linkedin_abc:x":     false,
		"@telegrambotanist:x": false,
		"@alice:x":            false,
		"":                    false,
	} {
		if got := domain.IsBridgeBot(mxid); got != want {
			t.Errorf("IsBridgeBot(%q) = %v, want %v", mxid, got, want)
		}
	}
}
