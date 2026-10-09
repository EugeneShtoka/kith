package setup_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// A network color is "#rrggbb", a named color, "none", or empty; anything else stops
// startup, naming the key.
func TestNetworkColorsAreColors(t *testing.T) {
	t.Parallel()
	if err := setup.NetworkColors(config.NetworkColors{WhatsApp: "#00ff00", Telegram: "Blue", Slack: "none"}); err != nil {
		t.Errorf("good colors refused: %v", err)
	}
	err := setup.NetworkColors(config.NetworkColors{Signal: "chartreuse"})
	if err == nil || !strings.Contains(err.Error(), "display.theme.networks.signal") {
		t.Errorf("a bad color = %v, want refused naming display.theme.networks.signal", err)
	}
	if err := setup.NetworkColors(config.NetworkColors{Telegram: "#12345"}); err == nil {
		t.Error("a short hex was taken")
	}
}
