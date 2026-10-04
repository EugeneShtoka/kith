package tui

import (
	"context"
	"strings"
	"testing"

	emojidata "github.com/EugeneShtoka/kith/internal/emoji"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// emojiBackend answers with a fixed composed-emoji ranking and records the picks
// reported back to it.
type emojiBackend struct {
	apitest.Nop
	frequent []string
	recorded []string
	kinds    []domain.EmojiKind
	// reactKey and reactTarget record a reaction sent through the grid.
	reactKey    string
	reactTarget domain.EventID
}

// EmojiScores turns the fixture's "frequent" list into a ranking, most-used first.
func (b *emojiBackend) EmojiScores(context.Context, domain.EmojiKind, domain.RoomID, []domain.RoomID, string) (map[string]int, error) {
	scores := make(map[string]int, len(b.frequent))
	for i, e := range b.frequent {
		scores[e] = (len(b.frequent) - i) * 10
	}
	return scores, nil
}

func (b *emojiBackend) SendReaction(_ context.Context, _ domain.RoomID, target domain.EventID, key string) error {
	b.reactKey, b.reactTarget = key, target
	return nil
}

func (b *emojiBackend) RecordEmoji(_ context.Context, kind domain.EmojiKind, _ domain.RoomID, emoji string) error {
	b.kinds = append(b.kinds, kind)
	b.recorded = append(b.recorded, emoji)
	return nil
}

// pickedEmoji is the emoji the open picker is offering, in order.
func pickedEmoji(m Model) []string {
	out := make([]string, 0, len(m.picker.items))
	for _, item := range m.picker.items {
		out = append(out, item.value)
	}
	return out
}

// composingEmoji returns a model in insert mode with a composed-emoji ranking.
func composingEmoji(t *testing.T, frequent ...string) (Model, *emojiBackend) {
	t.Helper()
	b := &emojiBackend{frequent: frequent}
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next

	m.focus, m.compose.insertMode = paneTimeline, true
	m = m.clearStatus()
	return m, b
}

// withEmojiScores delivers the room's emoji ranking, as selectRoom's command would.
func withEmojiScores(t *testing.T, m Model) Model {
	t.Helper()
	for _, kind := range []domain.EmojiKind{domain.EmojiReaction, domain.EmojiComposed} {
		scores, err := m.backend.EmojiScores(context.Background(), kind, m.openRoom, nil, m.glyphs.scope)
		if err != nil {
			t.Fatalf("EmojiScores: %v", err)
		}
		m = update(t, m, emojiScoresMsg{roomID: m.openRoom, kind: kind, scores: scores})
	}
	return m
}

// typeAll types text and runs whatever commands the keystrokes produce.
func typeAll(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		next, cmd := press(t, m, keyText(string(r)))
		m = next
		if cmd != nil {
			runCmd(t, cmd)
		}
	}
	return m
}

// The curated shortcode map is internally consistent.
func TestEmojiMapIsConsistent(t *testing.T) {
	t.Parallel()

	if len(emojidata.Curated) < 300 {
		t.Errorf("only %d shortcodes", len(emojidata.Curated))
	}
	for name, emoji := range emojidata.Curated {
		if name == "" || emoji == "" {
			t.Errorf("blank entry: %q → %q", name, emoji)
		}
		if strings.ContainsAny(name, " :") {
			t.Errorf("shortcode %q must not contain a space or a colon", name)
		}
		if strings.ToLower(name) != name {
			t.Errorf("shortcode %q must be lowercase — lookups fold to lower", name)
		}
		if curated.nameOf[emoji] == "" {
			t.Errorf("%q has no display shortcode", emoji)
		}
	}
	seen := map[string]bool{}
	for _, emoji := range curated.all {
		if seen[emoji] {
			t.Errorf("%q appears twice in the browse list", emoji)
		}
		seen[emoji] = true
	}
	for _, emoji := range emojidata.Curated {
		if !seen[emoji] {
			t.Errorf("%q is in the map but not in the browse list", emoji)
		}
	}
}

