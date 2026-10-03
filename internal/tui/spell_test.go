package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The composer's half of spelling: when it asks, what it keeps between asking and being
// answered, and where the marks land on a row that has been bidi-reordered.

// The fixtures below are misspelled on purpose — a spellchecker's tests need something to
// find, and a correctly spelled word would be testing nothing.
//
//nolint:misspell // deliberate: these are the inputs a spellchecker exists to catch
const (
	typoReceive = "recieve"
	typoThe     = "teh"
	// typoMisspell is the one-suggestion case: a word an engine has exactly one
	// answer for, which is what autocorrect fires on without looking further.
	typoMisspell = "mispell"
)

// The drafts they appear in, composed at compile time so each misspelling is written
// exactly once in the package.
const (
	draftTypo     = "I " + typoReceive + " mail"
	draftFixed    = "I receive mail"
	draftPrefixed = "Yes, " + draftTypo
	draftTwoTypos = "I " + typoReceive + " " + typoThe + " mail"
	draftOneFixed = "I " + typoReceive + " the mail"
	draftBidi     = "שלום " + typoReceive + " עולם"
)

// spellBackend answers spelling checks with whatever it was told to, and counts how
// many times it was asked.
type spellBackend struct {
	apitest.Nop
	mu     sync.Mutex
	asked  []string
	found  []domain.Misspelling
	err    error
	blocks chan struct{}
	// taught is what the correction walk told it to learn, and learnErr what to answer
	// when it does.
	taught    []learned
	dismissed []string
	learnErr  error
	sent      []domain.Draft
}

func (b *spellBackend) Send(_ context.Context, _ domain.RoomID, draft domain.Draft) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, draft)
	return nil
}

func (b *spellBackend) posted() []domain.Draft {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.Draft(nil), b.sent...)
}

func (b *spellBackend) CheckSpelling(_ context.Context, text string) ([]domain.Misspelling, error) {
	b.mu.Lock()
	b.asked = append(b.asked, text)
	blocks, found, err := b.blocks, b.found, b.err
	b.mu.Unlock()
	if blocks != nil {
		<-blocks
	}
	return found, err
}

func (b *spellBackend) questions() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.asked...)
}

// wrong is one misspelling of word in text, found at its first occurrence.
func wrong(text, word string, suggestions ...string) domain.Misspelling {
	at := strings.Index(text, word)
	if at < 0 {
		panic(fmt.Sprintf("%q is not in %q", word, text))
	}
	return domain.Misspelling{Word: word, Start: at, End: at + len(word), Suggestions: suggestions}
}

// spellComposing is a model in insert mode with a backend that answers spelling.
func spellComposing(t *testing.T, b *spellBackend) Model {
	t.Helper()
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus, m.compose.insertMode = paneTimeline, true
	return m.clearStatus()
}

// Typing arms a check; the check that runs is for the draft as it stands when the
// debounce expires, not for each of the keystrokes along the way.
func TestSpellCheckIsAskedOncePerPause(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	// The tick is stubbed out for the package (see TestMain), so arming is observed
	// rather than waited for: every keystroke bumps the generation, and only the tick
	// carrying the current one asks anything.
	m = typeInto(t, m, typoReceive)
	if m.spell.text != typoReceive {
		t.Fatalf("the composer thinks the draft is %q", m.spell.text)
	}

	// A tick armed three keystrokes ago finds a newer draft and does nothing.
	stale, cmd := m.handleSpellTick(spellTickMsg{gen: m.spell.gen - 3})
	m = stale
	if cmd != nil {
		t.Error("a stale tick asked a question; the keystroke that overtook it armed its own")
	}
	// The current one does.
	fresh, cmd := m.handleSpellTick(spellTickMsg{gen: m.spell.gen})
	m = fresh
	if cmd == nil {
		t.Fatal("the current tick asked nothing")
	}
	cmd()
	if got := b.questions(); len(got) != 1 || got[0] != typoReceive {
		t.Errorf("the backend was asked %v, want exactly [%s]", got, typoReceive)
	}
}

