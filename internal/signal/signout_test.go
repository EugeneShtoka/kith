package signal_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/signal"
)

// Signing out from the account's space unlinks the device through the helper, forgets
// which Signal account the name was (its space goes, the account is no longer linked),
// and, asked to, its rooms; a space no account has is refused.
func TestSigningOutUnlinksTheDevice(t *testing.T) {
	for _, forget := range []bool{false, true} {
		r := connected(t, msg(dana, dana, 1700000000100, "hi"))
		ctx := context.Background()
		space := domain.SpaceID("signal:" + me + "/account")
		if err := r.a.SignOut(ctx, "signal:nobody/account", forget); err == nil {
			t.Error("signed out of a space no account has")
		}
		if err := r.a.SignOut(ctx, space, forget); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(r.sent)
		if !slices.Contains(strings.Split(strings.TrimSpace(string(b)), "\n"), `{"account":"`+me+`"}`) {
			t.Errorf("the helper was not asked to unlink %s: %s", me, b)
		}
		spaces, err := r.a.Spaces(ctx)
		if err != nil || len(spaces) != 0 {
			t.Errorf("spaces after signing out: %v, %v", spaces, err)
		}
		r.until(5*time.Second, "unlinked", r.phase(signal.Unlinked, ""))
		if _, kept := r.room(direct(dana)); kept == forget {
			t.Errorf("forget=%v: the chat kept = %v", forget, kept)
		}
	}
}
