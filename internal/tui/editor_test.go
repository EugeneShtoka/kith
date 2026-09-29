package tui

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ctrl and alt spell modified keys as the terminal reports them.
func ctrl(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl} }
func alt(code rune) tea.KeyPressMsg  { return tea.KeyPressMsg{Code: code, Mod: tea.ModAlt} }

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// Every edit lands at the caret, not at the end of the text.
func TestEditorKeys(t *testing.T) {
	t.Parallel()

	// "hello world" with the caret after "hello".
	start := editor{text: "hello world", at: 5}
	for name, tc := range map[string]struct {
		key       tea.KeyPressMsg
		text      string
		at        int
		unclaimed bool
	}{
		"left":              {key: key(tea.KeyLeft), text: "hello world", at: 4},
		"right":             {key: key(tea.KeyRight), text: "hello world", at: 6},
		"home":              {key: key(tea.KeyHome), text: "hello world", at: 0},
		"end":               {key: key(tea.KeyEnd), text: "hello world", at: 11},
		"ctrl+a":            {key: ctrl('a'), text: "hello world", at: 0},
		"ctrl+e":            {key: ctrl('e'), text: "hello world", at: 11},
		"word left":         {key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}, text: "hello world", at: 0},
		"word right":        {key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}, text: "hello world", at: 11},
		"alt+b":             {key: alt('b'), text: "hello world", at: 0},
		"alt+f":             {key: alt('f'), text: "hello world", at: 11},
		"backspace":         {key: key(tea.KeyBackspace), text: "hell world", at: 4},
		"delete":            {key: key(tea.KeyDelete), text: "helloworld", at: 5},
		"ctrl+w":            {key: ctrl('w'), text: " world", at: 0},
		"ctrl+h":            {key: ctrl('h'), text: " world", at: 0},
		"alt+backspace":     {key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt}, text: " world", at: 0},
		"delete word after": {key: tea.KeyPressMsg{Code: tea.KeyDelete, Mod: tea.ModCtrl}, text: "hello", at: 5},
		"alt+d":             {key: alt('d'), text: "hello", at: 5},
		"ctrl+u":            {key: ctrl('u'), text: " world", at: 0},
		"ctrl+k":            {key: ctrl('k'), text: "hello", at: 5},
		// Not editing keys: they must reach whatever is behind the field.
		"enter":  {key: key(tea.KeyEnter), text: "hello world", at: 5, unclaimed: true},
		"esc":    {key: key(tea.KeyEscape), text: "hello world", at: 5, unclaimed: true},
		"ctrl+n": {key: ctrl('n'), text: "hello world", at: 5, unclaimed: true},
		"pgup":   {key: key(tea.KeyPgUp), text: "hello world", at: 5, unclaimed: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, claimed := start.edit(tc.key, defaultKeys)
			if claimed == tc.unclaimed {
				t.Fatalf("%s claimed = %v, want %v", tc.key.String(), claimed, !tc.unclaimed)
			}
			if got.text != tc.text || got.at != tc.at {
				t.Errorf("%s gave %q at %d, want %q at %d", tc.key.String(), got.text, got.at, tc.text, tc.at)
			}
		})
	}
}

// The ends clamp rather than wrap or panic.
func TestEditorClampsAtTheEnds(t *testing.T) {
	t.Parallel()

	for name, e := range map[string]editor{
		"empty":       {text: "", at: 0},
		"caret front": {text: "abc", at: 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, k := range []tea.KeyPressMsg{key(tea.KeyLeft), key(tea.KeyBackspace), ctrl('w'), ctrl('u')} {
				got, _ := e.edit(k, defaultKeys)
				if got.text != e.text || got.at != 0 {
					t.Errorf("%s at the front gave %q at %d, want %q at 0", k.String(), got.text, got.at, e.text)
				}
			}
		})
	}
	e := newEditor("abc")
	for _, k := range []tea.KeyPressMsg{key(tea.KeyRight), key(tea.KeyDelete), alt('d'), ctrl('k')} {
		got, _ := e.edit(k, defaultKeys)
		if got.text != "abc" || got.at != 3 {
			t.Errorf("%s at the end gave %q at %d, want %q at 3", k.String(), got.text, got.at, "abc")
		}
	}
}