// ":" opens the popup, with candidates labeled by shortcode.
func TestEmojiCompletionOpens(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t)
	m = typeAll(t, m, "ship it :roc")
	if !m.completion.active || m.completion.trigger != ":" {
		t.Fatalf("':' should open the emoji popup, got active=%v trigger=%q", m.completion.active, m.completion.trigger)
	}
	first := m.completion.candidates[0]
	if first.text != "🚀" {
		t.Errorf("first candidate inserts %q, want 🚀", first.text)
	}
	if !strings.Contains(first.label, ":rocket:") {
		t.Errorf("label = %q, should name the shortcode", first.label)
	}
}

// Prefix matches come before substring ones.
func TestEmojiCompletionOrdersPrefixFirst(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t)
	m = typeAll(t, m, ":heart")
	if len(m.completion.candidates) == 0 {
		t.Fatal("no candidates for :heart")
	}
	if m.completion.candidates[0].text != "❤️" {
		t.Errorf("first = %q, want the exact prefix match ❤️", m.completion.candidates[0].text)
	}
	labels := []string{}
	for _, c := range m.completion.candidates {
		labels = append(labels, c.label)
	}
	joined := strings.Join(labels, " ")
	prefixAt := strings.Index(joined, ":heart:")
	substrAt := strings.Index(joined, ":broken_heart:")
	if prefixAt < 0 || substrAt < 0 || prefixAt > substrAt {
		t.Errorf("prefix matches should precede substring ones, got %v", labels)
	}
}

// Accepting inserts the emoji with no trailing space, and records the pick.
func TestEmojiAcceptInsertsBare(t *testing.T) {
	t.Parallel()

	m, b := composingEmoji(t)
	m = typeAll(t, m, "ship it :roc")
	next, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = next
	if cmd != nil {
		runCmd(t, cmd)
	}
	if m.compose.input != "ship it 🚀" {
		t.Errorf("input = %q, want no trailing space", m.compose.input)
	}
	if m.completion.active {
		t.Error("accepting should close the popup")
	}
	if len(b.recorded) != 1 || b.recorded[0] != "🚀" {
		t.Errorf("recorded = %v, want the accepted emoji", b.recorded)
	}
	if len(b.kinds) != 1 || b.kinds[0] != domain.EmojiComposed {
		t.Errorf("recorded kinds = %v, want the composed vocabulary", b.kinds)
	}
}

// Typing the closing colon accepts.
func TestEmojiClosingColonAccepts(t *testing.T) {
	t.Parallel()

	m, b := composingEmoji(t)
	m = typeAll(t, m, "nice :tada:")
	if m.compose.input != "nice 🎉" {
		t.Errorf("input = %q, want the emoji substituted", m.compose.input)
	}
	if m.completion.active {
		t.Error("the popup should be closed after a completed shortcode")
	}
	if len(b.recorded) != 1 || b.recorded[0] != "🎉" {
		t.Errorf("recorded = %v — a hand-typed shortcode should feed the ranking too", b.recorded)
	}
}

// A colon pair that isn't a shortcode survives exactly as typed.
func TestEmojiUnknownShortcodeLeftAlone(t *testing.T) {
	t.Parallel()

	tests := []string{"meet at 10:30:", "ratio 3:1:", ":notanemoji:"}
	for _, text := range tests {
		m, b := composingEmoji(t)
		m = typeAll(t, m, text)
		if m.compose.input != text {
			t.Errorf("typing %q gave %q — an unknown shortcode must not be mangled", text, m.compose.input)
		}
		if len(b.recorded) != 0 {
			t.Errorf("typing %q recorded %v", text, b.recorded)
		}
	}
}

// The browser leads with frequent emoji, without duplicating them.
func TestEmojiBrowserLeadsWithFrequent(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t, "🚀", "🐛", "☕")
	m = withEmojiScores(t, m)
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if !m.picker.active() {
		t.Fatal("ctrl+e should open the browser")
	}
	got := pickedEmoji(m)
	if len(got) < 3 || got[0] != "🚀" || got[1] != "🐛" || got[2] != "☕" {
		t.Fatalf("browser starts %v, want the frequent emoji first", got[:min(3, len(got))])
	}
	if len(got) < len(curated.all) {
		t.Errorf("browser shows %d emoji, want at least the whole map (%d)", len(got), len(curated.all))
	}
	seen := map[string]bool{}
	for _, e := range got {
		if seen[e] {
			t.Fatalf("%q appears twice in the browser", e)
		}
		seen[e] = true
	}
}