// An empty composer is not a question. Sending a message clears the draft, and errors
// found in it would otherwise be drawn against whatever is typed next.
func TestSendingClearsTheMarks(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m = typeInto(t, m, typoReceive)
	m.spell.shown = []domain.Misspelling{wrong(typoReceive, typoReceive, "receive")}

	m.compose = m.compose.cleared()
	m, cmd := m.armSpellCheck()
	if len(m.spell.shown) != 0 {
		t.Errorf("an emptied composer still draws %v", m.spell.shown)
	}
	if cmd != nil {
		t.Error("an empty draft armed a check; there is nothing in it to check")
	}
}

// Nothing to check with ends the feature for the session rather than being reported.
func TestNothingToCheckWithStopsTheAsking(t *testing.T) {
	t.Parallel()

	b := &spellBackend{err: api.ErrSpellUnavailable}
	m := spellComposing(t, b)
	m = typeInto(t, m, "hello")

	next, cmd := m.handleSpellTick(spellTickMsg{gen: m.spell.gen})
	m = next
	msg := cmd()
	answered, _ := m.handleSpellChecked(msg.(spellCheckedMsg))
	m = answered

	if !m.spell.off {
		t.Fatal("ErrSpellUnavailable did not turn checking off")
	}
	m = typeInto(t, m, " there")
	if _, cmd := m.armSpellCheck(); cmd != nil {
		t.Error("still arming checks after the backend said there is nothing to check with")
	}
	if m.status() != "" {
		t.Errorf("status line says %q; nobody asked for this feature, so its absence is not news", m.status())
	}
}

// The heart of it: an answer about a draft that has since been edited is moved onto
// the draft as it is, rather than drawn where the words used to be.
func TestMarksFollowTheirWordsAcrossAnEdit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		from  string
		to    string
		marks []domain.Misspelling
		want  []string // the text each surviving mark now covers
	}{
		{
			name:  "typing before the word moves it along",
			from:  draftTypo,
			to:    draftPrefixed,
			marks: []domain.Misspelling{wrong(draftTypo, typoReceive)},
			want:  []string{typoReceive},
		},
		{
			name:  "typing after it leaves it alone",
			from:  draftTypo,
			to:    draftTypo + " today",
			marks: []domain.Misspelling{wrong(draftTypo, typoReceive)},
			want:  []string{typoReceive},
		},
		{
			name:  "deleting before it pulls it back",
			from:  draftPrefixed,
			to:    draftTypo,
			marks: []domain.Misspelling{wrong(draftPrefixed, typoReceive)},
			want:  []string{typoReceive},
		},
		{
			name:  "editing the word itself drops the mark",
			from:  draftTypo,
			to:    draftFixed,
			marks: []domain.Misspelling{wrong(draftTypo, typoReceive)},
			want:  nil,
		},
		{
			name: "one of two survives",
			from: draftTwoTypos,
			to:   draftOneFixed,
			marks: []domain.Misspelling{
				wrong(draftTwoTypos, typoReceive),
				wrong(draftTwoTypos, typoThe),
			},
			want: []string{typoReceive},
		},
		{
			name:  "a hebrew word keeps its bytes",
			from:  "שלום עולם yesterday",
			to:    "אמרתי שלום עולם yesterday",
			marks: []domain.Misspelling{wrong("שלום עולם yesterday", "yesterday")},
			want:  []string{"yesterday"},
		},
		{
			// The flicker.
			name:  "a letter added to the end of a marked word drops the mark",
			from:  "hello wor",
			to:    "hello worl",
			marks: []domain.Misspelling{{Word: "wor", Start: 6, End: 9}},
			want:  nil,
		},
		{
			name:  "and a letter added to the front of one",
			from:  "wor now",
			to:    "swor now",
			marks: []domain.Misspelling{{Word: "wor", Start: 0, End: 3}},
			want:  nil,
		},
		{
			// The other half of the same rule: the word is finished, so the mark is
			// about a whole word again and belongs on screen.
			name:  "finishing the word keeps it",
			from:  "hello wor",
			to:    "hello wor ",
			marks: []domain.Misspelling{{Word: "wor", Start: 6, End: 9}},
			want:  []string{"wor"},
		},
		{
			name:  "punctuation finishes a word too",
			from:  "hello wor",
			to:    "hello wor, yes",
			marks: []domain.Misspelling{{Word: "wor", Start: 6, End: 9}},
			want:  []string{"wor"},
		},
		{
			// An apostrophe is inside a word rather than after it, which is why the
			// boundary test is endsWord and not "is it a letter".
			name:  "an apostrophe does not finish it",
			from:  "dont",
			to:    "dont'",
			marks: []domain.Misspelling{{Word: "dont", Start: 0, End: 4}},
			want:  nil,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := shiftRanges(c.marks, c.from, c.to)
			if len(got) != len(c.want) {
				t.Fatalf("%d marks survived, want %d: %v", len(got), len(c.want), got)
			}
			for i, r := range got {
				if r.Start < 0 || r.End > len(c.to) {
					t.Fatalf("mark %d is outside the draft: %+v in %q", i, r, c.to)
				}
				if covered := c.to[r.Start:r.End]; covered != c.want[i] {
					t.Errorf("mark %d covers %q, want %q", i, covered, c.want[i])
				}
			}
		})
	}
}

