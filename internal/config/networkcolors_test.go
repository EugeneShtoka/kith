package config

import "testing"

// A network's color is as set, else its default; "" for "none", no default, or an
// unknown network. Names match in any case, spaces as underscores.
func TestANetworksColor(t *testing.T) {
	t.Parallel()
	n := NetworkColors{Telegram: "#123456", Slack: "None", GoogleMessages: "cyan"}
	for network, want := range map[string]string{
		"WhatsApp": "teal", "Telegram": "#123456", "Slack": "", "Google Messages": "cyan",
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

// Default network colors are distinct, except Meta's two networks.
func TestNetworksDefaultColorsAreApart(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for network, color := range defaultNetworkColors {
		if other, ok := seen[color]; ok && !meta(network, other) {
			t.Errorf("%s and %s are both %s", network, other, color)
		}
		seen[color] = network
	}
}

func meta(a, b string) bool {
	return (a == "messenger" && b == "instagram") || (a == "instagram" && b == "messenger")
}
