package matrix

import (
	"errors"
	"fmt"
	"testing"

	"maunium.net/go/mautrix"

	"github.com/EugeneShtoka/kith/internal/api"
)

// A send the homeserver refused for good is marked so; one that may pass is not.
func TestASendRefusedForGoodIsMarkedSo(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"forbidden":      {mautrix.MForbidden, true},
		"too large":      {mautrix.MTooLarge, true},
		"no such room":   {mautrix.MNotFound, true},
		"rate-limited":   {mautrix.MLimitExceeded, false},
		"server trouble": {mautrix.MUnknown, false},
		"unreachable":    {errors.New("dial tcp: connection refused"), false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := errors.Is(refusal(fmt.Errorf("send: %w", tc.err)), api.ErrRefused)
			if got != tc.want {
				t.Errorf("refused for good = %v, want %v", got, tc.want)
			}
		})
	}
}