// A grapheme is one keypress: 👍🏻 is two runes, and deleting half of it is wrong.
func TestEditorStepsByGrapheme(t *testing.T) {
	t.Parallel()

	e := newEditor("hi 👍🏻")
	back, _ := e.edit(key(tea.KeyBackspace), defaultKeys)
	if back.text != "hi " {
		t.Errorf("backspace over a toned emoji gave %q, want %q", back.text, "hi ")
	}
	left, _ := e.edit(key(tea.KeyLeft), defaultKeys)
	if left.at != len("hi ") {
		t.Errorf("left over a toned emoji landed at %d, want %d", left.at, len("hi "))
	}
	if got, _ := left.edit(key(tea.KeyRight), defaultKeys); got.at != len(e.text) {
		t.Errorf("right landed at %d, want the end at %d", got.at, len(e.text))
	}
}

// A word is a run of non-whitespace, so ctrl+w takes a whole path.
func TestEditorWordsAreWhitespaceDelimited(t *testing.T) {
	t.Parallel()

	e := newEditor("send /var/log/syslog")
	got, _ := e.edit(ctrl('w'), defaultKeys)
	if got.text != "send " {
		t.Errorf("ctrl+w gave %q, want the whole path gone", got.text)
	}
	// Trailing whitespace goes with the word before it, not on its own keystroke.
	spaced, _ := newEditor("one two   ").edit(ctrl('w'), defaultKeys)
	if spaced.text != "one " {
		t.Errorf("ctrl+w over trailing spaces gave %q, want %q", spaced.text, "one ")
	}
}

// A filter gets only the edits that keep the caret at the end.
func TestEditorTailSubset(t *testing.T) {
	t.Parallel()

	e := newEditor("one two")
	if got, claimed := e.editTail(ctrl('w'), defaultKeys); !claimed || got.text != "one " {
		t.Errorf("ctrl+w gave %q (claimed %v), want %q", got.text, claimed, "one ")
	}
	if got, claimed := e.editTail(ctrl('u'), defaultKeys); !claimed || got.text != "" {
		t.Errorf("ctrl+u gave %q (claimed %v), want it cleared", got.text, claimed)
	}
	for _, k := range []tea.KeyPressMsg{key(tea.KeyLeft), key(tea.KeyRight), key(tea.KeyHome), key(tea.KeyDelete)} {
		if _, claimed := e.editTail(k, defaultKeys); claimed {
			t.Errorf("%s must be left for the list to navigate with", k.String())
		}
	}
}

// Pasted text is made fit for its field: one-line fields collapse rows, the composer
// keeps them, and both drop control characters.
func TestPasteable(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		in        string
		want      string
		multiline string
		lines     int
	}{
		"plain":            {in: "hello", want: "hello", multiline: "hello", lines: 1},
		"trailing newline": {in: "hello\n", want: "hello", multiline: "hello", lines: 1},
		"crlf":             {in: "a\r\nb", want: "a b", multiline: "a\nb", lines: 2},
		"three lines":      {in: "a\nb\nc", want: "a b c", multiline: "a\nb\nc", lines: 3},
		"tab":              {in: "a\tb", want: "a b", multiline: "a b", lines: 1},
		"escape sequence": {
			in: "a\x1b[31mred\x1b[0m", want: "a[31mred[0m", multiline: "a[31mred[0m", lines: 1,
		},
		"empty": {in: "\n\n", want: "", multiline: "", lines: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, lines := pasteable(tc.in, false)
			if got != tc.want || lines != tc.lines {
				t.Errorf("pasteable(%q) = %q, %d lines; want %q, %d", tc.in, got, lines, tc.want, tc.lines)
			}
			if got, _ := pasteable(tc.in, true); got != tc.multiline {
				t.Errorf("pasteable(%q, multiline) = %q, want %q", tc.in, got, tc.multiline)
			}
		})
	}
}

// A paste lands at the caret of the field taking keystrokes.
func TestPasteIntoComposer(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = typeInto(t, m, "see  now")
	for range 4 {
		m, _ = press(t, m, key(tea.KeyLeft))
	}
	m = update(t, m, tea.PasteMsg{Content: "this"})
	if m.compose.input != "see this now" {
		t.Errorf("input = %q, want the paste at the caret", m.compose.input)
	}
	if m.compose.caret.at != len("see this") {
		t.Errorf("caret at %d, want %d — after what was pasted", m.compose.caret.at, len("see this"))
	}
}

// Pasting with nothing open starts a message.
func TestPasteStartsAMessage(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m.compose.insertMode = false
	m = update(t, m, tea.PasteMsg{Content: "https://example.com/x"})
	if !m.compose.insertMode {
		t.Error("pasting into the timeline should start composing")
	}
	if m.compose.input != "https://example.com/x" {
		t.Errorf("input = %q, want the pasted link", m.compose.input)
	}
}

