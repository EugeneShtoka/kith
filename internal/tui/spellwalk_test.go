package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The correction walk: what it is walking, what each key does to the draft, and the
// two things that would make it useless — a list that renumbers itself while you read
// it, and a correction you cannot take back in one keystroke.

// learned is one word the walk taught the backend, and how long it was meant to last.
type learned struct {
	word    string
	forever bool
}

func (b *spellBackend) LearnWord(_ context.Context, word string, forever bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.taught = append(b.taught, learned{word: word, forever: forever})
	return b.learnErr
}

func (b *spellBackend) lessons() []learned {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]learned(nil), b.taught...)
}

// walking is a model at the message cursor with a draft and its errors already found —
// the state you are in when you press the key that opens the walk.
func walking(t *testing.T, b *spellBackend, draft string, marks ...domain.Misspelling) Model {
	t.Helper()
	m := spellComposing(t, b)
	m = typeInto(t, m, draft)
	m.spell.shown = marks
	// esc, in effect: the walk is opened from the message cursor, because z= in the
	// composer would type "z=" into the message.
	m.compose.insertMode = false
	return m
}

// openWalk presses the binding rather than calling the opener, so the chord itself is
// covered: "z =" is two presses, and only the second resolves to anything.
func openWalk(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = chord(t, m, "z", "=")
	if !m.walk.active {
		t.Fatalf("z= did not open the walk; status says %q", m.status())
	}
	return m
}

// altS is the composer's opener, pressed the way a person presses it.
func altS() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 's', Mod: tea.ModAlt} }

// The list is what is underlined, in the order it appears in the draft, and the popup
// says which of them you are on.
func TestTheWalkOpensOnWhatIsUnderlined(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " " + typoThe + " mail"
	m := walking(t, b, draft,
		wrong(draft, typoReceive, "receive"), wrong(draft, typoThe, "the", "tea"))
	m = openWalk(t, m)

	if len(m.walk.items) != 2 || m.walk.at != 0 {
		t.Fatalf("walking %d items at %d, want 2 at 0", len(m.walk.items), m.walk.at)
	}
	row := stripStyles(strings.Join(m.spellWalkLines(40), "\n"))
	for _, want := range []string{typoReceive, "1/2", "1  receive"} {
		if !strings.Contains(row, want) {
			t.Errorf("the popup does not say %q:\n%s", want, row)
		}
	}
}

// A number replaces the word and moves on — and what is left of the list moves with the
// draft.
func TestANumberReplacesTheWordAndCarriesTheRestAlong(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := typoThe + " " + typoReceive + " mail"
	m := walking(t, b, draft,
		wrong(draft, typoThe, "the"), wrong(draft, typoReceive, "receive"))
	m = openWalk(t, m)

	m, _ = press(t, m, keyText("1"))
	if want := "the " + typoReceive + " mail"; m.compose.input != want {
		t.Fatalf("the draft is %q, want %q", m.compose.input, want)
	}
	if m.walk.at != 1 {
		t.Fatalf("the walk is on %d, want it to have advanced to 1", m.walk.at)
	}
	// The first typo gained a byte in the correction, so the second error moved one
	// byte right with it.
	next := m.walk.items[1]
	if covered := m.compose.input[next.Start:next.End]; covered != typoReceive {
		t.Errorf("the next mark covers %q, want %q", covered, typoReceive)
	}
	// And taking it corrects the right word, which is the point of the arithmetic.
	m, _ = press(t, m, keyText("1"))
	if want := "the receive mail"; m.compose.input != want {
		t.Errorf("the draft is %q, want %q", m.compose.input, want)
	}
	if m.walk.active {
		t.Error("the last word was answered and the walk is still up")
	}
}

// One ctrl+z takes a correction back.
func TestAReplacementIsOneUndoStep(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " mail"
	m := walking(t, b, draft, wrong(draft, typoReceive, "receive"))
	m = openWalk(t, m)
	m, _ = press(t, m, keyText("1"))

	e, ok := m.editorFor(fieldComposer).undo()
	if !ok {
		t.Fatal("there is nothing to undo after a correction")
	}
	if e.text != draft {
		t.Errorf("one undo left %q, want the draft as it was: %q", e.text, draft)
	}
}

