package signal_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The block list is mirrored once the account connects, so a block made on the phone
// shows; a block or an unblock from kith asks the helper for that person by ACI and
// the mirror follows. Someone who is not a Signal person by ACI is refused.
func TestBlockingAsksTheHelperAndTheMirrorFollows(t *testing.T) {
	r := connected(t)
	ctx := context.Background()
	mirror := func(want ...string) func() bool {
		return func() bool {
			got, err := r.a.Blocked(ctx)
			return err == nil && slices.Equal(got, want)
		}
	}
	r.until("the phone's block mirrored", mirror("signal:"+ofer))

	if err := r.a.BlockPerson(ctx, "signal:"+dana); err != nil {
		t.Fatal(err)
	}
	r.until("dana mirrored as blocked", mirror("signal:"+dana, "signal:"+ofer))
	if err := r.a.UnblockPerson(ctx, "signal:"+dana); err != nil {
		t.Fatal(err)
	}
	r.until("dana mirrored as unblocked", mirror("signal:"+ofer))
	b, _ := os.ReadFile(r.sent)
	sent := strings.ReplaceAll(string(b), " ", "")
	if want := `{"account":"` + me + `","person":"` + dana + `","blocked":true}`; !strings.Contains(sent, want) {
		t.Errorf("the helper was not asked to block: %s, want %s", sent, want)
	}
	if want := `{"account":"` + me + `","person":"` + dana + `"}`; !strings.Contains(sent, want) {
		t.Errorf("the helper was not asked to unblock: %s, want %s", sent, want)
	}

	for _, who := range []string{"signal:not-an-aci", domain.NativePerson(domain.ProtocolTelegram, "77")} {
		if err := r.a.BlockPerson(ctx, who); err == nil {
			t.Errorf("blocked %s", who)
		}
	}
}
