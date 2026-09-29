package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The shipped defaults must be internally consistent — no conflicts, nothing
// unbound. If this fails, the default config contradicts itself.
func TestDefaultKeymapIsClean(t *testing.T) {
	t.Parallel()

	km := newKeymap(config.DefaultKeys())
	if len(km.issues) > 0 {
		t.Errorf("default keys produced issues: %v", km.issues)
	}
	defaults := config.DefaultKeys()
	for _, row := range keyActions {
		if row.keys(defaults) == unbind {
			continue // no key by default, on purpose (interrupt)
		}
		if len(km.keysFor(row.scope, row.act)) == 0 {
			t.Errorf("%s has no key by default", row.name)
		}
	}
}

// Rebinding an action in config changes what the key does, with no code change —
// and the help overlay shows the new key without being edited.
func TestRebindingTakesEffect(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Timeline.Reply = "R"

	m := withMessage(t, openTimeline(t, sized(t, withRooms(t, newModel())).WithKeys(keys)))

	// The old key no longer replies…
	m = update(t, m, keyText("\r"))
	if m.compose.replyTo != "" {
		t.Error("enter should no longer start a reply once reply is rebound")
	}
	// …and the new one does.
	m = update(t, m, keyCode('R'))
	if m.compose.replyTo == "" {
		t.Error("R should start a reply after rebinding")
	}
	// Help is generated, so it names the new key without anyone editing it.
	if got := strings.Join(m.keys.keysFor(scopeTimeline, actReply), ","); got != "R" {
		t.Errorf("help would show reply as %q, want R", got)
	}
}

// Fields a config leaves empty take their defaults.
func TestPartialConfigStillGetsDefaults(t *testing.T) {
	t.Parallel()

	partial := config.Keys{Timeline: config.TimelineKeys{React: "e"}}
	partial.FillDefaults()
	km := newKeymap(partial)

	if got := km.keysFor(scopeTimeline, actReact); len(got) != 1 || got[0] != "e" {
		t.Errorf("react = %v, want [e]", got)
	}
	for _, row := range keyActions {
		if row.keys(config.DefaultKeys()) == unbind {
			continue // no key by default, on purpose
		}
		if len(km.keysFor(row.scope, row.act)) == 0 {
			t.Errorf("%s lost its default in a partial config", row.name)
		}
	}
}

// The keys that move, go back and cancel are ordinary defaults: a config can give
// them to something else (the user's rule: no key is hard-coded).
func TestMovingAndCancelingKeysAreRebindable(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Nav.Back = "h"
	keys.Nav.Open = "esc"
	keys.Nav.Up = "k"
	keys.Nav.PageDown = "pgdown,up"
	keys.Insert.Cancel = "ctrl+g"
	keys.Insert.Send = "esc"
	km := newKeymap(keys)
	for _, c := range []struct {
		key   string
		scope scope
		want  action
	}{
		{"esc", scopeNav, actOpen},
		{"up", scopeNav, actPageDown},
		{"esc", scopeInsert, actSend},
		{"ctrl+g", scopeInsert, actCancel},
	} {
		if got := km.lookup(c.key, c.scope); got != c.want {
			t.Errorf("%s = %v, want %v", c.key, got, c.want)
		}
	}
	if len(km.issues) != 0 {
		t.Errorf("a keymap that only moves keys around reported %v", km.issues)
	}
}

// No keymap can lock you out: an essential action left with no key gets its default
// back, and the issue says so.
func TestEssentialActionsAlwaysKeepAKey(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Nav.Back = unbind
	keys.Insert.Cancel = unbind
	keys.Prompt.Cancel = unbind
	km := newKeymap(keys)
	for _, c := range []struct {
		key   string
		scope scope
		want  action
	}{
		{"esc", scopeNav, actBack},
		{"esc", scopeInsert, actCancel},
		{"esc", scopePrompt, actCancel},
	} {
		if got := km.lookup(c.key, c.scope); got != c.want {
			t.Errorf("%s = %v, want %v", c.key, got, c.want)
		}
	}
	for _, name := range []string{"nav.back", "insert.cancel", "prompt.cancel"} {
		if !slices.ContainsFunc(km.issues, func(i string) bool { return strings.HasPrefix(i, name+": must keep a key") }) {
			t.Errorf("no issue says %s kept its key; issues %v", name, km.issues)
		}
	}
	// A default another action took is taken back from it, and that action falls back
	// to its own default: composing always has a way out.
	keys = config.DefaultKeys()
	keys.Insert.Send = "esc"
	km = newKeymap(keys)
	if km.lookup("esc", scopeInsert) != actCancel || km.lookup("ctrl+enter", scopeInsert) != actSend {
		t.Errorf("esc = %v, ctrl+enter = %v: want cancel kept and send back on its default",
			km.lookup("esc", scopeInsert), km.lookup("ctrl+enter", scopeInsert))
	}
	// An action that is not essential may be left with nothing.
	keys = config.DefaultKeys()
	keys.Timeline.React = unbind
	if km := newKeymap(keys); len(km.keysFor(scopeTimeline, actReact)) != 0 || len(km.issues) != 0 {
		t.Errorf("unbinding timeline.react: keys %v, issues %v", km.keysFor(scopeTimeline, actReact), km.issues)
	}
}

