package telegram

import (
	"context"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Short FLOOD_WAITs are waited out and retried, so bursts (forum topics, quotes while
// scrolling) slow down instead of failing; long ones fail, for callers like backfill
// to handle.

// floodWaitLimit is the longest wait retried automatically; floodRetries the retries.
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
