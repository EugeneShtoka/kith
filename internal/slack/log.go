package slack

import (
	"log/slog"
	"regexp"
	"strings"
)

// slack-go logs to stderr by default, and its websocket reconnect URL carries the token.
// A daemon's stderr is the journal, so its lines go to kith's log at debug, redacted.

// secrets matches a Slack token or a token= parameter.
var secrets = regexp.MustCompile(`xox[a-z]-[A-Za-z0-9-]+|token=[^&\s]+`)

func redacted(line string) string { return secrets.ReplaceAllString(line, "[redacted]") }

// slackLog is slack-go's logger, writing to log.
type slackLog struct{ log *slog.Logger }

// Output receives one line from slack-go.
func (l slackLog) Output(_ int, line string) error {
	l.log.Debug(redacted(strings.TrimSpace(line)), "component", "slack-go")
	return nil
}