// A duplicate within one scope is reported and the loser falls back to its default.
func TestConflictFallsBackToDefault(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Timeline.React = "enter" // already go_reply's key
	km := newKeymap(keys)

	if got := km.lookup("enter", scopeTimeline); got != actGoReply {
		t.Errorf("enter = %v, want go_reply (first claim wins)", got)
	}
	if got := km.lookup("e", scopeTimeline); got != actReact {
		t.Errorf("e = %v, want react (its default)", got)
	}
	if len(km.issues) != 2 { // the clash, and the fallback that resolved it
		t.Errorf("issues = %v, want the clash and the fallback", km.issues)
	}
	for _, want := range []string{"already bound to timeline.go_reply", "fell back"} {
		if !strings.Contains(strings.Join(km.issues, " "), want) {
			t.Errorf("issues %v should mention %q", km.issues, want)
		}
	}
}

// An action set to "-" is deliberately given no key, which "" cannot mean (TOML
// leaves omitted fields empty, and those take their default).
func TestUnbindSentinel(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Timeline.React = unbind
	km := newKeymap(keys)

	if got := km.keysFor(scopeTimeline, actReact); len(got) != 0 {
		t.Errorf("react = %v, want no keys", got)
	}
	// "e" is react's key; unbound, nothing answers it. (Not "r" — that is reply.)
	if got := km.lookup("e", scopeTimeline); got != actNone {
		t.Errorf("e = %v, want nothing", got)
	}
	if len(km.issues) > 0 {
		t.Errorf("a deliberate unbind is not an issue: %v", km.issues)
	}
}

// A more specific scope wins: the timeline's go_reply binding beats nav's identical
// open binding, which is what lets enter mean two different things.
func TestScopePrecedence(t *testing.T) {
	t.Parallel()

	km := newKeymap(config.DefaultKeys())
	if got := km.lookup("enter", scopeTimeline, scopeNav); got != actGoReply {
		t.Errorf("enter with timeline first = %v, want go_reply", got)
	}
	if got := km.lookup("enter", scopeNav); got != actOpen {
		t.Errorf("enter in nav = %v, want open", got)
	}
	if got := km.lookup("zzz", scopeNav, scopeCommand); got != actNone {
		t.Errorf("an unbound key = %v, want none", got)
	}
}

// While composing, a key that types something types it — no binding can make a
// letter unreachable in the composer.
func TestBindingsNeverSwallowTypedText(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Timeline.Insert = "x"
	keys.Nav.PageUp = "u" // a printable key bound to a motion
	m := sized(t, withRooms(t, newModel())).WithKeys(keys)
	m = openTimeline(t, m)
	m = update(t, m, keyCode('x')) // enter insert mode
	if !m.compose.insertMode {
		t.Fatal("x should enter insert mode after rebinding")
	}
	for _, r := range "quux" { // q would quit, u pages, x composes — in normal mode
		m = update(t, m, keyText(string(r)))
	}
	if m.compose.input != "quux" {
		t.Errorf("input = %q, want %q — bindings must not swallow typed text", m.compose.input, "quux")
	}
}

// A bound interrupt quits from every mode, even with quit unbound.
func TestABoundInterruptQuitsEverywhere(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Quit = unbind // even with quit deliberately unbound
	keys.Interrupt = "ctrl+c"
	base := sized(t, withRooms(t, newModel())).WithKeys(keys)

	for name, m := range map[string]Model{
		"rail":     base,
		"timeline": openTimeline(t, base),
		"help":     withHelp(t, base),
	} {
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if cmd == nil {
			t.Errorf("ctrl+c in %s produced no command, want quit", name)
		}
	}
}