// A multi-line paste keeps its lines in the composer, which grows to hold them.
func TestPasteKeepsLinesInTheComposer(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = update(t, m, tea.PasteMsg{Content: "one\ntwo\nthree"})
	if m.compose.input != "one\ntwo\nthree" {
		t.Errorf("input = %q, want the line breaks kept", m.compose.input)
	}
	if m.composerRows() != 3 {
		t.Errorf("composer is %d rows, want 3", m.composerRows())
	}
}

// A one-line field flattens the paste, and says so.
func TestPasteSaysWhenItFlattens(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = m.openPrompt(promptJoin)
	m = update(t, m, tea.PasteMsg{Content: "one\ntwo\nthree"})
	if m.prompt.input != "one two three" {
		t.Errorf("prompt = %q", m.prompt.input)
	}
	if !strings.Contains(m.status(), "3 lines") {
		t.Errorf("status = %q, want it to mention the 3 lines", m.status())
	}
}

// A paste while a list owns the keyboard goes nowhere.
func TestPasteIgnoredWhenAListOwnsTheKeyboard(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m.focus = paneRooms
	m.compose.insertMode = false
	m = update(t, m, tea.PasteMsg{Content: "nope"})
	if m.compose.input != "" {
		t.Errorf("input = %q, want the paste dropped outside the timeline", m.compose.input)
	}
}

// Prompts are fields too: a prefilled rename prompt edits at the caret.
func TestPromptEditing(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus = paneRooms
	m, _ = press(t, m, keyText("a")) // rooms.name
	if !m.prompt.active() || m.prompt.input != "Alpha" {
		t.Fatalf("prompt = %q (active %v), want it prefilled with the room name", m.prompt.input, m.prompt.active())
	}
	if m.compose.caret.at != len("Alpha") {
		t.Fatalf("caret at %d, want the end of the prefill", m.compose.caret.at)
	}
	m, _ = press(t, m, key(tea.KeyHome))
	m, _ = press(t, m, keyText("!"))
	if m.prompt.input != "!Alpha" {
		t.Errorf("prompt = %q, want the typed character at the caret", m.prompt.input)
	}
	m, _ = press(t, m, ctrl('e'))
	m, _ = press(t, m, ctrl('w'))
	if m.prompt.input != "" {
		t.Errorf("prompt = %q, want ctrl+w to take the word", m.prompt.input)
	}
	m = update(t, m, tea.PasteMsg{Content: "Alpha Team"})
	if m.prompt.input != "Alpha Team" {
		t.Errorf("prompt = %q, want the paste", m.prompt.input)
	}
}

// A caret owned by another field reads as the end of the text, so a prompt's caret
// cannot place text in the composer's draft.
func TestCaretDoesNotLeakBetweenFields(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = typeInto(t, m, "draft")
	m = m.openPromptWith(promptRoomName, "Alpha")
	m, _ = press(t, m, key(tea.KeyHome)) // caret to 0, owned by the prompt
	m, _ = press(t, m, key(tea.KeyEscape))
	m, _ = press(t, m, keyText("!"))
	if m.compose.input != "draft!" {
		t.Errorf("input = %q — the prompt's caret must not place text in the composer", m.compose.input)
	}
}

// The composer's window follows the caret on a line wider than the pane.
func TestCaretWindowKeepsTheCaretVisible(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("ab", 40)
	line := ansi.Strip(editedLine(newEditor(long), 20, true))
	if w := ansi.StringWidth(line); w > 20 {
		t.Errorf("line is %d cells wide, want at most 20: %q", w, line)
	}
	if !strings.HasSuffix(line, caretMark) {
		t.Errorf("line = %q, want the caret visible at the end", line)
	}
	if !strings.HasPrefix(line, "…") {
		t.Errorf("line = %q, want the clipped start marked", line)
	}
	if !strings.Contains(line, long[len(long)-6:]) {
		t.Errorf("line = %q, want the text just before the caret", line)
	}

	// Caret at the front of a long line: what follows it is what you can see.
	front := ansi.Strip(editedLine(editor{text: long, at: 0}, 20, true))
	if !strings.HasPrefix(front, caretMark) {
		t.Errorf("line = %q, want the caret at the start", front)
	}
	if !strings.HasSuffix(front, "…") {
		t.Errorf("line = %q, want the clipped end marked", front)
	}

	// It fits: no window, no ellipsis.
	if got := ansi.Strip(editedLine(newEditor("hi"), 20, true)); got != "hi"+caretMark {
		t.Errorf("short line = %q, want %q", got, "hi"+caretMark)
	}
}

