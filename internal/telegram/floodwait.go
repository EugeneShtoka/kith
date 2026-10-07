package telegram

import (
	"context"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Telegram answers an account asking too fast with FLOOD_WAIT and how long to wait.
// A short wait is waited out and the call asked again, so a burst (a forum's topics
// read one after another, quotes fetched while scrolling) slows down instead of
// failing; a long one fails the call, which its caller may wait out itself (backfill).

// floodWaitLimit is the longest wait a call waits out by itself; floodRetries how many
// times it asks again.
const (
	floodWaitLimit = 30 * time.Second
	floodRetries   = 3
)

// waitOutFloods is the middleware that waits out short FLOOD_WAITs.
func waitOutFloods() telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			for try := 0; ; try++ {
				err := next.Invoke(ctx, input, output)
				wait, flooded := tgerr.AsFloodWait(err)
				if !flooded || wait > floodWaitLimit || try >= floodRetries {
					return err //nolint:wrapcheck // the call's own error, for its caller to name
				}
				if !sleep(ctx, wait) {
					return err //nolint:wrapcheck // as above: ended while waiting
				}
			}
		}
	})
}
