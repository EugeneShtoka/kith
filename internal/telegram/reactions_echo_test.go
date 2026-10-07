package telegram

import (
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A send's answer, applied when sent, comes again through the updates, perhaps after
// a later send's: it does not put back what the later one changed. Someone else's
// change, heard after, still counts.
func TestALateEchoOfOurReactionDoesNotUndoALaterOne(t *testing.T) {
	t.Parallel()
	a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	ctx := t.Context()
	room := roomID(42, -11)
	target := inRoom(room, 5)
	if err := cache.SaveMessages(ctx, room, []domain.Message{{ID: target, RoomID: room, Body: "hike?", Timestamp: time.Unix(1000, 0)}}); err != nil {
		t.Fatal(err)
	}
	ours := func(key string) []domain.Reaction {
		return []domain.Reaction{{ID: reactionID(target, "42", key), RoomID: room, Target: target, Sender: personID(42), Key: key}}
	}
	keys := func() []string {
		got, _ := cache.Reactions(ctx, room)
		var out []string
		for _, r := range got {
			out = append(out, r.Key+" "+r.Sender)
		}
		slices.Sort(out)
		return out
	}
	thumbs, party := ours("👍"), ours("🎉")
	a.sentReactions(target, thumbs) // sent 👍: its answer applied
	a.reactionsChanged(ctx, room, target, thumbs)
	a.sentReactions(target, party) // then 🎉, replacing it
	a.reactionsChanged(ctx, room, target, party)
	a.heardReactions(ctx, room, target, thumbs) // the 👍 answer, late, through the updates
	if got := keys(); !slices.Equal(got, []string{"🎉 telegram:42"}) {
		t.Errorf("after the late echo: %q, want 🎉 kept", got)
	}
	theirs := append(slices.Clone(party), domain.Reaction{ID: reactionID(target, "7", "👍"), RoomID: room, Target: target, Sender: personID(7), Key: "👍"})
	a.heardReactions(ctx, room, target, theirs)
	if got := keys(); len(got) != 2 {
		t.Errorf("someone else's reaction after: %q, want it counted", got)
	}
}