// The ? overlay is generated from the keymap, so it lists every action and cannot
// drift from what the keys do.
func TestHelpOverlayListsEveryAction(t *testing.T) {
	t.Parallel()

	m := withHelp(t, sized(t, withRooms(t, newModel())))
	body := strings.Join(m.helpLines(), "\n")
	for _, row := range keyActions {
		if !strings.Contains(body, row.label()) {
			t.Errorf("help is missing %q (%s)", row.label(), row.name)
		}
	}
}

// Help scrolls with the bindings it documents, and clamps at both ends.
func TestHelpScrolls(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.height = 20 // short enough that the list doesn't fit
	m = withHelp(t, m)

	if m.reader.scroll != 0 {
		t.Fatalf("help should open at the top, got %d", m.reader.scroll)
	}
	m = update(t, m, keyCode('k')) // up at the top stays at the top
	if m.reader.scroll != 0 {
		t.Errorf("scrolling up at the top = %d, want 0", m.reader.scroll)
	}
	m = update(t, m, keyCode('j'))
	if m.reader.scroll != 1 {
		t.Errorf("scroll after one down = %d, want 1", m.reader.scroll)
	}
	for range 50 { // page well past the end
		m = update(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if want := len(m.helpLines()) - m.helpRows(); m.reader.scroll != want {
		t.Errorf("scroll clamped to %d, want %d", m.reader.scroll, want)
	}
}

// Opening and closing help, with the keys the overlay itself advertises.
func TestHelpOpensAndCloses(t *testing.T) {
	t.Parallel()

	for _, closeKey := range []tea.KeyPressMsg{
		keyText("?"), keyText("q"), {Code: tea.KeyEscape},
	} {
		m := withHelp(t, sized(t, withRooms(t, newModel())))
		m = update(t, m, closeKey)
		if m.reader.showing(readerHelp) {
			t.Errorf("%q should close help", closeKey.String())
		}
		if m.reader.scroll != 0 {
			t.Errorf("closing help should reset its scroll, got %d", m.reader.scroll)
		}
	}
}

// The status legend reads its key names out of the keymap, so a rebinding shows up
// there too.
func TestHintsFollowBindings(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Timeline.React = "e"
	m := openTimeline(t, sized(t, withRooms(t, newModel())).WithKeys(keys))

	hints := m.hints()
	if !strings.Contains(hints, "e: react") {
		t.Errorf("hints = %q, should show the rebound react key", hints)
	}
	if strings.Contains(hints, "r: react") {
		t.Errorf("hints = %q, should not still show the old key", hints)
	}
	// An unbound action is not advertised at all.
	keys.Timeline.React = unbind
	m2 := openTimeline(t, sized(t, withRooms(t, newModel())).WithKeys(keys))
	if strings.Contains(m2.hints(), "react") {
		t.Errorf("hints = %q, should not name an unbound action", m2.hints())
	}
}

// A keymap with problems says so on the status line, pointing at the help overlay.
func TestKeymapIssuesSurfaceOnStatus(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Nav.Open = "esc"
	m := sized(t, withRooms(t, newModel())).WithKeys(keys)
	if !strings.Contains(m.status(), "keybinding issue") || !strings.Contains(m.status(), "?") {
		t.Errorf("status = %q, want a keybinding-issue notice naming the help key", m.status())
	}
	// And the overlay itself lists them.
	body := strings.Join(withHelp(t, m).helpLines(), "\n")
	if !strings.Contains(body, "already bound to nav.open") {
		t.Errorf("help should list the issue, got:\n%s", body)
	}
}

func TestValidKey(t *testing.T) {
	t.Parallel()

	good := []string{
		"a", "Z", "?", "1", "·", // any single character
		"enter", "esc", "tab", "space", "backspace", "up", "pgdown", "home", "end",
		"f1", "f9", "f20",
		"ctrl+c", "shift+tab", "alt+enter", "ctrl+alt+delete", "super+k", "meta+x",
	}
	for _, key := range good {
		if !validKey(key) {
			t.Errorf("validKey(%q) = false, want true", key)
		}
	}
	bad := []string{
		"kk", "escape", "pageup", "pgdn", "return", "ctrl", "ctrl+", "f0", "f21",
		"control+c", "cmd+k", "arrowup", "",
	}
	for _, key := range bad {
		if validKey(key) {
			t.Errorf("validKey(%q) = true, want false", key)
		}
	}
}

// A key name the terminal never reports would be a silently dead binding, so it is
// reported and the action falls back to its default.
func TestUnknownKeyNameIsReported(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Timeline.React = "escape" // the real name is "esc"
	km := newKeymap(keys)

	if !strings.Contains(strings.Join(km.issues, " "), `"escape" is not a key name`) {
		t.Errorf("issues = %v, should name the bad key", km.issues)
	}
	if got := km.lookup("e", scopeTimeline); got != actReact {
		t.Error("react should have fallen back to its default after a bad key name")
	}
	// A valid name alongside a bad one still binds.
	keys.Timeline.React = "escape,e"
	km = newKeymap(keys)
	if got := km.lookup("e", scopeTimeline); got != actReact {
		t.Error("the valid half of the list should still bind")
	}
}

func TestSplitKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want []string
	}{
		{"k,up", []string{"k", "up"}},
		{" k , up ", []string{"k", "up"}}, // whitespace around each name is trimmed
		{"k,,up,", []string{"k", "up"}},   // blanks and a trailing comma are dropped
		{"enter", []string{"enter"}},
		{"", nil},
		{",", nil},
		{"shift+tab", []string{"shift+tab"}},
		{"comma,gg", []string{",", "g g"}},
	}
	for _, tc := range tests {
		if got := splitKeys(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("splitKeys(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// openTimeline opens the first room and steps back from the composer to the message
// cursor.
func openTimeline(t *testing.T, m Model) Model {
	t.Helper()
	m = update(t, m, keyCode('l')) // rail → rooms
	m = update(t, m, keyCode('l')) // rooms → timeline (in the composer)
	return update(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
}

// withMessage lands one message in the open room, so the message cursor has
// something to select and the actions that need a selection can run.
func withMessage(t *testing.T, m Model) Model {
	t.Helper()
	return update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "hi"}},
	}})
}