// A mid-line mention completes in place, keeping what follows the caret.
func TestCompletionAcceptsMidLine(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "hi  — welcome")
	// By grapheme, not by byte: left steps over the em dash in one press.
	for range utf8.RuneCountInString(" — welcome") {
		m, _ = press(t, m, key(tea.KeyLeft))
	}
	m = typeInto(t, m, "@dana")
	if !m.completion.active {
		t.Fatal("typing @dana mid-line should open the popup")
	}
	m, _ = press(t, m, key(tea.KeyTab))
	if m.compose.input != "hi Dana Levi  — welcome" {
		t.Errorf("input = %q", m.compose.input)
	}
	if m.compose.caret.at != len("hi Dana Levi ") {
		t.Errorf("caret at %d, want it after the inserted name", m.compose.caret.at)
	}
}

// Moving the caret out of the query closes the popup.
func TestCompletionClosesWhenTheCaretLeaves(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "@dana")
	if !m.completion.active {
		t.Fatal("want a popup to close")
	}
	for range 5 {
		m, _ = press(t, m, key(tea.KeyLeft))
	}
	if m.completion.active {
		t.Error("the caret moved onto the trigger — the popup should be gone")
	}
}

// A filter takes ctrl+w, and leaves the arrows to the grid it narrows.
func TestFilterEditing(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t)
	m, _ = press(t, m, ctrl('e'))
	m = typeInto(t, m, "cof")
	if m.picker.filter != "cof" {
		t.Fatalf("filter = %q", m.picker.filter)
	}
	m, _ = press(t, m, ctrl('w'))
	if m.picker.filter != "" {
		t.Errorf("filter = %q, want ctrl+w to take the word", m.picker.filter)
	}
	m = typeInto(t, m, "cof")
	before := m.picker.cursor
	m, _ = press(t, m, key(tea.KeyRight))
	if m.picker.cursor == before && len(m.picker.items) > 1 {
		t.Error("right must still walk the grid while filtering, not move a caret")
	}
}

// The filter does not own the caret, so a filtered emoji lands at the composer's caret.
func TestFilteredEmojiLandsAtTheCaret(t *testing.T) {
	t.Parallel()

	m, _ := composingEmoji(t)
	m = typeInto(t, m, "coffee ?")
	m, _ = press(t, m, key(tea.KeyLeft))
	m, _ = press(t, m, ctrl('e'))
	m = typeInto(t, m, "cof")
	m, _ = press(t, m, key(tea.KeyEnter))
	if m.compose.input != "coffee ☕?" {
		t.Errorf("input = %q, want the emoji at the caret", m.compose.input)
	}
}

// An empty paste says so rather than silently doing nothing.
func TestEmptyPasteSaysSo(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = update(t, m, tea.ClipboardMsg{Content: "\n"})
	if m.compose.input != "" {
		t.Errorf("input = %q, want nothing", m.compose.input)
	}
	if !strings.Contains(m.status(), "nothing to paste") {
		t.Errorf("status = %q", m.status())
	}
}

// By default enter starts a line and ctrl+enter sends.
func TestEnterMakesALineAndCtrlEnterSends(t *testing.T) {
	t.Parallel()

	m, b := composing(t)
	m = typeInto(t, m, "one")
	m, _ = press(t, m, key(tea.KeyEnter))
	if len(b.sent) != 0 {
		t.Error("enter should not send by default")
	}
	m = typeInto(t, m, "two")
	if m.compose.input != "one\ntwo" {
		t.Fatalf("input = %q, want two lines", m.compose.input)
	}
	if m.composerRows() != 2 {
		t.Errorf("composer is %d rows, want 2", m.composerRows())
	}
	m, cmd := press(t, m, sendKey())
	if cmd == nil {
		t.Fatal("ctrl+enter should send")
	}
	if m.compose.input != "" {
		t.Errorf("input = %q, want the composer cleared", m.compose.input)
	}
}

// alt+enter sends too: ctrl+enter is indistinguishable from enter without the kitty
// keyboard protocol.
func TestAltEnterAlsoSends(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = typeInto(t, m, "hi")
	_, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	if cmd == nil {
		t.Error("alt+enter should send")
	}
}