// A number with nothing behind it does nothing at all. The list on screen already says
// which numbers exist, so saying it again on the status line would be scolding.
func TestANumberWithNoSuggestionBehindItIsIgnored(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " mail"
	m := walking(t, b, draft, wrong(draft, typoReceive, "receive"))
	m = openWalk(t, m)

	m, _ = press(t, m, keyText("4"))
	if m.compose.input != draft {
		t.Errorf("the draft became %q", m.compose.input)
	}
	if !m.walk.active || m.walk.at != 0 {
		t.Errorf("the walk moved: active=%v at=%d", m.walk.active, m.walk.at)
	}
}

// Skipping leaves the word exactly as it is and goes to the next one. space does it
// too, because that is where a thumb already is.
func TestSkipLeavesTheWordAndMovesOn(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"s", " "} {
		b := &spellBackend{}
		draft := "I " + typoReceive + " " + typoThe + " mail"
		m := walking(t, b, draft,
			wrong(draft, typoReceive, "receive"), wrong(draft, typoThe, "the"))
		m = openWalk(t, m)

		m, _ = press(t, m, keyText(key))
		if m.compose.input != draft {
			t.Errorf("%q changed the draft to %q", key, m.compose.input)
		}
		if m.walk.at != 1 {
			t.Errorf("%q left the walk on %d, want 1", key, m.walk.at)
		}
	}
}

// Teaching a word is the answer to a name no dictionary has heard of.
func TestAddAndIgnoreTeachTheWordAndClearItsUnderlines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		forever bool
	}{
		{"add keeps it after a restart", "a", true},
		{"ignore is for this session", "i", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			b := &spellBackend{}
			draft := "Shtoka and Shtoka again"
			marks := []domain.Misspelling{
				{Word: "Shtoka", Start: 0, End: 6},
				{Word: "Shtoka", Start: 11, End: 17},
			}
			m := walking(t, b, draft, marks...)
			m = openWalk(t, m)

			next, cmd := press(t, m, keyText(c.key))
			m = next
			if cmd == nil {
				t.Fatal("teaching a word asked the backend nothing")
			}
			cmd()
			if got := b.lessons(); len(got) != 1 || got[0] != (learned{word: "Shtoka", forever: c.forever}) {
				t.Errorf("the backend was taught %v, want Shtoka forever=%v", got, c.forever)
			}
			if len(m.spell.shown) != 0 {
				t.Errorf("a taught word is still underlined in %v places", len(m.spell.shown))
			}
		})
	}
}

// esc stops and leaves everything it did not reach underlined. The walk is a way
// through the errors, not a demand that they all be dealt with.
func TestStoppingLeavesTheRestUnderlined(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " " + typoThe + " mail"
	marks := []domain.Misspelling{wrong(draft, typoReceive, "receive"), wrong(draft, typoThe, "the")}
	m := walking(t, b, draft, marks...)
	m = openWalk(t, m)

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.walk.active {
		t.Error("esc did not stop the walk")
	}
	if len(m.spell.shown) != len(marks) {
		t.Errorf("stopping rubbed out %d of the marks", len(marks)-len(m.spell.shown))
	}
	if m.compose.input != draft {
		t.Errorf("stopping changed the draft to %q", m.compose.input)
	}
}

// The walk owns the keyboard while it is up.
func TestTheWalkOwnsTheKeyboard(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " mail"
	m := walking(t, b, draft, wrong(draft, typoReceive, "receive"))
	m = openWalk(t, m)

	for _, key := range []string{"q", "k", "/"} {
		next, cmd := press(t, m, keyText(key))
		if cmd != nil {
			t.Errorf("%q did something while the walk was up", key)
		}
		if !next.walk.active {
			t.Errorf("%q closed the walk", key)
		}
	}
}