// Typing filters the grid, backspace widens it again.
func TestEmojiBrowserFilters(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t)
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	all := len(pickedEmoji(m))

	m, _ = press(t, m, keyText("c"))
	m, _ = press(t, m, keyText("o"))
	m, _ = press(t, m, keyText("f"))
	if m.picker.filter != "cof" {
		t.Errorf("filter = %q", m.picker.filter)
	}
	if got := pickedEmoji(m); len(got) != 1 || got[0] != "☕" {
		t.Errorf("filtered to %v, want just ☕", got)
	}
	m, _ = press(t, m, keyText("z"))
	if got := pickedEmoji(m); len(got) != 0 {
		t.Errorf("filter %q matched %v", m.picker.filter, got)
	}
	if !strings.Contains(stripStyles(m.View().Content), "nothing matches") {
		t.Error("a filter matching nothing should say so")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(m.picker.items) != 1 {
		t.Error("backspace should widen the filter again")
	}
	for range 3 {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if len(m.picker.items) != all {
		t.Errorf("clearing the filter gave %d emoji, want the original %d", len(m.picker.items), all)
	}
}

// The grid walks in two dimensions and clamps at both ends; letters filter.
func TestEmojiBrowserNavigation(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t)
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	perRow := m.pickerColumns()
	if perRow < 2 {
		t.Fatalf("paletteColumns = %d, too narrow to test", perRow)
	}

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.picker.cursor != 1 {
		t.Errorf("right: cursor = %d, want 1", m.picker.cursor)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.picker.cursor != 1+perRow {
		t.Errorf("down: cursor = %d, want %d", m.picker.cursor, 1+perRow)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.picker.cursor != 1 {
		t.Errorf("up: cursor = %d, want 1", m.picker.cursor)
	}
	for range 5 {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if m.picker.cursor != 0 {
		t.Errorf("up past the start = %d, want 0", m.picker.cursor)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if want := len(m.picker.items) - 1; m.picker.cursor != want {
		t.Errorf("end = %d, want %d", m.picker.cursor, want)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if want := len(m.picker.items) - 1; m.picker.cursor != want {
		t.Errorf("down past the end = %d, want it clamped to %d", m.picker.cursor, want)
	}
	m, _ = press(t, m, keyText("j"))
	if m.picker.filter != "j" {
		t.Errorf("a letter should filter, got filter %q", m.picker.filter)
	}
}

// Accepting from the browser inserts and records the pick; esc changes nothing.
func TestEmojiBrowserAcceptAndCancel(t *testing.T) {
	t.Parallel()

	m, b := composingEmoji(t, "🚀")
	m = withEmojiScores(t, m)
	m = typeAll(t, m, "ship ")
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	next, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	next = deliver(t, next, cmd)
	if next.compose.input != "ship 🚀" {
		t.Errorf("input = %q", next.compose.input)
	}
	if next.picker.active() {
		t.Error("accepting should close the browser")
	}
	if len(b.recorded) != 1 || b.recorded[0] != "🚀" {
		t.Errorf("recorded = %v", b.recorded)
	}

	m2, _ := composingEmoji(t, "🚀")
	m2 = typeAll(t, m2, "ship ")
	m2, _ = press(t, m2, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m2, _ = press(t, m2, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m2.picker.active() {
		t.Error("esc should close the browser")
	}
	if m2.compose.input != "ship " {
		t.Errorf("input = %q, want it untouched", m2.compose.input)
	}
}

// From the message cursor the browser key opens the grid to react instead of type.
func TestEmojiBrowserFromMessageCursorReacts(t *testing.T) {
	t.Parallel()

	m, b := composingEmoji(t, "🚀")
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "hi", Timestamp: at(1)},
	}}})
	m.compose.insertMode = false

	m, _ = press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if m.picker.kind != pickerReaction {
		t.Fatalf("picker = %v, want the reaction grid", m.picker.kind)
	}
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("accepting should send the reaction")
	}
	m = deliver(t, m, cmd)
	if b.reactTarget != "$1" || b.reactKey == "" {
		t.Errorf("reacted key=%q target=%q, want something on $1", b.reactKey, b.reactTarget)
	}
	if m.compose.input != "" || m.compose.insertMode {
		t.Errorf("input = %q insert = %t, want the composer untouched", m.compose.input, m.compose.insertMode)
	}
}

// With nothing selected the reaction grid does not open.
func TestReactionGridNeedsAMessage(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t, "🚀")
	m.compose.insertMode = false
	m = m.setMessages(nil)
	if next, _ := press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}); next.picker.active() {
		t.Error("no message, no reaction grid")
	}
}