// Flipping the pair makes enter send, as the settings row writes it.
func TestEnterSendsWhenFlipped(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	cfg := config.Config{}
	cfg.Keys.FillDefaults()
	if err := toggleEnterKey(&cfg, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.Keys.Insert.Send != "enter" || cfg.Keys.Insert.Newline != "ctrl+enter,alt+enter" {
		t.Fatalf("flip gave send=%q newline=%q", cfg.Keys.Insert.Send, cfg.Keys.Insert.Newline)
	}
	m.keys = newKeymap(cfg.Keys)
	if len(m.keys.issues) > 0 {
		t.Errorf("the flipped pair should not clash: %v", m.keys.issues)
	}
	m = typeInto(t, m, "hi")
	m, cmd := press(t, m, key(tea.KeyEnter))
	if cmd == nil {
		t.Error("enter should send once flipped")
	}
	if m.compose.input != "" {
		t.Errorf("input = %q, want it sent rather than broken", m.compose.input)
	}
	// And ctrl+enter now makes the line.
	m = typeInto(t, m, "a")
	m, _ = press(t, m, sendKey())
	if m.compose.input != "a\n" {
		t.Errorf("input = %q, want ctrl+enter to start a line once flipped", m.compose.input)
	}
}

// The settings row names the actual keys.
func TestEnterKeySettingRow(t *testing.T) {
	t.Parallel()

	cfg := config.Config{}
	cfg.Keys.FillDefaults()
	if got := enterKeyShow(cfg); got != "ctrl+enter sends · enter new line" {
		t.Errorf("row shows %q", got)
	}
	if err := toggleEnterKey(&cfg, ""); err != nil {
		t.Fatal(err)
	}
	if got := enterKeyShow(cfg); got != "enter sends · ctrl+enter new line" {
		t.Errorf("flipped row shows %q", got)
	}
}

// The rows the composer draws and the rows the message area gives up must agree.
func TestComposerHeightAndMessageAreaAgree(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	for want := 1; want <= composerMaxRows; want++ {
		if got := m.composerRows(); got != want {
			t.Fatalf("with %d lines the composer is %d rows, want %d", want, got, want)
		}
		if got := m.msgAreaRows(); got != m.height-5-want {
			t.Errorf("msgAreaRows = %d, want %d", got, m.height-5-want)
		}
		lines := m.composerLines(true)
		if len(lines) != want {
			t.Errorf("composerLines drew %d rows, want %d", len(lines), want)
		}
		m, _ = press(t, m, key(tea.KeyEnter))
	}
	// Past the cap it scrolls instead of growing, and the caret stays visible.
	for range 5 {
		m, _ = press(t, m, key(tea.KeyEnter))
	}
	if got := m.composerRows(); got != composerMaxRows {
		t.Errorf("composer is %d rows, want it capped at %d", got, composerMaxRows)
	}
	if got := len(m.composerLines(true)); got != composerMaxRows {
		t.Errorf("composerLines drew %d rows, want %d", got, composerMaxRows)
	}
	last := m.composerLines(true)[composerMaxRows-1]
	if !strings.Contains(last, caretMark) {
		t.Errorf("the caret should be on the last drawn row, got %q", stripStyles(last))
	}
	// And the divider says what is out of sight.
	if div := m.composerDivider(50); !strings.Contains(div, "above") {
		t.Errorf("divider = %q, want it to say how much is hidden", div)
	}
}

// A long line wraps onto the next row rather than scrolling sideways.
func TestLongLineWraps(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = typeInto(t, m, strings.Repeat("word ", 30))
	if m.composerRows() < 3 {
		t.Errorf("composer is %d rows — a 150-character line should wrap", m.composerRows())
	}
	for _, line := range m.composerLines(true) {
		if w := ansi.StringWidth(stripStyles(line)); w > m.contentWidth() {
			t.Errorf("row is %d cells wide, pane interior is %d: %q",
				w, m.contentWidth(), stripStyles(line))
		}
	}
}

// Wrapping keeps every byte, so the caret can sit on the space a row broke at.
func TestWrapSegmentsKeepEveryByte(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"spaces":      "the quick brown fox jumps over the lazy dog",
		"long word":   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bb",
		"newlines":    "one\ntwo\n\nthree",
		"trailing nl": "one\n",
		"empty":       "",
		"emoji":       "👍🏻 👨‍👩‍👧‍👦 ok fine then some more words here",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			segs := wrapSegments(text, 10)
			var rebuilt strings.Builder
			at := 0
			for i, seg := range segs {
				if seg.start < at {
					t.Fatalf("segment %d starts at %d, before the previous end %d", i, seg.start, at)
				}
				rebuilt.WriteString(text[at:seg.start]) // the newline between rows
				rebuilt.WriteString(text[seg.start:seg.end])
				at = seg.end
				if w := ansi.StringWidth(text[seg.start:seg.end]); w > 10 && seg.end-seg.start > 1 {
					t.Errorf("row %d is %d cells wide, want at most 10: %q", i, w, text[seg.start:seg.end])
				}
			}
			rebuilt.WriteString(text[at:])
			if rebuilt.String() != text {
				t.Errorf("rebuilt %q from the segments, want %q", rebuilt.String(), text)
			}
		})
	}
}