// An answer landing mid-walk does not renumber the list.
func TestAnAnswerMidWalkDoesNotRenumberIt(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " " + typoThe + " mail"
	m := walking(t, b, draft,
		wrong(draft, typoReceive, "receive"), wrong(draft, typoThe, "the"))
	m = openWalk(t, m)

	answered, _ := m.handleSpellChecked(spellCheckedMsg{
		gen: m.spell.gen, text: m.spell.text,
		found: []domain.Misspelling{wrong(draft, typoReceive, "receive")},
	})
	m = answered

	if len(m.walk.items) != 2 {
		t.Errorf("the walk is now %d items long; it was 2 when it opened", len(m.walk.items))
	}
	if !strings.Contains(stripStyles(strings.Join(m.spellWalkLines(40), "\n")), "1/2") {
		t.Error("the counter changed under the walk")
	}
}

// Nothing misspelled is not an error and not a popup: it is one line saying there is
// nothing here for you to do.
func TestOpeningWithACleanDraftSaysSo(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := walking(t, b, "all of this is spelled correctly")
	m, _ = chord(t, m, "z", "=")

	if m.walk.active {
		t.Fatal("the walk opened with nothing to walk")
	}
	if !strings.Contains(m.status(), "nothing misspelled") {
		t.Errorf("status says %q", m.status())
	}
}

// A Hebrew word is drawn the way it reads, in the popup as everywhere else.
func TestTheWalkDrawsAnRTLWordTheWayItReads(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "שלום עולמ"
	m := walking(t, b, draft, wrong(draft, "עולמ", "עולם"))
	m = openWalk(t, m)

	drawn := stripStyles(strings.Join(m.spellWalkLines(40), "\n"))
	for _, want := range []string{reverseClusters("עולמ"), reverseClusters("עולם")} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the popup does not draw %q:\n%s", want, drawn)
		}
	}
}

// A word the engine cannot guess at — a name, usually — still gets a row saying so.
// An empty popup would read as one that failed to load.
func TestAWordWithNoSuggestionsSaysThereAreNone(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "Shtoka writes here"
	m := walking(t, b, draft, wrong(draft, "Shtoka"))
	m = openWalk(t, m)

	if drawn := stripStyles(strings.Join(m.spellWalkLines(40), "\n")); !strings.Contains(drawn, "nothing to suggest") {
		t.Errorf("the popup offers no explanation for an empty list:\n%s", drawn)
	}
}

// [spell] check_before_send: the walk opens instead of the message going out, and the
// message goes out when the walk is done.

// sendReady is a model composing a draft whose errors have already been answered for,
// with check_before_send on — the state a send key lands in.
func sendReady(t *testing.T, b *spellBackend, draft string, marks ...domain.Misspelling) Model {
	t.Helper()
	m := spellComposing(t, b)
	m.conf.base.Spell.CheckBeforeSend = true
	m = typeInto(t, m, draft)
	m.spell.shown = marks
	// The answer describes this draft, which is what applied == gen means: nothing is
	// outstanding, so the gate reads what it is holding rather than asking again.
	m.spell.applied = m.spell.gen
	return m
}

// Something misspelled holds the send and opens the walk on it instead.
func TestSendWithAMisspellingOpensTheWalkInstead(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " mail"
	m := sendReady(t, b, draft, wrong(draft, typoReceive, "receive"))

	m, _ = press(t, m, sendKey())
	if !m.walk.active {
		t.Fatalf("the send went out unchecked; status says %q", m.status())
	}
	if !m.walk.sendWhenDone {
		t.Error("the walk does not know it was opened by a send")
	}
	if got := b.posted(); len(got) != 0 {
		t.Errorf("%d message(s) went out while the walk was still open: %v", len(got), got)
	}
	if m.compose.input != draft {
		t.Errorf("the draft became %q", m.compose.input)
	}
}

