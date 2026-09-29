package tui

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The tiers are cumulative.
func TestEmojiTiersAreCumulative(t *testing.T) {
	t.Parallel()

	curatedSet := newEmojiSet(emojiCurated, nil)
	standard := newEmojiSet(emojiStandard, nil)
	complete := newEmojiSet(emojiComplete, nil)

	if len(curatedSet.all) >= len(standard.all) || len(standard.all) >= len(complete.all) {
		t.Errorf("tiers should grow: %d, %d, %d", len(curatedSet.all), len(standard.all), len(complete.all))
	}
	for name, emoji := range curatedSet.byName {
		if standard.byName[name] != emoji {
			t.Errorf("standard lost %q → %s", name, emoji)
		}
	}
	for name, emoji := range standard.byName {
		if complete.byName[name] != emoji {
			t.Errorf("complete lost %q → %s", name, emoji)
		}
	}
	if _, ok := standard.byName["flag_ukraine"]; ok {
		t.Error("standard should hold no flags — they are composed sequences")
	}
	if _, ok := complete.byName["flag_ukraine"]; !ok {
		t.Error("complete should hold the flags")
	}
}

// The curated names are what people type, so no generated name may take one.
func TestCuratedNamesWinCollisions(t *testing.T) {
	t.Parallel()

	complete := newEmojiSet(emojiComplete, nil)
	for name, emoji := range emojiShortcodes {
		if got := complete.byName[name]; got != emoji {
			t.Errorf("%q = %s in the complete set, want the curated %s", name, got, emoji)
		}
	}
	if got := complete.nameOf["👍"]; got != "+1" {
		t.Errorf("nameOf(👍) = %q, want the curated +1", got)
	}
}

// A user's additions win over everything, including redefinitions; blanks are dropped.
func TestExtraEmojiWinAndCanRedefine(t *testing.T) {
	t.Parallel()

	set := newEmojiSet(emojiComplete, map[string]string{
		"tableflip": "(╯°□°）╯︵ ┻━┻",
		"+1":        "🫶",
	})
	if got := set.byName["tableflip"]; got != "(╯°□°）╯︵ ┻━┻" {
		t.Errorf("tableflip = %q, want the addition", got)
	}
	if got := set.byName["+1"]; got != "🫶" {
		t.Errorf("+1 = %q, want the redefinition to win", got)
	}
	blank := newEmojiSet(emojiCurated, map[string]string{"": "🎉", "empty": ""})
	if _, ok := blank.byName[""]; ok {
		t.Error("a nameless entry should be dropped")
	}
	if _, ok := blank.byName["empty"]; ok {
		t.Error("an emoji-less entry should be dropped")
	}
}

// Every emoji in every tier is nameable and listed once.
func TestEverySetIsWellFormed(t *testing.T) {
	t.Parallel()

	for _, tier := range []string{emojiCurated, emojiStandard, emojiComplete} {
		set := newEmojiSet(tier, nil)
		seen := make(map[string]bool, len(set.all))
		for _, emoji := range set.all {
			if seen[emoji] {
				t.Errorf("%s: %s listed twice", tier, emoji)
			}
			seen[emoji] = true
			if set.nameOf[emoji] == "" {
				t.Errorf("%s: %s has no display name", tier, emoji)
			}
		}
		if len(set.names) < len(set.all) {
			t.Errorf("%s: %d names for %d emoji", tier, len(set.names), len(set.all))
		}
	}
}