// A multi-line message sends intact, and a body opening with a slash is not a command.
func TestMultiLineSend(t *testing.T) {
	t.Parallel()

	b := &recordingReply{}
	m := update(t, New(context.Background(), b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus, m.compose.insertMode = paneTimeline, true

	m = typeInto(t, m, "/upload not really")
	m, _ = press(t, m, key(tea.KeyEnter))
	m = typeInto(t, m, "second line")
	m, cmd := press(t, m, sendKey())
	if cmd == nil {
		t.Fatal("sending produced no command")
	}
	m = deliver(t, m, cmd)
	if b.body != "/upload not really\nsecond line" {
		t.Errorf("sent %q, want both lines and no command interpretation", b.body)
	}
}

// The caret is the terminal's cursor: the marker never reaches the screen and the
// cursor sits where the typed text ends.
func TestCaretBecomesTheTerminalCursor(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = typeInto(t, m, "hello")
	v := m.View()
	if strings.Contains(v.Content, caretMark) {
		t.Error("the caret marker leaked into the frame")
	}
	if v.Cursor == nil {
		t.Fatal("composing should place the cursor")
	}
	rows := strings.Split(stripStyles(v.Content), "\n")
	if v.Cursor.Y >= len(rows) {
		t.Fatalf("cursor row %d, frame has %d", v.Cursor.Y, len(rows))
	}
	row := rows[v.Cursor.Y]
	if !strings.Contains(row, "hello") {
		t.Errorf("cursor is on row %d = %q, want the row being typed", v.Cursor.Y, row)
	}
	if rest := strings.TrimRight(trimCells(row, v.Cursor.X), " │"); rest != "" {
		t.Errorf("text after the cursor column: %q — it should sit right after %q", rest, "hello")
	}
	if !strings.HasSuffix(strings.TrimRight(headCells(row, v.Cursor.X), " "), "hello") {
		t.Errorf("row up to the cursor = %q, want it to end with the typed text",
			headCells(row, v.Cursor.X))
	}
}

// trimCells drops the first n cells of s.
func trimCells(s string, n int) string {
	return strings.TrimPrefix(s, headCells(s, n))
}

// Nothing to type into means no cursor.
func TestNoCursorWhenNotTyping(t *testing.T) {
	t.Parallel()

	base, _ := composing(t)
	base = typeInto(t, base, "draft")
	for name, m := range map[string]Model{
		"normal mode":  func() Model { c := base; c.compose.insertMode = false; return c }(),
		"help overlay": func() Model { c := base; c.reader = c.reader.opening(readerHelp); return c }(),
		"another pane": func() Model { c := base; c.focus, c.compose.insertMode = paneRooms, false; return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if v := m.View(); v.Cursor != nil {
				t.Errorf("cursor at %v, want it hidden", v.Cursor.Position)
			}
		})
	}
	// An open picker takes the keyboard, hiding the composer's caret.
	withPicker := base
	withPicker.picker = newContextPicker(actCopyURL, linkItems([]string{"https://a", "https://b"}))
	if v := withPicker.View(); v.Cursor != nil {
		t.Errorf("cursor at %v while a chooser owns the keyboard", v.Cursor.Position)
	}
	// A prompt places the cursor in the status line.
	prompt := m2WithPrompt(base)
	v := prompt.View()
	if v.Cursor == nil {
		t.Fatal("a prompt should place the cursor")
	}
	if v.Cursor.Y != prompt.height-1 {
		t.Errorf("prompt cursor on row %d, want the status line at %d", v.Cursor.Y, prompt.height-1)
	}
}

func m2WithPrompt(m Model) Model { return m.openPromptWith(promptJoin, "#room:x") }

// up and down move by a display row and keep the goal column.
func TestComposerRowMotion(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = typeInto(t, m, "first line")
	m, _ = press(t, m, key(tea.KeyEnter))
	m = typeInto(t, m, "no")
	m, _ = press(t, m, key(tea.KeyEnter))
	m = typeInto(t, m, "third line")

	m, _ = press(t, m, key(tea.KeyUp))
	if got := m.compose.input[:m.compose.caret.at]; got != "first line\nno" {
		t.Errorf("one row up landed after %q, want the end of the short row", got)
	}
	m, _ = press(t, m, key(tea.KeyUp))
	if got := m.compose.input[:m.compose.caret.at]; got != "first line" {
		t.Errorf("two rows up landed after %q — the goal column was not kept", got)
	}
	// And back down to where it started.
	m, _ = press(t, m, key(tea.KeyDown))
	m, _ = press(t, m, key(tea.KeyDown))
	if got := m.compose.input[:m.compose.caret.at]; got != "first line\nno\nthird line" {
		t.Errorf("back down landed after %q, want the original column", got)
	}
	// A plain edit forgets the goal, so the next up takes the column it is in.
	m, _ = press(t, m, key(tea.KeyLeft))
	m, _ = press(t, m, key(tea.KeyUp))
	if got := m.compose.input[:m.compose.caret.at]; got != "first line\nno" {
		t.Errorf("after moving left, up landed after %q", got)
	}
	// The ends clamp rather than wrapping to the other end of the message.
	m, _ = press(t, m, key(tea.KeyHome))
	before := m.compose.caret.at
	m, _ = press(t, m, key(tea.KeyUp))
	m, _ = press(t, m, key(tea.KeyUp))
	if m.compose.caret.at != 0 && m.compose.caret.at == before {
		t.Errorf("up from the first row moved to %d", m.compose.caret.at)
	}
}

// Row motion follows the wrap, not just the newlines.
func TestRowMotionFollowsTheWrap(t *testing.T) {
	t.Parallel()

	m, _ := composing(t)
	m = typeInto(t, m, strings.Repeat("word ", 30))
	rows := len(m.composerSegments())
	if rows < 3 {
		t.Fatalf("a 150-character line wrapped to %d rows", rows)
	}
	start := m.compose.caret.at
	m, _ = press(t, m, key(tea.KeyUp))
	if m.compose.caret.at >= start {
		t.Errorf("up did not move within the wrapped line: %d → %d", start, m.compose.caret.at)
	}
	if got := caretSegment(m.composerSegments(), m.compose.caret.at); got != rows-2 {
		t.Errorf("up landed on row %d of %d, want %d", got, rows, rows-2)
	}
}

// A Hebrew display name is reordered where drawn, but stays logical as data.
func TestRTLNamesAreReorderedWhereDrawn(t *testing.T) {
	t.Parallel()

	const logical = "דנה לוי"
	visual := reorder(logical, bidi.RightToLeft)
	if visual == logical {
		t.Fatal("the fixture name is not reordered by the bidi pass")
	}

	m, _ := composing(t)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{
			{ID: "$1", RoomID: "!a:x", Sender: "@d:x", SenderName: logical, Body: "hi", Timestamp: at(1)},
		},
	}})
	frame := stripStyles(m.View().Content)
	if !strings.Contains(frame, visual) {
		t.Error("the sender column should show the reordered name")
	}
	// processedName stays logical: it prefills prompts and builds rule labels.
	if got := m.processedName(m.timeline.messages[0]); got != logical {
		t.Errorf("processedName = %q, want the logical string", got)
	}
	// senderName is logical and isolated, reordered by the sentence it goes into.
	if got := m.senderName(m.timeline.messages[0]); got != isolate(logical) {
		t.Errorf("senderName = %q, want the logical string isolated", got)
	}
	if got := displayTitle("from " + m.senderName(m.timeline.messages[0])); got != "from "+visual {
		t.Errorf("drawn = %q, want the name reordered inside the sentence", got)
	}
}