// The browser takes the keyboard: letters filter rather than type.
func TestEmojiBrowserCapturesKeys(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t)
	m = typeAll(t, m, "hello")
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m, _ = press(t, m, keyText("c"))
	if m.compose.input != "hello" {
		t.Errorf("input = %q — the browser should absorb the keystroke", m.compose.input)
	}
	if m.picker.filter != "c" {
		t.Errorf("filter = %q", m.picker.filter)
	}
}

// Composed and reaction rankings are separate, and kept per room.
func TestComposeAndReactionRankingsAreSeparate(t *testing.T) {
	t.Parallel()

	b := &emojiBackend{frequent: []string{"🚀"}}
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, cmd := m.selectRoom(m.filteredRooms()[0])
	m = next
	if cmd == nil {
		t.Fatal("opening a room should load both rankings")
	}
	m = update(t, m, emojiScoresMsg{roomID: "!a:x", kind: domain.EmojiComposed, scores: map[string]int{"🚀": 40}})
	m = update(t, m, emojiScoresMsg{roomID: "!a:x", kind: domain.EmojiReaction, scores: map[string]int{"🎯": 40}})
	if got := m.rankedEmoji(domain.EmojiComposed); got[0] != "🚀" {
		t.Errorf("composed ranking starts %q, want 🚀", got[0])
	}
	if got := m.rankedEmoji(domain.EmojiReaction); got[0] != "🎯" {
		t.Errorf("reaction ranking starts %q, want 🎯", got[0])
	}
	m = update(t, m, emojiScoresMsg{roomID: "!other:x", kind: domain.EmojiComposed, scores: map[string]int{"🐛": 99}})
	if got := m.rankedEmoji(domain.EmojiComposed); got[0] != "🚀" {
		t.Errorf("another room's ranking leaked in: starts %q", got[0])
	}
	if len(m.glyphs.orders["!other:x"].composed) == 0 {
		t.Error("the other room's ranking should have been kept for when it is opened")
	}
}

// A mention and an emoji share one popup.
func TestBothCompletionSourcesInOneMessage(t *testing.T) {
	t.Parallel()

	b := &memberBackend{members: []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}}}
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, membersMsg{roomID: "!a:x", members: b.members})
	m.focus, m.compose.insertMode = paneTimeline, true

	m = typeAll(t, m, "@dan")
	if m.completion.trigger != "@" {
		t.Fatalf("trigger = %q, want @", m.completion.trigger)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = typeAll(t, m, ":tada:")
	if m.compose.input != "Dana 🎉" {
		t.Errorf("input = %q, want both sources to have worked", m.compose.input)
	}
	if len(m.compose.drafted) != 1 || m.compose.drafted[0].UserID != "@dana:x" {
		t.Errorf("the mention should survive an emoji completion, got %+v", m.compose.drafted)
	}
}

// reacting opens the react prompt on a message.
func reacting(t *testing.T, frequent ...string) (Model, *emojiBackend) {
	t.Helper()
	m, b := composingEmoji(t, frequent...)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", SenderName: "Alice", Body: "hello"},
	}}})
	m.compose.insertMode = false
	m, _ = press(t, m, keyText("e"))
	if !m.compose.reacting {
		t.Fatal("e should open the react prompt")
	}
	return m, b
}