// Answering the last one sends. That is the whole point of the setting: the correction
// is on the way to the message going out, not a detour from it.
func TestFinishingAWalkOpenedByASendSendsIt(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " mail"
	m := sendReady(t, b, draft, wrong(draft, typoReceive, "receive"))
	m, _ = press(t, m, sendKey())

	m, cmd := press(t, m, keyText("1"))
	if m.walk.active {
		t.Fatal("the walk is still up after its last word was answered")
	}
	// settle rather than one call: sending is a batch — the message, the read receipt,
	// the typing notice — and only running it out reaches the one that posts.
	m = settle(t, m, cmd)
	got := b.posted()
	if len(got) != 1 {
		t.Fatalf("%d messages went out, want 1", len(got))
	}
	if got[0].Body != "I receive mail" {
		t.Errorf("sent %q, want the corrected draft", got[0].Body)
	}
	if m.compose.input != "" {
		t.Errorf("the composer still holds %q after sending", m.compose.input)
	}
}

// Skipping is an answer.
func TestSkippingEverythingStillSends(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " mail"
	m := sendReady(t, b, draft, wrong(draft, typoReceive, "receive"))
	m, _ = press(t, m, sendKey())

	m, cmd := press(t, m, keyText("s"))
	settle(t, m, cmd)
	if got := b.posted(); len(got) != 1 || got[0].Body != draft {
		t.Errorf("sent %v, want the draft as it was written", got)
	}
}

// esc stops the walk *and* the send. Nothing goes out behind you: esc is how you say
// "I will deal with this myself", and the draft is left for you to deal with.
func TestStoppingAWalkOpenedByASendCancelsTheSend(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " mail"
	m := sendReady(t, b, draft, wrong(draft, typoReceive, "receive"))
	m, _ = press(t, m, sendKey())

	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = settle(t, m, cmd)
	if got := b.posted(); len(got) != 0 {
		t.Errorf("esc sent %v", got)
	}
	if m.compose.input != draft {
		t.Errorf("esc changed the draft to %q", m.compose.input)
	}
	if m.walk.active {
		t.Error("esc left the walk open")
	}
}

// A clean draft is not interrupted, and neither is a draft nobody can check. A checker
// that cannot answer must not be able to stop a message going out.
func TestNothingWrongAndNothingToCheckWithBothSendStraightOut(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(m Model) Model
	}{
		{"a clean draft", func(m Model) Model { return m }},
		{"no engine at all", func(m Model) Model { m.spell.off = true; return m }},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			b := &spellBackend{}
			m := c.setup(sendReady(t, b, "nothing wrong here"))
			m, cmd := press(t, m, sendKey())
			if m.walk.active {
				t.Fatal("the walk opened with nothing to walk")
			}
			m = settle(t, m, cmd)
			if got := b.posted(); len(got) != 1 {
				t.Errorf("%d messages went out, want 1", len(got))
			}
		})
	}
}

// Sending inside the debounce — typing a sentence and sending it in the same breath —
// asks the checker now rather than skipping the check.
func TestSendingBeforeTheCheckHasRunAsksForOne(t *testing.T) {
	t.Parallel()

	draft := "I " + typoReceive + " mail"
	b := &spellBackend{found: []domain.Misspelling{wrong(draft, typoReceive, "receive")}}
	m := spellComposing(t, b)
	m.conf.base.Spell.CheckBeforeSend = true
	m = typeInto(t, m, draft)
	// No answer has landed: applied is behind gen, which is what "still typing" is.

	m, cmd := press(t, m, sendKey())
	if m.walk.active {
		t.Fatal("the walk opened on an answer about an older draft")
	}
	if cmd == nil {
		t.Fatal("the send neither went out nor asked anything")
	}
	if got := b.posted(); len(got) != 0 {
		t.Fatalf("the message went out before it was checked: %v", got)
	}
	asked, ok := msgOf[spellBeforeSendMsg](t, cmd)
	if !ok {
		t.Fatal("the send did not ask for a check")
	}
	answered, _ := m.handleSpellBeforeSend(asked)
	m = answered
	if !m.walk.active {
		t.Errorf("the answer came back with an error in it and no walk opened; status %q", m.status())
	}
	if got := b.posted(); len(got) != 0 {
		t.Errorf("it sent anyway: %v", got)
	}
}