// An answer computed for an older draft still lands in the right place, because it is
// shifted the same way an edit shifts what is already drawn.
func TestAnAnswerAboutAnOlderDraftIsMovedOntoTheNewOne(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m = typeInto(t, m, draftTypo)
	asked := m.spell.text
	// Two more characters arrive while the question is in flight.
	m = typeInto(t, m, "!!")

	next, _ := m.handleSpellChecked(spellCheckedMsg{
		gen:   m.spell.gen,
		text:  asked,
		found: []domain.Misspelling{wrong(asked, typoReceive)},
	})
	m = next

	if len(m.spell.shown) != 1 {
		t.Fatalf("marks = %v, want one", m.spell.shown)
	}
	r := m.spell.shown[0]
	if got := m.compose.input[r.Start:r.End]; got != typoReceive {
		t.Errorf("the mark covers %q in %q, want %q", got, m.compose.input, typoReceive)
	}
}

// An older answer must not replace a newer one.
func TestAnOvertakenAnswerIsDropped(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m = typeInto(t, m, draftTypo)

	newer, _ := m.handleSpellChecked(spellCheckedMsg{gen: 9, text: m.spell.text,
		found: []domain.Misspelling{wrong(m.spell.text, typoReceive)}})
	m = newer
	older, _ := m.handleSpellChecked(spellCheckedMsg{gen: 4, text: m.spell.text})
	m = older

	if len(m.spell.shown) != 1 {
		t.Errorf("an answer from generation 4 replaced one from generation 9: %v", m.spell.shown)
	}
}

// The word the caret is inside is not marked.
func TestTheWordBeingTypedIsNotMarked(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m = typeInto(t, m, draftTypo)
	m.spell.shown = []domain.Misspelling{
		wrong(m.spell.text, typoReceive),
		wrong(m.spell.text, "mail"),
	}

	// The caret is at the end, inside "mail".
	if got := marked(m); len(got) != 1 || got[0] != typoReceive {
		t.Errorf("marked %v with the caret in \"mail\", want only the typo", got)
	}
	// Moved into the typo, it is that one that goes quiet.
	m = withCaret(m, strings.Index(m.spell.text, typoReceive)+3)
	if got := marked(m); len(got) != 1 || got[0] != "mail" {
		t.Errorf("marked %v with the caret in the typo, want only mail", got)
	}
	// And out of both, both are drawn.
	m = withCaret(m, 1)
	if got := marked(m); len(got) != 2 {
		t.Errorf("marked %v with the caret between words, want both", got)
	}
}

// underline = "none" stops the drawing and not the checking.
func TestUnderlineNoneDrawsNothingAndStillChecks(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m.conf.base.Spell.Underline = config.SpellNoUnderline
	m = typeInto(t, m, typoReceive+" mail")
	m.spell.shown = []domain.Misspelling{wrong(m.spell.text, typoReceive)}

	if got := m.composerMarks(); got != nil {
		t.Errorf("underline = none drew %v", got)
	}
	if m.spell.off {
		t.Error("underline = none turned checking off; it is the drawing that stops")
	}
	// And the questions keep being asked, which is what leaves the correction walk
	// something to walk.
	next, cmd := m.handleSpellTick(spellTickMsg{gen: m.spell.gen})
	m = next
	if cmd == nil {
		t.Fatal("underline = none stopped the checker being asked")
	}
	cmd()
	if got := b.questions(); len(got) != 1 || got[0] != m.spell.text {
		t.Errorf("the backend was asked %v, want the draft", got)
	}
}