// Undo takes back whole edits: a typing run, a backspace run, or one kill key.
func TestUndoStepsAreWholeEdits(t *testing.T) {
	t.Parallel()

	e := newEditor("")
	for _, r := range "hello world" {
		e = e.insert(string(r))
	}
	if e.text != "hello world" {
		t.Fatalf("typed text = %q", e.text)
	}
	back, ok := e.undo()
	if !ok || back.text != "hello " {
		t.Fatalf("undo gave %q (ok=%v), want %q", back.text, ok, "hello ")
	}
	back, ok = back.undo()
	if !ok || back.text != "" {
		t.Fatalf("second undo gave %q (ok=%v), want the field empty", back.text, ok)
	}
	if _, more := back.undo(); more {
		t.Error("a third undo claimed there was something left")
	}
	// Redo retraces both steps.
	fwd, ok := back.redo()
	if !ok || fwd.text != "hello " {
		t.Fatalf("redo gave %q (ok=%v), want %q", fwd.text, ok, "hello ")
	}
	fwd, _ = fwd.redo()
	if fwd.text != "hello world" {
		t.Errorf("second redo gave %q, want the whole line back", fwd.text)
	}
}

// A kill key is one step, and a run of backspaces coalesces.
func TestUndoRestoresWhatAKillKeyTook(t *testing.T) {
	t.Parallel()

	line := "the whole line"
	killed := newEditor(line).deleteToStart()
	if killed.text != "" {
		t.Fatalf("ctrl+u left %q", killed.text)
	}
	back, ok := killed.undo()
	if !ok || back.text != line {
		t.Fatalf("undo after ctrl+u gave %q (ok=%v), want the line back", back.text, ok)
	}
	if back.at != len(line) {
		t.Errorf("caret at %d, want %d — undo restores where you were", back.at, len(line))
	}

	e := newEditor("abcdef")
	for range 4 {
		e = e.backspace()
	}
	if e.text != "ab" {
		t.Fatalf("four backspaces left %q", e.text)
	}
	restored, _ := e.undo()
	if restored.text != "abcdef" {
		t.Errorf("undo after a run of backspaces gave %q, want all six characters", restored.text)
	}
}