// Typing on while the check is in flight is not an answer to the send that started it.
func TestADraftEditedDuringTheCheckIsNotSent(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := sendReady(t, b, "hello there")
	// The check was asked about the draft one keystroke ago; the composer has moved on.
	held := spellBeforeSendMsg{gen: m.spell.gen, text: "hello the"}

	next, _ := m.handleSpellBeforeSend(held)
	m = next
	if got := b.posted(); len(got) != 0 {
		t.Errorf("sent %v for a draft that had moved on", got)
	}
	if !strings.Contains(m.status(), "send again") {
		t.Errorf("status says %q, which does not say the send has to be repeated", m.status())
	}
}

// The walk opens from inside the composer, and the keystroke does not reach the draft on
// its way through.
func TestTheWalkOpensFromInsideTheComposer(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	draft := "I " + typoReceive + " mail"
	m := spellComposing(t, b)
	m = typeInto(t, m, draft)
	m.spell.shown = []domain.Misspelling{wrong(draft, typoReceive, "receive")}

	m, _ = press(t, m, altS())
	if !m.walk.active {
		t.Fatalf("alt+s did not open the walk while composing; status %q", m.status())
	}
	if m.compose.input != draft {
		t.Fatalf("alt+s typed something: %q", m.compose.input)
	}
	// And the numbers correct rather than typing, which is the interception order the
	// composer makes load-bearing: the walk is asked before the field is.
	m, _ = press(t, m, keyText("1"))
	if m.compose.input != "I receive mail" {
		t.Errorf("the draft is %q, want the correction rather than a typed 1", m.compose.input)
	}
}

// allowed records what the walk dismissed rather than taught, which is the whole of the
// observable difference between `a` on a misspelling and `a` on a hint.
func (b *spellBackend) AllowRareWord(_ context.Context, word string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dismissed = append(b.dismissed, word)
	return b.learnErr
}

func (b *spellBackend) dismissals() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.dismissed...)
}

// `a` means two different things, because the two marks are two different claims.
func TestAddOnAHintDismissesItInsteadOfTeachingTheEngine(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	const draft = "שגיא writes here"
	m := walking(t, b, draft, rareHint(draft, "שגיא", "שהיא"))
	m = openWalk(t, m)

	next, cmd := m.handleSpellWalkKey(key('a'))
	m = next
	if cmd != nil {
		cmd()
	}
	if got := b.dismissals(); len(got) != 1 || got[0] != "שגיא" {
		t.Errorf("dismissed %v, want the hinted word", got)
	}
	if got := b.lessons(); len(got) != 0 {
		t.Errorf("the engine was taught %v; it already accepts that word", got)
	}
	// And the mark goes, because nothing else is coming to remove it.
	for _, r := range m.spell.shown {
		if r.Word == "שגיא" {
			t.Error("the hint is still drawn after being dismissed")
		}
	}
}

// `i` on a hint is still the session-long version, and still goes to the engine's
// ignore rather than the allow-list: "not now" and "never" are different answers.
func TestIgnoreOnAHintStaysWithTheEngine(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	const draft = "שגיא writes here"
	m := walking(t, b, draft, rareHint(draft, "שגיא", "שהיא"))
	m = openWalk(t, m)

	_, cmd := m.handleSpellWalkKey(key('i'))
	if cmd != nil {
		cmd()
	}
	if got := b.dismissals(); len(got) != 0 {
		t.Errorf("ignore wrote %v to the allow-list; it is a session answer", got)
	}
	if got := b.lessons(); len(got) != 1 || got[0].forever {
		t.Errorf("lessons = %+v, want one session-long ignore", got)
	}
}