// An unknown tier is refused at startup, not read as "curated".
func TestEmojiTierValidation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"", emojiCurated},
		{"curated", emojiCurated},
		{" Standard ", emojiStandard},
		{"COMPLETE", emojiComplete},
	} {
		got, err := setup.EmojiTier(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("EmojiTier(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := setup.EmojiTier("expanded"); err == nil {
		t.Error("an unknown tier should be refused")
	}
	err := setup.Validate(config.Config{Display: config.Display{Emoji: config.Emoji{Set: "everything"}}})
	if err == nil || !strings.Contains(err.Error(), "emoji.set") {
		t.Errorf("Validate error = %v, want it to name display.emoji.set", err)
	}
}

// Scored emoji float up; unscored ones keep their input order.
func TestRankEmojiFloatsUsedOnesUp(t *testing.T) {
	t.Parallel()

	alphabetical := []string{"🅰️", "🅱️", "🅾️", "🆎"}
	scores := foldScores(map[string]int{"🆎": 14, "🅱️": 4})
	got := emojiRanker{primary: scores}.sorted(alphabetical)
	if want := []string{"🆎", "🅱️", "🅰️", "🅾️"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if got[2] != "🅰️" || got[3] != "🅾️" {
		t.Errorf("unused emoji lost their order: %v", got)
	}
	if same := (emojiRanker{}).sorted(alphabetical); !reflect.DeepEqual(same, alphabetical) {
		t.Errorf("with no scores the order = %v, want the input untouched", same)
	}
}

// Ranking folds tone, so a toned history lifts the toned form.
func TestRankingIgnoresTone(t *testing.T) {
	t.Parallel()

	toned := "👍" + light
	got := emojiRanker{primary: foldScores(map[string]int{toned: 50})}.sorted([]string{"🎉", toned})
	if got[0] != toned {
		t.Errorf("order = %v, want the toned form lifted by its own history", got)
	}
}

// The quick palette, browser grid and :shortcode: popup agree about what comes first.
func TestOneOrderForEveryWayIn(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, emojiScoresMsg{roomID: "!a:x", kind: domain.EmojiReaction,
		scores: map[string]int{"🔥": 30, "🎉": 20}})

	grid := m.rankedEmoji(domain.EmojiReaction)
	if grid[0] != "🔥" || grid[1] != "🎉" {
		t.Fatalf("grid leads with %v, want 🔥,🎉", grid[:2])
	}
	if m.glyphs.palette[0] != grid[0] || m.glyphs.palette[1] != grid[1] {
		t.Errorf("palette = %v, want the head of the grid order %v", m.glyphs.palette[:2], grid[:2])
	}
	popup := m.emojiCandidatesOf("", domain.EmojiReaction)
	if len(popup) < 2 || popup[0].emoji != grid[0] || popup[1].emoji != grid[1] {
		t.Errorf("popup leads with %v, want the same order", popup[:2])
	}
}

// An empty composed history is informed by the reaction history, which it outranks
// once it exists.
func TestComposingBorrowsTheReactionHistory(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, emojiScoresMsg{roomID: "!a:x", kind: domain.EmojiReaction,
		scores: map[string]int{"🔥": 30}})
	if got := m.rankedEmoji(domain.EmojiComposed); got[0] != "🔥" {
		t.Errorf("composed order leads with %q, want the reaction history to break the tie", got[0])
	}
	m = update(t, m, emojiScoresMsg{roomID: "!a:x", kind: domain.EmojiComposed,
		scores: map[string]int{"🎉": 1}})
	if got := m.rankedEmoji(domain.EmojiComposed); got[0] != "🎉" {
		t.Errorf("composed order leads with %q, want the composed history first", got[0])
	}
	if got := m.rankedEmoji(domain.EmojiReaction); got[0] != "🔥" {
		t.Errorf("reaction order leads with %q, want 🔥", got[0])
	}
}

// Non-emoji usage (failed shortcodes, text reactions) never reaches the palette.
func TestNonEmojiUsageNeverReachesThePalette(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, emojiScoresMsg{roomID: "!a:x", kind: domain.EmojiReaction,
		scores: map[string]int{":dance:": 500, "lol": 400, "🔥": 30}})
	for _, slot := range m.glyphs.palette {
		if slot == ":dance:" || slot == "lol" {
			t.Fatalf("palette = %v, want text kept out of it", m.glyphs.palette)
		}
	}
	if m.glyphs.palette[0] != "🔥" {
		t.Errorf("palette leads with %q, want the real emoji", m.glyphs.palette[0])
	}
	if !looksLikeEmoji("🔥") || looksLikeEmoji(":dance:") || looksLikeEmoji("lol") {
		t.Error("looksLikeEmoji disagrees with itself")
	}
}

// The "nearby" rung is the room's first space by [display] space_priority, and does
// not depend on the rail cursor.
func TestSpaceRungIsTheRoomsPrioritySpace(t *testing.T) {
	t.Parallel()

	m := New(context.Background(), apitest.Nop{}, config.Display{
		SpacePriority: []string{"Friends"},
	})
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!work:x", Name: "A work room"},
		{ID: "!mate:x", Name: "A friend"},
	}})
	m = sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!c:x", Name: "Colleagues", Children: []domain.RoomID{"!a:x", "!work:x"}},
		{ID: "!f:x", Name: "Friends", Children: []domain.RoomID{"!a:x", "!mate:x"}},
	}}))

	got := m.spaceRoomsFor("!a:x")
	if len(got) != 2 || !slices.Contains(got, "!mate:x") {
		t.Errorf("space rung = %v, want the Friends space (%v)", got, []string{"!a:x", "!mate:x"})
	}
	if slices.Contains(got, "!work:x") {
		t.Error("the space rung took the alphabetically first space, not the configured one")
	}
	m.rail.cursor = len(m.rail.groups) - 1
	if again := m.spaceRoomsFor("!a:x"); !slices.Equal(again, got) {
		t.Errorf("the rung moved with the rail cursor: %v then %v", got, again)
	}
	if orphan := m.spaceRoomsFor("!nowhere:x"); len(orphan) != 0 {
		t.Errorf("a room in no space got %v", orphan)
	}
}