// Drawn, with Hebrew around it: the marks land on the English word wherever the bidi
// reorder put it, and the row still says what was typed.
func TestTheComposerUnderlinesInAnRTLLine(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m = typeInto(t, m, draftBidi)
	m.spell.shown = []domain.Misspelling{wrong(m.spell.text, typoReceive)}
	// The caret is at the end of the draft, which is inside no marked word here.

	lines := m.composerLines(true)
	if len(lines) == 0 {
		t.Fatal("the composer drew nothing")
	}
	row := lines[0]
	if !strings.Contains(row, "\x1b[") {
		t.Fatalf("no underline in a row with a misspelling in it:\n  %q", row)
	}
	// Every character typed is still on the row, in the order the terminal draws it.
	plain := stripStyles(row)
	for _, word := range []string{typoReceive, reverseClusters("שלום"), reverseClusters("עולם")} {
		if !strings.Contains(plain, word) {
			t.Errorf("the row lost %q:\n  %q", word, plain)
		}
	}
	// And it is the misspelled word that carries them, in one pair, with nothing of its
	// Hebrew neighbors caught inside.
	if marked := m.theme.SpellMark(ansi.UnderlineCurly, false).Styled(typoReceive); !strings.Contains(row, marked) {
		t.Errorf("the row does not carry %q:\n  %q", marked, row)
	}
}

// A clean draft costs no escapes at all — which is the common case, on every frame.
func TestACleanComposerRowCarriesNoEscapes(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m = typeInto(t, m, "שלום עולם")

	for i, row := range m.composerLines(true) {
		// The gutter has its own colors; what must be plain is the text after it.
		if strings.Contains(row[strings.LastIndex(row, "\x1b[m")+3:], "\x1b[") {
			t.Errorf("row %d carries escapes with nothing misspelled:\n  %q", i, row)
		}
	}
}

// marked is the words composerMarks would draw, read out of the draft.
func marked(m Model) []string {
	var out []string
	for _, r := range m.composerMarks() {
		out = append(out, m.spell.text[r.Start:r.End])
	}
	return out
}

// withCaret puts the composer's caret at a byte offset.
func withCaret(m Model, at int) Model {
	e := m.editorFor(fieldComposer)
	e.at = at
	return m.store(fieldComposer, e)
}

// rareHint is a rare-word mark: a word the engine *accepted*, carrying the commoner words
// the frequency list says might have been meant.
func rareHint(text, word string, alternatives ...string) domain.Misspelling {
	m := wrong(text, word, alternatives...)
	m.Rare = true
	return m
}

// The two marks are drawn in one pass and told apart by their style: a misspelling gets
// the alert-colored curly, a hint the muted dotted.
func TestBothMarksAreDrawnEachInItsOwnStyle(t *testing.T) {
	t.Parallel()

	const draft = "I " + typoReceive + " the mail"
	b := &spellBackend{found: []domain.Misspelling{
		wrong(draft, typoReceive, "receive"),
		rareHint(draft, "mail", "mall", "mile"),
	}}
	m := spellComposing(t, b)
	m = typeInto(t, m, draft)
	m = checkedNow(t, m)

	if len(m.spell.shown) != 2 {
		t.Fatalf("drawing %+v, want the misspelling and the hint", m.spell.shown)
	}
	// The caret sits at the end of the draft, so `mail` is the word being typed and is
	// left undrawn; the marks themselves are both kept.
	m = typeInto(t, m, " ")
	m.spell.shown = shiftRanges(b.found, draft, m.spell.text)
	marks := m.composerMarks()
	if len(marks) != 2 {
		t.Fatalf("composerMarks = %+v, want both", marks)
	}
	row := m.markedRow(m.spell.text, bidi.LeftToRight, marks)
	wrongStyle := m.theme.SpellMark(ansi.UnderlineCurly, false).Styled(typoReceive)
	hintStyle := m.theme.SpellMark(ansi.UnderlineDotted, true).Styled("mail")
	if !strings.Contains(row, wrongStyle) {
		t.Errorf("the misspelling is not drawn in the error style:\n  %q", row)
	}
	if !strings.Contains(row, hintStyle) {
		t.Errorf("the hint is not drawn in the quieter style:\n  %q", row)
	}
}