// The footer says what the key will do, and on a hint that is not "add".
func TestTheWalkExplainsAHintAndRenamesTheKey(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	const draft = "שגיא writes here"
	m := walking(t, b, draft, rareHint(draft, "שגיא", "שהיא"))
	m = openWalk(t, m)

	lines := strings.Join(m.spellWalkLines(60), "\n")
	if !strings.Contains(lines, "rarely written") {
		t.Errorf("the walk does not say why the word is there:\n%s", lines)
	}
	if !strings.Contains(lines, "it's fine") {
		t.Errorf("the footer still offers to *add* a word the dictionary has:\n%s", lines)
	}
}

// A hint must not hold up a send unless somebody asked for exactly that.
func TestRareBeforeSendDecidesWhetherAHintGatesTheSend(t *testing.T) {
	t.Parallel()

	const draft = "send this"
	for _, gates := range []bool{false, true} {
		b := &spellBackend{found: []domain.Misspelling{rareHint(draft, "send", "sent")}}
		m := spellComposing(t, b)
		m.conf.base.Spell.CheckBeforeSend = true
		m.conf.base.Spell.RareBeforeSend = gates
		m = typeInto(t, m, draft)

		msg, ok := m.checkBeforeSendCmd(m.spell.gen, draft)().(spellBeforeSendMsg)
		if !ok {
			t.Fatal("the send check answered something else")
		}
		if gates && len(msg.found) != 1 {
			t.Errorf("rare_before_send = true handed the gate %+v, want the hint", msg.found)
		}
		if !gates && len(msg.found) != 0 {
			t.Errorf("rare_before_send = false handed the gate %+v, want nothing", msg.found)
		}
	}
}

// One choice for one word.
func TestCorrectingAWordFixesItsOtherOccurrences(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	const draft = "I " + typoReceive + " mail, you " + typoReceive + " mail, we " + typoReceive + " mail"
	marks := []domain.Misspelling{
		{Word: typoReceive, Start: 2, End: 2 + len(typoReceive), Suggestions: []string{"receive"}},
	}
	at := strings.Index(draft[3:], typoReceive) + 3
	marks = append(marks, domain.Misspelling{
		Word: typoReceive, Start: at, End: at + len(typoReceive), Suggestions: []string{"receive"},
	})
	last := strings.LastIndex(draft, typoReceive)
	marks = append(marks, domain.Misspelling{
		Word: typoReceive, Start: last, End: last + len(typoReceive), Suggestions: []string{"receive"},
	})
	m := walking(t, b, draft, marks...)
	m = openWalk(t, m)

	next, _ := m.takeSuggestion(1)
	m = next

	want := strings.ReplaceAll(draft, typoReceive, "receive")
	if got := m.editorFor(fieldComposer).text; got != want {
		t.Errorf("draft = %q, want every occurrence fixed:\n  %q", got, want)
	}
	// And the walk is over, because there is nothing left to ask about.
	if m.walk.active {
		t.Errorf("the walk is still asking: %+v", m.walk.items)
	}
}

// A word deliberately skipped is not reached back for. Skipping is an answer, and a later
// choice must not overwrite an earlier one.
func TestCorrectingDoesNotReachBackPastASkip(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	const draft = typoReceive + " and " + typoReceive + " and " + typoReceive
	var marks []domain.Misspelling
	for at := 0; at >= 0; {
		i := strings.Index(draft[at:], typoReceive)
		if i < 0 {
			break
		}
		start := at + i
		marks = append(marks, domain.Misspelling{
			Word: typoReceive, Start: start, End: start + len(typoReceive),
			Suggestions: []string{"receive"},
		})
		at = start + len(typoReceive)
	}
	m := walking(t, b, draft, marks...)
	m = openWalk(t, m)

	// Skip the first, then correct the second: the first stays as it was typed, and the
	// third goes with the second.
	skipped, _ := m.handleSpellWalkKey(key('s'))
	m = skipped
	next, _ := m.takeSuggestion(1)
	m = next

	want := typoReceive + " and receive and receive"
	if got := m.editorFor(fieldComposer).text; got != want {
		t.Errorf("draft = %q, want the skipped one left alone:\n  %q", got, want)
	}
}