// withHelp opens the help overlay.
func withHelp(t *testing.T, m Model) Model {
	t.Helper()
	m = update(t, m, keyText("?"))
	if !m.reader.showing(readerHelp) {
		t.Fatal("? should open the help overlay")
	}
	return m
}

// Key sequences: "gg" is two presses, and the first of them waits rather than acting.
func TestKeySequences(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	km := newKeymap(keys)

	if got := km.lookup("g g", scopeNav); got != actSelectOldest {
		t.Errorf("lookup(\"g g\") = %v, want select_oldest — the default is a sequence", got)
	}
	// A prefix waits only in the scopes where its sequence is bound.
	if !km.incomplete("g", scopeNav) {
		t.Error(`"g" should be incomplete in scopeNav: it can still become "g g"`)
	}
	if km.incomplete("g g", scopeNav) {
		t.Error("a complete binding is not incomplete")
	}
	if km.incomplete("s", scopeTimeline) {
		t.Error(`"s" must not wait in the timeline: the sort chords belong to the room list`)
	}
	if !km.incomplete("s", scopeRooms) {
		t.Error(`"s" should wait in the room list, where "s u" and friends are bound`)
	}
	if km.lookup("g", scopeNav) != actNone {
		t.Error(`"g" alone should bind nothing once the binding is "gg"`)
	}
	// Written either way, and read back the way vim writes it.
	spaced := newKeymap(withNav(keys, "g g"))
	if got := spaced.lookup("g g", scopeNav); got != actSelectOldest {
		t.Errorf(`the spaced spelling should bind the same sequence, got %v`, got)
	}
	if got := km.keysFor(scopeNav, actSelectOldest); len(got) != 1 || got[0] != "gg" {
		t.Errorf("keysFor = %v, want [gg] — sequences read back as they are written", got)
	}
}

// A three-step chord needs the spaced spelling, and a mistyped key name must stay a
// reported mistake rather than quietly becoming one.
func TestSequenceShorthandIsNarrow(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"gg":       "g g",
		"esc":      "esc",
		"pgup":     "pgup",
		"escape":   "escape", // left for validation to reject
		"ctrl+w h": "ctrl+w h",
	} {
		if got := normalizeSequence(in); got != want {
			t.Errorf("normalizeSequence(%q) = %q, want %q", in, got, want)
		}
	}
}

