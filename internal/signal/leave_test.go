package signal_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving a group asks the helper to take the account out of it, and the group goes
// from the cache; a one-to-one chat cannot be left, and stays.
func TestLeavingAGroupTakesTheAccountOut(t *testing.T) {
	r := connected(t, inGroup(msg(groupID, dana, 1700000000100, "hi all")), msg(dana, dana, 1700000000200, "hi"))
	ctx := context.Background()
	group := domain.RoomID("signal:" + me + "/" + "-_8BAgMEBQ")
	if _, ok := r.room(group); !ok {
		t.Fatal("the group was not cached")
	}
	if err := r.a.LeaveRoom(ctx, direct(dana)); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("leaving a one-to-one chat = %v, want ErrNotOnNetwork", err)
	}
	if _, ok := r.room(direct(dana)); !ok {
		t.Error("the one-to-one chat went though it cannot be left")
	}
	if err := r.a.LeaveRoom(ctx, group); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(r.sent)
	if want := `{"account":"` + me + `","group":"` + groupID + `"}`; !strings.Contains(strings.ReplaceAll(string(b), " ", ""), want) {
		t.Errorf("the helper was not asked to leave: %s, want %s", b, want)
	}
	if _, ok := r.room(group); ok {
		t.Error("the group left is still cached")
	}
}
