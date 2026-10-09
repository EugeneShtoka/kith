package tui

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// signOuter records accounts signed out, and refuses when told to.
type signOuter struct {
	apitest.Nop
	signedOut *[]string
	fail      bool
}

func (s signOuter) SignOut(_ context.Context, space domain.SpaceID, forget bool) error {
	if s.fail {
		return errors.New("not connected")
	}
	how := " keeping its rooms"
	if forget {
		how = " forgetting its rooms"
	}
	*s.signedOut = append(*s.signedOut, string(space)+how)
	return nil
}

// L on an account's own space, over every answer and a refusal: whether its rooms go
// too is asked first, and a no keeps them; last, saying whether they go, whether to
// sign out; nothing happens before that last yes, and a no there signs nothing out; a
// refusal says why, and nothing else on the rail is left.
func TestSigningAnAccountOutFromTheRail(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(3, 4))
	account := domain.Space{ID: "telegram:1/account", Name: "Telegram home", Bridge: domain.ProtocolTelegram,
		Original: true, Leaving: domain.LeftBySigningOut, Children: []domain.RoomID{"telegram:1/7", "telegram:1/8"}}
	for i := range 100 {
		fail := r.IntN(5) == 0
		var signedOut []string
		m := update(t, New(context.Background(), signOuter{signedOut: &signedOut, fail: fail}, config.Display{}),
			roomsMsg{rooms: []domain.Room{{ID: "telegram:1/7", Name: "Dana", IsDirect: true}, {ID: "telegram:1/8", Name: "Eli", IsDirect: true}}})
		m = sized(t, update(t, m, spacesMsg{spaces: []domain.Space{account}}))
		m.focus = paneRail
		m.rail.cursor = indexOfGroup(m.rail.groups, "Telegram home")
		m = pressKey(t, m, "L")
		if m.confirm.action != pendingSignOutForget || !strings.Contains(m.confirmPrompt(), "2 rooms") {
			t.Fatalf("case %d: first question %v %q, want whether its rooms go", i, m.confirm.action, m.confirmPrompt())
		}
		forget := r.IntN(2) == 0
		m = pressKey(t, m, map[bool]string{true: "y", false: "n"}[forget])
		if m.confirm.action != pendingSignOut || strings.Contains(m.confirmPrompt(), "go from kith") != forget {
			t.Fatalf("case %d: last question %v %q, forgetting: %v", i, m.confirm.action, m.confirmPrompt(), forget)
		}
		if len(signedOut) > 0 {
			t.Fatalf("case %d: signed out %v before the last yes", i, signedOut)
		}
		if r.IntN(4) == 0 {
			m = pressKey(t, m, "n")
			if len(signedOut) > 0 || m.confirm.active() {
				t.Fatalf("case %d: a last no signed out %v", i, signedOut)
			}
			continue
		}
		next, cmd := asModel(m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"}))
		msg, ok := msgOf[signedOutMsg](t, cmd)
		if !ok {
			t.Fatalf("case %d: the last yes signed nothing out", i)
		}
		next = update(t, next, msg)
		if fail {
			if len(signedOut) > 0 || !strings.Contains(next.status(), "could not sign out of Telegram home: not connected") {
				t.Fatalf("case %d: a refusal: signed out %v, status %q", i, signedOut, next.status())
			}
			continue
		}
		want := "telegram:1/account keeping its rooms"
		if forget {
			want = "telegram:1/account forgetting its rooms"
		}
		if len(signedOut) != 1 || signedOut[0] != want || !strings.Contains(next.status(), "signed out of Telegram home") {
			t.Fatalf("case %d: signed out %v, want %s; status %q", i, signedOut, want, next.status())
		}
	}
}