// Editing after an undo drops the redo stack.
func TestEditingAfterUndoDropsTheRedo(t *testing.T) {
	t.Parallel()

	e := newEditor("").insert("one").insert(" ").insert("two")
	back, _ := e.undo()
	branched := back.insert("!")
	if _, ok := branched.redo(); ok {
		t.Error("redo survived an edit made after the undo")
	}
}

// A caret motion ends the undo step in progress.
func TestCaretMotionBreaksTheUndoStep(t *testing.T) {
	t.Parallel()

	e := newEditor("").insert("a").insert("b").home().insert("x")
	back, ok := e.undo()
	if !ok || back.text != "ab" {
		t.Fatalf("undo gave %q (ok=%v), want %q — only the edit after the motion", back.text, ok, "ab")
	}
}

// A paste is one step whatever its length, and never folded into the typing around it.
func TestPasteIsItsOwnUndoStep(t *testing.T) {
	t.Parallel()

	e := newEditor("").insert("see ").insert("https://example.org/a/long/path").insert("!")
	back, _ := e.undo()
	if back.text != "see https://example.org/a/long/path" {
		t.Fatalf("undo gave %q, want the typed character back only", back.text)
	}
	back, _ = back.undo()
	if back.text != "see " {
		t.Errorf("second undo gave %q, want the paste gone in one step", back.text)
	}
}

// The undo history is bounded.
func TestUndoHistoryIsBounded(t *testing.T) {
	t.Parallel()

	e := newEditor("")
	for i := range undoDepth * 3 {
		e = e.insert(string(rune('a' + i%26))).insert(" ")
	}
	if len(e.past) > undoDepth {
		t.Errorf("history holds %d steps, want at most %d", len(e.past), undoDepth)
	}
}

// Undo survives between keystrokes, although the editor is rebuilt on every key.
func TestUndoSurvivesBetweenKeystrokes(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "hello"))
	m, _ = press(t, m, keyText("i"))
	for _, r := range "one two" {
		next, _ := press(t, m, keyText(string(r)))
		m = next
	}
	if m.compose.input != "one two" {
		t.Fatalf("composer holds %q", m.compose.input)
	}
	back, _ := press(t, m, tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if back.compose.input != "one " {
		t.Fatalf("ctrl+z gave %q, want %q", back.compose.input, "one ")
	}
	fwd, _ := press(t, back, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if fwd.compose.input != "one two" {
		t.Errorf("ctrl+r gave %q, want the word back", fwd.compose.input)
	}
}

// defaultKeys is the keymap with no [keys] configured.
var defaultKeys = newKeymap(config.DefaultKeys())