// withNav returns keys with a different select_oldest binding.
func withNav(keys config.Keys, binding string) config.Keys {
	keys.Nav.SelectOldest = binding
	return keys
}

// The end keys go to the ends of the help, as in every other list, not a page.
func TestHelpEndKeysGoToTheEnds(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.height = 20
	m = withHelp(t, m)
	end := len(m.helpLines()) - m.helpRows()
	if end <= m.helpRows() {
		t.Fatalf("the help is %d rows past a %d-row page; the test needs it longer", end, m.helpRows())
	}
	for _, key := range []struct {
		name string
		key  tea.KeyPressMsg
		want int
	}{
		{"end", tea.KeyPressMsg{Code: tea.KeyEnd}, end},
		{"home", tea.KeyPressMsg{Code: tea.KeyHome}, 0},
		{"G", keyCode('G'), end},
		{"g, the first half of gg", keyCode('g'), end},
		{"gg", keyCode('g'), 0},
	} {
		m = update(t, m, key.key)
		if m.reader.scroll != key.want {
			t.Errorf("%s scrolled to %d, want %d", key.name, m.reader.scroll, key.want)
		}
	}
}

// interrupt and paste are bindings like any other: rebound, the new key does the job
// from anywhere, typing included, and the old one is just a key.
func TestInterruptAndPasteAreRebindable(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Interrupt, keys.Paste = "ctrl+q", "ctrl+y"
	m := openTimeline(t, sized(t, withRooms(t, newModel())).WithKeys(keys))
	m.compose.insertMode = true

	if act := m.keys.lookup("ctrl+q", scopeGlobal); act != actInterrupt {
		t.Errorf("ctrl+q = %v, want interrupt", act)
	}
	if act := m.keys.lookup("ctrl+y", scopeGlobal); act != actPaste {
		t.Errorf("ctrl+y = %v, want paste", act)
	}
	if act := m.keys.lookup("ctrl+c", scopeGlobal); act == actInterrupt {
		t.Error("ctrl+c still interrupts after interrupt was rebound")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}); cmd == nil {
		t.Error("the rebound interrupt produced no command while typing, want quit")
	}
}

