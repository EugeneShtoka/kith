package meta

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/api"
)

// Meta's answer to a block-status task refuses the change only when its row for that
// person says it failed.
func TestABlockIsRefusedOnlyByItsOwnRow(t *testing.T) {
	t.Parallel()
	row := func(who int64, ok bool) *table.LSResetUserBlockStatus {
		return &table.LSResetUserBlockStatus{ContactID: who, Success: ok}
	}
	for name, c := range map[string]struct {
		resp *table.LSTable
		want error
	}{
		"no answer":       {nil, nil},
		"no row":          {&table.LSTable{}, nil},
		"took it":         {&table.LSTable{LSResetUserBlockStatus: []*table.LSResetUserBlockStatus{row(dana, true)}}, nil},
		"refused":         {&table.LSTable{LSResetUserBlockStatus: []*table.LSResetUserBlockStatus{row(dana, false)}}, errBlockRefused},
		"refused another": {&table.LSTable{LSResetUserBlockStatus: []*table.LSResetUserBlockStatus{row(me, false), row(dana, true)}}, nil},
	} {
		if got := blockRefused(c.resp, dana); !errors.Is(got, c.want) || (got == nil) != (c.want == nil) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// Blocking needs a connected account and a Messenger person.
func TestBlockingNeedsAConnectionAndAMessengerPerson(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _, _, _ := cachedAdapter(t)
	if err := a.BlockPerson(ctx, personID(dana)); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("block while not connected = %v", err)
	}
	if err := a.UnblockPerson(ctx, "instagram:2000"); err == nil || errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("unblock of an Instagram person = %v", err)
	}
}
