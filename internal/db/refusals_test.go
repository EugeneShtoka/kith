package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// What a bridge refuses is keyed on the protocol, not the room: what Telegram will not
// take in one chat it will not take in any of them.
func TestReactionRefusalsRoundTrip(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	if got, err := cache.ReactionRefusals(ctx); err != nil || len(got) != 0 {
		t.Fatalf("cold table = %v, %v; want empty", got, err)
	}

	first := time.Now().Truncate(time.Millisecond)
	for _, r := range []struct {
		protocol, emoji string
	}{{"Telegram", "🫶"}, {"Telegram", "🤯"}, {"RCS", "🫶"}} {
		if serr := cache.SaveReactionRefusal(ctx, r.protocol, r.emoji, first); serr != nil {
			t.Fatal(serr)
		}
	}
	got, err := cache.ReactionRefusals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d refusals, want 3: %+v", len(got), got)
	}

	// Re-recording the same one moves its timestamp rather than duplicating the row:
	// the record says when it was last seen, and the pair is the identity.
	later := first.Add(time.Hour)
	if again := cache.SaveReactionRefusal(ctx, "Telegram", "🫶", later); again != nil {
		t.Fatal(again)
	}
	got, err = cache.ReactionRefusals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("%d refusals after re-recording, want the same 3", len(got))
	}
	for _, r := range got {
		if r.Protocol == "Telegram" && r.Emoji == "🫶" && !r.At.Equal(later) {
			t.Errorf("timestamp = %s, want it moved to %s", r.At, later)
		}
	}

	// Blanks are not facts.
	if blank := cache.SaveReactionRefusal(ctx, "", "🫶", first); blank != nil {
		t.Fatal(blank)
	}
	if blank := cache.SaveReactionRefusal(ctx, "Telegram", "", first); blank != nil {
		t.Fatal(blank)
	}
	if got, _ = cache.ReactionRefusals(ctx); len(got) != 3 {
		t.Errorf("%d refusals, want blanks ignored", len(got))
	}
}

// The weights: ten for this room, three for its space, one for anywhere — three
// independent terms, so a use here is worth 14 and a use in a sibling room 4.
func TestEmojiScoresWeighByDistance(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const me = "@me:x"
	const here = domain.RoomID("!here:x")
	sibling := domain.RoomID("!sibling:x")
	far := domain.RoomID("!far:x")

	reacted := []domain.Reaction{
		{ID: "$r1", RoomID: here, Target: "$m1", Sender: me, Key: "🎯"},
		{ID: "$r2", RoomID: sibling, Target: "$m2", Sender: me, Key: "🧭"},
		{ID: "$r3", RoomID: far, Target: "$m3", Sender: me, Key: "🛰️"},
		// Somebody else's reaction is not this account's history.
		{ID: "$r4", RoomID: here, Target: "$m1", Sender: "@them:x", Key: "🐘"},
	}
	if err := cache.SaveReactions(ctx, reacted); err != nil {
		t.Fatal(err)
	}

	scores, err := cache.EmojiScores(ctx, domain.EmojiReaction, here, []domain.RoomID{here, sibling}, me, "room")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		emoji string
		score int
	}{
		{"🎯", roomWeight + spaceWeight + 1}, // here: all three terms
		{"🧭", spaceWeight + 1},              // a sibling room: space and global
		{"🛰️", 1},                           // anywhere else: global only
		{"🐘", 0},                            // theirs, not ours
	} {
		if got := scores[want.emoji]; got != want.score {
			t.Errorf("%s scored %d, want %d", want.emoji, got, want.score)
		}
	}

	// Idempotent: the same page arriving twice must not inflate the score.
	if resave := cache.SaveReactions(ctx, reacted); resave != nil {
		t.Fatal(resave)
	}
	again, againErr := cache.EmojiScores(ctx, domain.EmojiReaction, here, []domain.RoomID{here, sibling}, me, "room")
	if againErr != nil {
		t.Fatal(againErr)
	}
	if again["🎯"] != scores["🎯"] {
		t.Errorf("re-saving the same reactions moved 🎯 from %d to %d", scores["🎯"], again["🎯"])
	}

	// Repeated use compounds, which is the whole mechanism.
	more := make([]domain.Reaction, 0, 4)
	for i := range 4 {
		more = append(more, domain.Reaction{
			ID: domain.EventID("$c" + string(rune('a'+i))), RoomID: here,
			Target: domain.EventID("$m" + string(rune('a'+i))), Sender: me, Key: "🧭",
		})
	}
	if saveMore := cache.SaveReactions(ctx, more); saveMore != nil {
		t.Fatal(saveMore)
	}
	scores, err = cache.EmojiScores(ctx, domain.EmojiReaction, here, []domain.RoomID{here, sibling}, me, "room")
	if err != nil {
		t.Fatal(err)
	}
	if scores["🧭"] <= scores["🎯"] {
		t.Errorf("🧭 scored %d and 🎯 %d — four uses here should overtake one", scores["🧭"], scores["🎯"])
	}

	// Composed emoji are ranked apart, from their tally.
	composed, composedErr := cache.EmojiScores(ctx, domain.EmojiComposed, here, []domain.RoomID{here, sibling}, me, "room")
	if composedErr != nil {
		t.Fatal(composedErr)
	}
	if len(composed) != 0 {
		t.Errorf("composed scores = %v, want none: nothing was composed", composed)
	}
	if record := cache.RecordEmoji(ctx, domain.EmojiComposed, here, "✍️", 1); record != nil {
		t.Fatal(record)
	}
	composed, composedErr = cache.EmojiScores(ctx, domain.EmojiComposed, here, []domain.RoomID{here, sibling}, me, "room")
	if composedErr != nil {
		t.Fatal(composedErr)
	}
	if composed["✍️"] != roomWeight+spaceWeight+1 {
		t.Errorf("composed ✍️ scored %d, want the same weighting as a reaction", composed["✍️"])
	}
	// And the two kinds stay apart: the composed emoji is nowhere in the reaction
	// ranking, however often it is typed.
	if scores["✍️"] != 0 {
		t.Errorf("a composed emoji scored %d in the reaction ranking", scores["✍️"])
	}
}