// Searching the help: / starts a filter, typing narrows the list as it is typed (a
// command key types too), enter keeps it for scrolling, esc clears it, and a second
// esc closes the help.
func TestHelpSearchNarrowsTheList(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = withHelp(t, m)
	all := len(m.helpBody())

	m = update(t, m, keyText("/"))
	if !m.reader.filtering {
		t.Fatal("/ did not start a search in the help")
	}
	for _, r := range "reply" {
		m = update(t, m, keyText(string(r)))
	}
	body := m.helpBody()
	if len(body) == 0 || len(body) >= all {
		t.Fatalf("filtering by reply kept %d of %d lines", len(body), all)
	}
	for _, line := range body {
		plain := strings.ToLower(ansi.Strip(line))
		if plain != "" && !strings.Contains(plain, "reply") && !isHelpTitle(m, line) {
			t.Errorf("a row that does not mention reply survived: %q", plain)
		}
	}
	if !strings.Contains(ansi.Strip(m.helpView()), "/reply") {
		t.Error("the help does not show what it is filtered by")
	}

	m = update(t, m, keyText("q")) // types, does not quit
	if m.reader.filter != "replyq" || !m.reader.showing(readerHelp) {
		t.Fatalf("q while searching: filter %q, help up %v; want it typed", m.reader.filter, m.reader.showing(readerHelp))
	}
	if !strings.Contains(ansi.Strip(strings.Join(m.helpBody(), "\n")), "nothing matches") {
		t.Error("a filter that matches nothing does not say so")
	}
	m = update(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})

	m = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.reader.filtering || m.reader.filter != "reply" {
		t.Fatalf("enter: filtering %v, filter %q; want the filter kept", m.reader.filtering, m.reader.filter)
	}
	m = update(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.reader.filter != "" || !m.reader.showing(readerHelp) {
		t.Fatalf("the first esc: filter %q, help up %v; want the filter cleared and help open",
			m.reader.filter, m.reader.showing(readerHelp))
	}
	if len(m.helpBody()) != all {
		t.Error("clearing the filter did not bring every line back")
	}
	m = update(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.reader.up() {
		t.Error("the second esc did not close the help")
	}
}

// A section whose title matches stays whole: "timeline" lists every timeline key.
func TestHelpSearchKeepsASectionByItsTitle(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	lines := m.helpLines()
	got := filterHelp(lines, "room list")
	var want int
	in := false
	for _, line := range lines {
		switch {
		case line == "":
			in = false
		case strings.EqualFold(ansi.Strip(line), "Room list"):
			in = true
			want++
		case in:
			want++
		}
	}
	if want < 3 {
		t.Fatalf("the Room list section has %d lines; the test needs a real one", want)
	}
	if len(got) < want {
		t.Errorf("searching the section's title kept %d lines, want its %d at least", len(got), want)
	}
}

// isHelpTitle reports a section title line of the help.
func isHelpTitle(m Model, line string) bool {
	for _, s := range scopeTitles {
		if strings.EqualFold(ansi.Strip(line), s.title) {
			return true
		}
	}
	return false
}

// Editing text is bound like everything else ([keys.edit]): a rebound key edits, the
// old one no longer does, "-" leaves an action unbound, and the composer's row motion
// follows its binding. A bad binding is reported and falls back to the default.
func TestEditingKeysAreRebindable(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Edit.DeleteWordBack = "ctrl+y"
	keys.Edit.Undo = "-"
	keys.Edit.RowUp = "ctrl+p"
	m := openTimeline(t, sized(t, withRooms(t, newModel())).WithKeys(keys))
	m.focus, m.compose.insertMode = paneTimeline, true
	m = typeInto(t, m, "one two")

	m, _ = press(t, m, tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if m.compose.input != "one two" {
		t.Errorf("ctrl+w edited (%q) after delete_word_back moved to ctrl+y", m.compose.input)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if m.compose.input != "one " {
		t.Errorf("ctrl+y left %q, want the word before the caret deleted", m.compose.input)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if m.compose.input != "one " {
		t.Errorf("ctrl+z undid (%q) with undo unbound", m.compose.input)
	}
	if act := m.keys.lookup("ctrl+p", scopeEdit); act != actEditRowUp {
		t.Errorf("ctrl+p = %v in the editing keys, want row_up", act)
	}
	if act := m.keys.lookup("up", scopeEdit); act == actEditRowUp {
		t.Error("up still moves a row after row_up was rebound")
	}

	t.Run("a bad binding is reported and keeps the default", func(t *testing.T) {
		t.Parallel()
		for name, set := range map[string]func(*config.Keys){
			"not a key name":            func(k *config.Keys) { k.Edit.Redo = "ctrl+bogus+key" },
			"another edit action's key": func(k *config.Keys) { k.Edit.Redo = "ctrl+u" },
		} {
			keys := config.DefaultKeys()
			set(&keys)
			km := newKeymap(keys)
			if len(km.issues) == 0 {
				t.Errorf("%s: no issue reported", name)
			}
			if act := km.lookup("ctrl+r", scopeEdit); act != actEditRedo {
				t.Errorf("%s: ctrl+r = %v, want redo kept on its default", name, act)
			}
			if act := km.lookup("ctrl+u", scopeEdit); act != actEditDeleteToStart {
				t.Errorf("%s: ctrl+u = %v, want delete_to_start untouched", name, act)
			}
		}
	})
}

// Positions in a list are bindings too: the completion strip's alt+1..5, the
// palette's digits and the correction walk's digits, each rebindable.
func TestListPositionsAreBindings(t *testing.T) {
	t.Parallel()
	keys := config.DefaultKeys()
	keys.Insert.Choose2 = "alt+w"
	keys.Spell.Choose1 = "x"
	km := newKeymap(keys)
	if n, ok := choiceOf(km.lookup("alt+w", scopeInsert)); !ok || n != 1 {
		t.Errorf("alt+w takes option %d,%v, want the second", n, ok)
	}
	if _, ok := choiceOf(km.lookup("alt+2", scopeInsert)); ok {
		t.Error("alt+2 still takes an option after insert.choose_2 moved")
	}
	if n, ok := choiceOf(km.lookup("alt+5", scopeInsert)); !ok || n != 4 {
		t.Errorf("alt+5 takes option %d,%v, want the fifth", n, ok)
	}
	if n, ok := suggestionOf(km.lookup("x", scopeSpell)); !ok || n != 0 {
		t.Errorf("x takes suggestion %d,%v, want the first", n, ok)
	}
	if n, ok := suggestionOf(km.lookup("9", scopeSpell)); !ok || n != 8 {
		t.Errorf("9 takes suggestion %d,%v, want the ninth", n, ok)
	}
}
