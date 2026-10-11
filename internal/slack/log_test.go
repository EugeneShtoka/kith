package slack

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// slack-go's lines reach kith's log with tokens and cookies redacted, including in the
// reconnect URL.
func TestSlackGoLogsNoSession(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := slackLog{log: log}
	for _, line := range []string{
		"Updated reconnect URL to wss://wss-primary.slack.com/?frt=abc/2&token=xoxc-1-2-3-abc\n",
		"Using URL: wss://x/?token=xoxc-1-2-3-abc&ts=1\n",
		"cookie d=xoxd-DR5W+n3K/MIKx== set\n",
	} {
		_ = l.Output(2, line)
	}
	got := out.String()
	for _, secret := range []string{"xoxc-1-2-3-abc", "xoxd-DR5W"} {
		if strings.Contains(got, secret) {
			t.Errorf("the log holds %q:\n%s", secret, got)
		}
	}
	if strings.Count(got, "[redacted]") != 3 || !strings.Contains(got, "frt=abc/2") {
		t.Errorf("the lines should stay, the session cut out:\n%s", got)
	}
}
