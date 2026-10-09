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

// chatEnder records rooms left and chats deleted, and fails a deletion when told to.
type chatEnder struct {
	apitest.Nop
	left    *[]domain.RoomID
	deleted *[]string
	fail    bool
}

func (c chatEnder) LeaveRoom(_ context.Context, room domain.RoomID) error {
	*c.left = append(*c.left, room)
	return nil
}

func (c chatEnder) DeleteChat(_ context.Context, room domain.RoomID, forEveryone bool) error {
	if c.fail {
		return errors.New("refused")
	}
	who := "me"
	if forEveryone {
		who = "everyone"
	}
	*c.deleted = append(*c.deleted, string(room)+" for "+who)
	return nil
}

// L on a chat, over every way a chat is put away, every answer and a refusal: a room
// that is left is left, never deleted; a chat deleted for you alone asks once; one the
// other person can lose too asks that first (no deleting it for you only), and last,
// saying for whom, whether to delete it; nothing goes before that last yes, and a no
// there deletes nothing; a deleted chat leaves the list, a refused one stays and says
// why.
func TestDeletingAChatThatCannotBeLeft(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(5, 8))
	ways := []domain.ChatDeleting{domain.ChatLeft, domain.ChatDeletedForMe, domain.ChatDeletedForEither}
	for i := range 200 {
		way, fail := ways[r.IntN(len(ways))], r.IntN(5) == 0
		var left []domain.RoomID
		var deleted []string
		room := domain.Room{ID: "telegram:1/7", Name: "Dana", IsDirect: true, Deleting: way}
		m := update(t, New(context.Background(), chatEnder{left: &left, deleted: &deleted, fail: fail}, config.Display{}),
			roomsMsg{rooms: []domain.Room{room, {ID: "telegram:1/8", Name: "Eli", IsDirect: true}}})
		m = sized(t, m)
		m.focus = paneRooms
		m = pressKey(t, m, "L")

		if way == domain.ChatLeft {
			if m.confirm.action != pendingLeave {
				t.Fatalf("case %d: a room that is left asked %v %q", i, m.confirm.action, m.confirmPrompt())
			}
			continue
		}
		forEveryone := false
		if way == domain.ChatDeletedForEither {
			if m.confirm.action != pendingDeleteForThem {
				t.Fatalf("case %d: first question %v %q, want whether they lose it too", i, m.confirm.action, m.confirmPrompt())
			}
			answer := []string{"y", "n"}[r.IntN(2)]
			forEveryone = answer == "y"
			m = pressKey(t, m, answer)
		}
		if m.confirm.action != pendingDeleteChat || strings.Contains(m.confirmPrompt(), "both") != forEveryone {
			t.Fatalf("case %d: last question %v %q, for everyone: %v", i, m.confirm.action, m.confirmPrompt(), forEveryone)
		}
		if len(deleted) > 0 || len(left) > 0 {
			t.Fatalf("case %d: deleted %v, left %v before the last yes", i, deleted, left)
		}
		if r.IntN(4) == 0 {
			m = pressKey(t, m, "n")
			if len(deleted) > 0 || m.confirm.active() {
				t.Fatalf("case %d: a last no deleted %v (confirm %v)", i, deleted, m.confirm.action)
			}
			continue
		}
		next, cmd := asModel(m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"}))
		msg, ok := msgOf[leftMsg](t, cmd)
		if !ok {
			t.Fatalf("case %d: the last yes deleted nothing", i)
		}
		next = update(t, next, msg)
		_, listed := next.roomByID(room.ID)
		if len(left) > 0 {
			t.Fatalf("case %d: a chat to delete was left: %v", i, left)
		}
		if fail {
			if !listed || !strings.Contains(next.status(), "delete failed") {
				t.Fatalf("case %d: a refused deletion: listed %v, status %q", i, listed, next.status())
			}
			continue
		}
		want := "telegram:1/7 for me"
		if forEveryone {
			want = "telegram:1/7 for everyone"
		}
		if len(deleted) != 1 || deleted[0] != want || listed || !strings.Contains(next.status(), "deleted") {
			t.Fatalf("case %d: deleted %v (want %s), still listed %v, status %q", i, deleted, want, listed, next.status())
		}
	}
}
