package instagram

import (
	"context"
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
)

const (
	block   = "IGDBlockUserDialogOffMsysMutation"
	unblock = "IGDBlockUserDialogOffMsysUnblockMutation"
)

// Blocking names the person to Instagram by the IG ID their threads gave, not kith's
// messaging ID; someone no thread has shown is refused without a call, as is anyone
// while no account is connected, and Instagram refusing is an error.
func TestBlockingNamesThePersonByTheirIGID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t)
	r.thread(dm())
	dana, ofer := person(fbid(danaFBID)), person(fbid(oferFBID))

	if err := r.a.BlockPerson(ctx, dana); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("block while not connected = %v", err)
	}
	f := r.connect()
	f.data[block] = `{"xdt_block_v2":{}}`
	f.data[unblock] = `{"xdt_unblock":{}}`
	if err := r.a.BlockPerson(ctx, dana); err != nil {
		t.Fatal(err)
	}
	if err := r.a.UnblockPerson(ctx, dana); err != nil {
		t.Fatal(err)
	}
	if calls := f.called(block); len(calls) != 1 || field(calls[0], "target_user_id") != danaIGID {
		t.Errorf("blocked %v", calls)
	}
	if calls := f.called(unblock); len(calls) != 1 || field(calls[0], "target_user_id") != danaIGID {
		t.Errorf("unblocked %v", calls)
	}

	if err := r.a.BlockPerson(ctx, ofer); err == nil {
		t.Error("blocked someone no thread has shown")
	}
	if err := r.a.BlockPerson(ctx, "messenger:2000"); err == nil {
		t.Error("blocked a Messenger person")
	}
	if calls := f.called(block); len(calls) != 1 {
		t.Errorf("blocked %v", calls)
	}

	f.refuse[unblock] = true
	if err := r.a.UnblockPerson(ctx, dana); err == nil {
		t.Error("a refused unblock went through")
	}
}
