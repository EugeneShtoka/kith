package config

import "testing"

// A network's color is as set, else its default (WhatsApp green, Telegram blue, Slack
// magenta), none for "none", for a network with no default, or one not known; a
// network is named as its domain name is, any case, spaces as underscores.
func TestANetworksColor(t *testing.T) {
	t.Parallel()
	n := NetworkColors{Telegram: "#123456", Slack: "None", GoogleMessages: "cyan"}
	for network, want := range map[string]string{
		"WhatsApp": "green", "Telegram": "#123456", "Slack": "", "Google Messages": "cyan",
		"Signal": "", "Matrix": "", "Carrier Pigeon": "",
	} {
		if got := n.For(network); got != want {
			t.Errorf("For(%q) = %q, want %q", network, got, want)
		}
	}
	if got := (NetworkColors{}).For("slack"); got != "magenta" {
		t.Errorf("Slack unset = %q, want magenta", got)
	}
}
