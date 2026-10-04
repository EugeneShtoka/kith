package slack

import (
	"log/slog"
	"regexp"
	"strings"
)

// slack-go logs what it does to stderr unless told otherwise, and what it logs
// carries the session: the websocket's reconnect URL has the token in its query.
// Whoever reads the token can act as the person, and a daemon's stderr is the
// journal, so its lines go through kith's log at debug, the session cut out.

// secrets finds a session in a line: a Slack token, or a token= parameter.
var secrets = regexp.MustCompile(`xox[a-z]-[A-Za-z0-9-]+|token=[^&\s]+`)

// redacted is a line with any session in it cut out.
func redacted(line string) string { return secrets.ReplaceAllString(line, "[redacted]") }

// slackLog is slack-go's logger, writing to log.
type slackLog struct{ log *slog.Logger }

// Output is how slack-go hands over a line.
func (l slackLog) Output(_ int, line string) error {
	l.log.Debug(redacted(strings.TrimSpace(line)), "component", "slack-go")
	return nil
}