// The react prompt completes :shortcodes: into the reaction, not the composer.
func TestReactPromptCompletesShortcodes(t *testing.T) {
	t.Parallel()

	m, _ := reacting(t)
	m = typeAll(t, m, ":tad")
	if !m.completion.active {
		t.Fatal("':' in the react prompt should open the popup")
	}
	if m.completion.target != targetReaction {
		t.Errorf("target = %v, want the reaction input", m.completion.target)
	}
	if len(m.completion.candidates) == 0 || m.completion.candidates[0].text != "🎉" {
		t.Fatalf("candidates = %+v", m.completion.candidates)
	}
	next, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = next
	if cmd != nil {
		runCmd(t, cmd)
	}
	if m.compose.reactInput != "🎉" {
		t.Errorf("reactInput = %q, want the emoji", m.compose.reactInput)
	}
	if m.compose.input != "" {
		t.Errorf("input = %q — the composer must not be touched", m.compose.input)
	}
}

// Typing the closing colon works in the react prompt too.
func TestReactPromptClosingColon(t *testing.T) {
	t.Parallel()

	m, b := reacting(t)
	m = typeAll(t, m, ":fire:")
	if m.compose.reactInput != "🔥" {
		t.Errorf("reactInput = %q, want the substituted emoji", m.compose.reactInput)
	}
	if m.completion.active {
		t.Error("the popup should close once the shortcode is complete")
	}
	if len(b.recorded) != 1 || b.recorded[0] != "🔥" {
		t.Errorf("recorded = %v — a react-prompt pick should feed the ranking too", b.recorded)
	}
}

// The react prompt does not complete mentions.
func TestReactPromptDoesNotCompleteMentions(t *testing.T) {
	t.Parallel()

	m, _ := reacting(t)
	m = typeAll(t, m, "@dan")
	if m.completion.active {
		t.Error("'@' should not complete in the react prompt")
	}
	if m.compose.reactInput != "@dan" {
		t.Errorf("reactInput = %q — the text must still be typed", m.compose.reactInput)
	}
}

// While the popup is open the first esc/enter acts on it, the second on the prompt.
func TestReactPromptPopupOwnsEscAndEnter(t *testing.T) {
	t.Parallel()

	// esc: dismiss the popup, then leave the prompt.
	m, _ := reacting(t)
	m = typeAll(t, m, ":tad")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.completion.active {
		t.Error("the first esc should dismiss the popup")
	}
	if !m.compose.reacting {
		t.Error("the first esc should not also leave the prompt")
	}
	if m.compose.reactInput != ":tad" {
		t.Errorf("reactInput = %q — dismissing leaves the text as typed", m.compose.reactInput)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.compose.reacting || m.compose.reactInput != "" {
		t.Error("the second esc should leave the prompt and clear it")
	}

	// enter: accept the candidate, then send it.
	m2, _ := reacting(t)
	m2 = typeAll(t, m2, ":tad")
	m2, _ = press(t, m2, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m2.completion.active {
		t.Error("the first enter should accept the candidate")
	}
	if m2.compose.reactInput != "🎉" {
		t.Errorf("reactInput = %q, want the accepted emoji", m2.compose.reactInput)
	}
	if !m2.compose.reacting {
		t.Error("accepting should not also send")
	}
	m2, cmd := press(t, m2, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("the second enter should send")
	}
	if m2.compose.reacting || m2.completion.active {
		t.Error("sending should close both")
	}
}

// Sending leaves no popup state behind.
func TestReactPromptLeavesNoPopupBehind(t *testing.T) {
	t.Parallel()

	m, _ := reacting(t)
	m = typeAll(t, m, ":tada:") // completes and closes the popup itself
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.compose.reacting || m.completion.active {
		t.Errorf("after sending: reacting=%v popup=%v, want both closed", m.compose.reacting, m.completion.active)
	}
}

// Digits pick from the quick palette only while nothing has been typed.
func TestReactPromptDigitsStillPick(t *testing.T) {
	t.Parallel()

	m, _ := reacting(t)
	m.glyphs.palette = []string{"👍", "❤️"}
	next, cmd := press(t, m, keyText("1"))
	if cmd == nil {
		t.Fatal("a digit should still send from the palette")
	}
	if next.compose.reacting {
		t.Error("picking from the palette should close the prompt")
	}
	m2, _ := reacting(t)
	m2.glyphs.palette = []string{"👍"}
	m2 = typeAll(t, m2, ":100")
	if m2.compose.reactInput != ":100" {
		t.Errorf("reactInput = %q, want the digits typed into the shortcode", m2.compose.reactInput)
	}
	if !m2.completion.active {
		t.Error("the popup should be open for :100")
	}
}
