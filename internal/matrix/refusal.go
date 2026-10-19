package matrix

import (
	"errors"
	"fmt"

	"maunium.net/go/mautrix"

	"github.com/EugeneShtoka/kith/internal/api"
)

// refusal marks a send the homeserver refused for good, which no retry can change:
// forbidden (not in the room, or not allowed to post), too large, or the room unknown.
// Anything else (unreachable, rate-limited, a server error) may pass.
func refusal(err error) error {
	for _, final := range []error{mautrix.MForbidden, mautrix.MTooLarge, mautrix.MNotFound} {
		if errors.Is(err, final) {
			return fmt.Errorf("%w: %w", api.ErrRefused, err)
		}
	}
	return err
}