// rare_underline = "none" is the retreat position: the detection stays, the second
// underline goes, and the walk still has the hint when it is opened on purpose.
func TestRareUnderlineNoneDropsTheMarkAndKeepsTheCheck(t *testing.T) {
	t.Parallel()

	const draft = "I " + typoReceive + " the mail"
	b := &spellBackend{found: []domain.Misspelling{
		wrong(draft, typoReceive, "receive"),
		rareHint(draft, "mail", "mall", "mile"),
	}}
	m := spellComposing(t, b)
	m.conf.base.Spell.RareUnderline = config.SpellNoUnderline
	m = typeInto(t, m, draft+" ")
	m = checkedNow(t, m)
	m.spell.shown = shiftRanges(b.found, draft, m.spell.text)

	marks := m.composerMarks()
	if len(marks) != 1 || marks[0].Rare {
		t.Fatalf("composerMarks = %+v, want only the misspelling", marks)
	}
	// Still found, which is what the walk walks.
	if len(m.spell.shown) != 2 {
		t.Errorf("the hint stopped being found: %+v", m.spell.shown)
	}
}

// The first hint of a session says what a dotted underline means; no later one does.
func TestTheFirstHintExplainsItselfOnce(t *testing.T) {
	t.Parallel()

	const draft = "the mail"
	b := &spellBackend{found: []domain.Misspelling{rareHint(draft, "mail", "mall")}}
	m := spellComposing(t, b)
	m = typeInto(t, m, draft)
	m = checkedNow(t, m)

	said := m.st.event
	if !strings.Contains(said, "rare") {
		t.Fatalf("the first hint said %q, want an explanation of the mark", said)
	}
	m = m.clearStatus()
	m = checkedNow(t, m)
	if m.st.event != "" {
		t.Errorf("the second hint explained itself again: %q", m.st.event)
	}
}

// checkedNow runs the armed check and takes its answer, which is three steps every
// test here would otherwise spell out.
func checkedNow(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.handleSpellTick(spellTickMsg{gen: m.spell.gen})
	m = next
	if cmd == nil {
		t.Fatal("the current tick asked nothing")
	}
	msg, ok := cmd().(spellCheckedMsg)
	if !ok {
		t.Fatal("the check answered something else")
	}
	after, _ := m.handleSpellChecked(msg)
	m = after
	return m
}

// The same guard where it matters most: a hint must never be what stands between
// somebody and sending their message.
func TestRareHintsDoNotHoldUpASend(t *testing.T) {
	t.Parallel()

	const draft = "send this"
	b := &spellBackend{found: []domain.Misspelling{rareHint(draft, "send", "sent")}}
	m := spellComposing(t, b)
	m.conf.base.Spell.CheckBeforeSend = true
	m = typeInto(t, m, draft)

	msg, ok := m.checkBeforeSendCmd(m.spell.gen, draft)().(spellBeforeSendMsg)
	if !ok {
		t.Fatal("the send check answered something else")
	}
	if len(msg.found) != 0 {
		t.Errorf("the send gate was handed %+v, want nothing to walk", msg.found)
	}
}

// Typing a word must never underline it, at any point, by any route.
func TestNothingIsDrawnOnTheWordBeingTyped(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	for letter := range strings.SplitSeq(typoReceive, "") {
		m = typeInto(t, m, letter)
		// The engine is asked about the draft as it stands and says the half-typed word
		// is not a word, which is true and useless: it is not a word *yet*.
		b.found = []domain.Misspelling{{Word: m.spell.text, Start: 0, End: len(m.spell.text)}}
		m = checkedNow(t, m)
		if marks := m.composerMarks(); len(marks) != 0 {
			t.Fatalf("typing %q drew %+v", m.spell.text, marks)
		}
		// And the keystroke *after* an answer has landed, which is where the mark used
		// to reappear: the caret moves past the end of a mark that is now a prefix.
		m = typeInto(t, m, "x")
		if marks := m.composerMarks(); len(marks) != 0 {
			t.Fatalf("the letter after the check drew %+v on %q", marks, m.spell.text)
		}
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}

	// Finish the word and it is marked, because now there is a word to mark.
	m = typeInto(t, m, " ")
	b.found = []domain.Misspelling{wrong(m.spell.text, typoReceive, "receive")}
	m = checkedNow(t, m)
	marks := m.composerMarks()
	if len(marks) != 1 || marks[0].Word != typoReceive {
		t.Fatalf("the finished word is not marked: %+v", marks)
	}
}
