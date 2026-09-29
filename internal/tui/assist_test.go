package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// completeBackend answers one fixed ranking and records what it was asked.
type completeBackend struct {
	apitest.Nop
	candidates []domain.WordCandidate
	asked      []domain.CompleteRequest
}

func (b *completeBackend) CompleteWord(_ context.Context, req domain.CompleteRequest) ([]domain.WordCandidate, error) {
	b.asked = append(b.asked, req)
	return b.candidates, nil
}

// completing is a model composing in a room, with completion wired and one answer ready.
func completing(t *testing.T, candidates ...domain.WordCandidate) (Model, *completeBackend) {
	t.Helper()
	b := &completeBackend{candidates: candidates}
	m := update(t, New(context.Background(), b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus = paneTimeline
	m.compose.insertMode = true
	return m.clearStatus(), b
}

// typeAndAnswer types a word and delivers the lookup its pause would have asked for.
func typeAndAnswer(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = typeInto(t, m, text)
	if m.assist.prefix == "" {
		return m
	}
	next, cmd := asModel(m.Update(assistTickMsg{gen: m.assist.gen}))
	m = next
	if cmd == nil {
		t.Fatal("the debounce armed no lookup")
	}
	if msg := cmd(); msg != nil {
		m = update(t, m, msg)
	}
	return m
}

func TestGhostDrawsOnlyForAClearLeader(t *testing.T) {
	t.Parallel()

	m, _ := completing(t, domain.WordCandidate{Word: "coriander", Score: 30}, domain.WordCandidate{Word: "corinthian", Score: 4})
	m = typeAndAnswer(t, m, "cori")
	if got, ok := m.ghost(); !ok || got != "ander" {
		t.Fatalf("ghost() = %q, %v; want the tail of coriander", got, ok)
	}

	// Too close to call: nothing is drawn.
	close, _ := completing(t, domain.WordCandidate{Word: "coriander", Score: 11}, domain.WordCandidate{Word: "corinthian", Score: 10})
	close = typeAndAnswer(t, close, "cori")
	if got, ok := close.ghost(); ok {
		t.Fatalf("ghost() = %q, want nothing while the leader is level", got)
	}
}

// Nothing is asked or drawn before domain.CompleteMinPrefix characters.
func TestGhostWaitsForTheThirdCharacter(t *testing.T) {
	t.Parallel()

	m, b := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	m = typeAndAnswer(t, m, "co")
	if _, ok := m.ghost(); ok {
		t.Fatalf("ghost() drew after two characters; want nothing before %d", domain.CompleteMinPrefix)
	}
	if len(b.asked) != 0 {
		t.Fatalf("asked %d times after two characters, want none", len(b.asked))
	}
	m = typeAndAnswer(t, m, "r")
	if _, ok := m.ghost(); !ok {
		t.Fatal("ghost() drew nothing after three characters")
	}
	if len(b.asked) != 1 || b.asked[0].Prefix != "cor" {
		t.Fatalf("asked %+v, want one lookup for cor", b.asked)
	}
}

// A candidate must save at least two characters, counted in runes.
func TestNothingOfferedForOneCharacterSaved(t *testing.T) {
	t.Parallel()

	m, _ := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	m = typeAndAnswer(t, m, "corianderr")
	if got, ok := m.ghost(); ok {
		t.Fatalf("ghost() = %q past the end of the word, want nothing", got)
	}
	short, _ := completing(t, domain.WordCandidate{Word: "משתמש", Score: 30})
	short = typeAndAnswer(t, short, "משתמ") // one rune saved, four bytes
	if got, ok := short.ghost(); ok {
		t.Fatalf("ghost() = %q for one rune saved, want nothing", got)
	}
}

func TestCompleteAllTakesTheWholeWord(t *testing.T) {
	t.Parallel()

	m, _ := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	m = typeAndAnswer(t, m, "the cori")
	m, _ = press(t, m, keyCode(tea.KeyTab))
	if m.compose.input != "the coriander " {
		t.Fatalf("composer = %q, want the whole word and a space after it", m.compose.input)
	}
	// One undo step.
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if m.compose.input != "the cori" {
		t.Fatalf("after undo = %q, want exactly what was typed", m.compose.input)
	}
}

func TestForwardArrowTakesAWord(t *testing.T) {
	t.Parallel()

	m, _ := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	m = typeAndAnswer(t, m, "cori")
	m, _ = press(t, m, keyCode(tea.KeyRight))
	if m.compose.input != "coriander " {
		t.Fatalf("composer = %q, want the word taken by the forward arrow", m.compose.input)
	}
}

// In a Hebrew word the arrow pointing into the suggestion is the left one; the right
// arrow moves the caret back.
func TestForwardIsTheLeftArrowInHebrew(t *testing.T) {
	t.Parallel()

	m, _ := completing(t, domain.WordCandidate{Word: "משתמשים", Score: 30})
	m = typeAndAnswer(t, m, "משתמ")
	if _, ok := m.ghost(); !ok {
		t.Fatal("no ghost for a Hebrew word")
	}
	left, _ := press(t, m, keyCode(tea.KeyLeft))
	if left.compose.input != "משתמשים " {
		t.Fatalf("composer = %q, want the left arrow to take the word in Hebrew", left.compose.input)
	}
	right, _ := press(t, m, keyCode(tea.KeyRight))
	if right.compose.input != "משתמ" {
		t.Fatalf("composer = %q, want the backward arrow to leave the draft alone", right.compose.input)
	}
	if right.editorFor(fieldComposer).at >= m.editorFor(fieldComposer).at {
		t.Fatal("the backward arrow did not move the caret back")
	}
}

// A Latin word inside a Hebrew message keeps the Latin arrows.
func TestArrowsFollowTheWordNotTheMessage(t *testing.T) {
	t.Parallel()

	m, _ := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	m = typeAndAnswer(t, m, "שלום cori")
	if got, ok := m.ghost(); !ok || got != "ander" {
		t.Fatalf("ghost() = %q, %v; want the English suggestion inside a Hebrew draft", got, ok)
	}
	m, _ = press(t, m, keyCode(tea.KeyRight))
	if m.compose.input != "שלום coriander " {
		t.Fatalf("composer = %q, want the right arrow to take an English word", m.compose.input)
	}
}

func TestAltDigitTakesAChoice(t *testing.T) {
	t.Parallel()

	m, _ := completing(t,
		domain.WordCandidate{Word: "coriander", Score: 11},
		domain.WordCandidate{Word: "corinthian", Score: 10},
		domain.WordCandidate{Word: "coriolis", Score: 9},
	)
	m = typeAndAnswer(t, m, "cor")
	m = typeAndAnswer(t, m, "i")
	if _, ok := m.ghost(); ok {
		t.Fatal("a level field drew a ghost")
	}
	second, _ := press(t, m, tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt})
	if second.compose.input != "corinthian " {
		t.Fatalf("alt+2 gave %q, want the second candidate", second.compose.input)
	}
	// Beyond what is offered, the key is not taken.
	fourth, _ := press(t, m, tea.KeyPressMsg{Code: '4', Mod: tea.ModAlt})
	if fourth.compose.input != "cori" {
		t.Fatalf("alt+4 gave %q, want the draft untouched", fourth.compose.input)
	}
}

// With nothing confident enough to draw, tab opens the chooser.
func TestCompleteAllOpensTheChooserWhenNothingLeads(t *testing.T) {
	t.Parallel()

	m, _ := completing(t,
		domain.WordCandidate{Word: "coriander", Score: 11},
		domain.WordCandidate{Word: "corinthian", Score: 10},
	)
	m = typeAndAnswer(t, m, "cori")
	m, _ = press(t, m, keyCode(tea.KeyTab))
	if !m.completion.isWords() {
		t.Fatalf("tab left completion %+v, want the word chooser open", m.completion)
	}
	if len(m.completion.candidates) != 2 {
		t.Fatalf("chooser has %d rows, want both candidates", len(m.completion.candidates))
	}
	// The ghost stands down while the chooser is up.
	if got := m.ghostTail("cori", 4, 0, 40); got != "" {
		t.Fatalf("ghostTail() = %q while the chooser is open, want nothing", got)
	}
	m, _ = press(t, m, keyCode(tea.KeyTab))
	if m.compose.input != "coriander " {
		t.Fatalf("composer = %q, want the chosen word", m.compose.input)
	}
}

func TestGhostStandsDownWhenItCannotApply(t *testing.T) {
	t.Parallel()

	base, _ := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	base = typeAndAnswer(t, base, "cori")

	cases := []struct {
		name string
		with func(Model) Model
	}{
		{"the caret moves off the end", func(m Model) Model {
			left, _ := press(t, m, keyCode(tea.KeyLeft))
			return left
		}},
		{"a space finishes the word", func(m Model) Model { return typeInto(t, m, " ") }},
		{"insert mode is left", func(m Model) Model {
			m.compose.insertMode = false
			return m
		}},
		{"the correction walk opens", func(m Model) Model {
			m.walk.active = true
			return m
		}},
		{"the setting is off", func(m Model) Model {
			off := false
			m.conf.base.Complete.Ghost = &off
			return m
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, ok := tc.with(base).ghost(); ok {
				t.Fatalf("ghost() = %q, want nothing", got)
			}
		})
	}
}

// The lookup carries the open room and the configured scope.
func TestCompletionLookupCarriesItsScope(t *testing.T) {
	t.Parallel()

	m, b := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	m.conf.base.Complete.Scope = config.CompleteScopeSpace
	m = typeAndAnswer(t, m, "cori")
	if len(b.asked) != 1 {
		t.Fatalf("asked %d times, want one", len(b.asked))
	}
	req := b.asked[0]
	if req.Scope != config.CompleteScopeSpace {
		t.Errorf("scope = %q, want %q", req.Scope, config.CompleteScopeSpace)
	}
	if len(req.RoomIDs) != 1 || req.RoomIDs[0] != "!a:x" {
		t.Errorf("rooms = %+v, want the open room", req.RoomIDs)
	}
	if req.Limit < domain.CompleteChoices {
		t.Errorf("limit = %d, want at least the %d that can be shown", req.Limit, domain.CompleteChoices)
	}
}

// A stale answer must never be drawn.
func TestStaleAnswersAreDropped(t *testing.T) {
	t.Parallel()

	m, b := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	m = typeInto(t, m, "cori")
	stale := m.assist.gen
	m = typeInto(t, m, "a") // "coria" — a new prefix, a new generation
	m = update(t, m, assistDoneMsg{gen: stale, prefix: "cori", typed: "cori", candidates: b.candidates})
	if got, ok := m.ghost(); ok {
		t.Fatalf("ghost() = %q from a stale answer, want nothing", got)
	}
}

// The ghost is clipped to the room left on the row, and only drawn at the caret's end.
func TestGhostTailFitsTheRow(t *testing.T) {
	t.Parallel()

	m, _ := completing(t, domain.WordCandidate{Word: "coriander", Score: 30})
	m = typeAndAnswer(t, m, "cori")

	drawn := m.ghostTail("cori", 4, 0, 40)
	if !strings.Contains(drawn, "ander") {
		t.Fatalf("ghostTail() = %q, want the tail in it", drawn)
	}
	tight := m.ghostTail("cori", 4, 0, 6)
	if !strings.Contains(tight, "an") || strings.Contains(tight, "ander") {
		t.Fatalf("ghostTail(width 6) = %q, want it cut to the room left", tight)
	}
	if none := m.ghostTail("cori", 4, 0, 4); none != "" {
		t.Fatalf("ghostTail(no room) = %q, want nothing", none)
	}
	if mid := m.ghostTail("coriander", 4, 0, 40); mid != "" {
		t.Fatalf("ghostTail(mid-row) = %q, want nothing", mid)
	}
}

// modelBackend answers one fixed model result and records what it was asked.
type modelBackend struct {
	apitest.Nop
	result domain.ModelResult
	asked  []domain.ModelRequest
}

func (b *modelBackend) ModelTask(_ context.Context, req domain.ModelRequest) (domain.ModelResult, error) {
	b.asked = append(b.asked, req)
	return b.result, nil
}

// modeling is a model composing in a room with the model layer configured.
func modeling(t *testing.T, result domain.ModelResult) (Model, *modelBackend) {
	t.Helper()
	b := &modelBackend{result: result}
	m := update(t, New(context.Background(), b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m.focus = paneTimeline
	m.compose.insertMode = true
	m.conf.base.Complete.Model.Model = "test-model"
	// The assist endpoint gates `/summary` and `:todo`.
	m.conf.base.Assist.Endpoint = "https://example.invalid/v1/chat/completions"
	m.conf.base.Assist.Model = "test-model"
	return m.clearStatus(), b
}

// typeAndAsk types a draft and delivers the ask its pause would have made.
func typeAndAsk(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = typeInto(t, m, text)
	if m.model.text == "" {
		return m
	}
	next, cmd := asModel(m.Update(modelTickMsg{gen: m.model.gen}))
	m = next
	if cmd == nil {
		t.Fatal("the model debounce armed no ask")
	}
	if msg := cmd(); msg != nil {
		m = update(t, m, msg)
	}
	return m
}

// The model is asked only after the space that finishes a word.
func TestModelIsAskedAtWordBoundariesOnly(t *testing.T) {
	t.Parallel()

	m, b := modeling(t, domain.ModelResult{Text: "and see what happens"})
	m = typeAndAsk(t, m, "let us try")
	if len(b.asked) != 0 {
		t.Fatalf("asked %+v mid-word, want nothing until the word is finished", b.asked)
	}
	m = typeAndAsk(t, m, " ")
	if len(b.asked) != 1 {
		t.Fatalf("asked %d times after a space, want once", len(b.asked))
	}
	if b.asked[0].Task != domain.ModelComplete || b.asked[0].Draft != "let us try " {
		t.Fatalf("asked %+v, want the whole draft under the complete task", b.asked[0])
	}
	if got, ok := m.ghost(); !ok || got != "and see what happens" {
		t.Fatalf("ghost() = %q, %v; want the model's continuation", got, ok)
	}
}

func TestModelGhostKeysTakeAWordOrAllOfIt(t *testing.T) {
	t.Parallel()

	word, _ := modeling(t, domain.ModelResult{Text: "and see what happens"})
	word = typeAndAsk(t, word, "let us try ")
	word, _ = press(t, word, keyCode(tea.KeyRight))
	if word.compose.input != "let us try and " {
		t.Fatalf("composer = %q, want one word of the continuation", word.compose.input)
	}

	all, _ := modeling(t, domain.ModelResult{Text: "and see what happens"})
	all = typeAndAsk(t, all, "let us try ")
	all, _ = press(t, all, keyCode(tea.KeyTab))
	if all.compose.input != "let us try and see what happens " {
		t.Fatalf("composer = %q, want the whole continuation", all.compose.input)
	}
	all, _ = press(t, all, tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if all.compose.input != "let us try " {
		t.Fatalf("after undo = %q, want the draft as typed", all.compose.input)
	}
}

// A refusal stays off the status line; the marker says the room is not live.
func TestModelRefusalIsShownAsNotLive(t *testing.T) {
	t.Parallel()

	m, _ := modeling(t, domain.ModelResult{Refusal: domain.ModelRefusedRoom})
	m = typeAndAsk(t, m, "let us try ")
	if _, ok := m.ghost(); ok {
		t.Fatal("a refused ask produced a ghost")
	}
	if got := m.modelMarker(); got != "⌂ not ready" {
		t.Fatalf("modelMarker() = %q, want it to say the model is not answering", got)
	}
	if m.status() != "" {
		t.Fatalf("status = %q, want a refusal to stay off the status line", m.status())
	}

	live, _ := modeling(t, domain.ModelResult{Text: "and so on", Model: "test-model"})
	live = typeAndAsk(t, live, "let us try ")
	if got := live.modelMarker(); got != "⌂ test-model" {
		t.Fatalf("modelMarker() = %q, want the model that is answering", got)
	}

	// Nothing configured, no marker.
	off, _ := modeling(t, domain.ModelResult{})
	no := false
	off.conf.base.Complete.Model.Enabled = &no
	if got := off.modelMarker(); got != "" {
		t.Fatalf("modelMarker() = %q with nothing configured, want nothing", got)
	}
}

// alt+m shows a dry run of what would be sent.
func TestModelPreviewShowsTheRequest(t *testing.T) {
	t.Parallel()

	m, b := modeling(t, domain.ModelResult{
		Text:     "system:\nfinish sentences\n\nuser:\ncontext:\nDana: hello\n\ndraft:\nlet us try ",
		Endpoint: "https://example.invalid/v1/chat/completions",
		Model:    "test-model",
	})
	m = typeInto(t, m, "let us try ")
	next, cmd := asModel(m.Update(tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt}))
	m = next
	if cmd == nil {
		t.Fatal("alt+m asked for nothing")
	}
	m = update(t, m, cmd())
	if !m.reader.showing(readerAsk) {
		t.Fatal("the preview overlay did not open")
	}
	if len(b.asked) != 1 || !b.asked[0].DryRun {
		t.Fatalf("asked %+v, want exactly one dry run", b.asked)
	}
	body := strings.Join(m.modelPreviewLines(), "\n")
	for _, want := range []string{"example.invalid", "test-model", "finish sentences", "let us try"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview = %q, want %q in it", body, want)
		}
	}
}

// The phrase ghost: what this room usually says next, from its own history.
func TestPhraseGhostComesFromTheRoomsOwnHistory(t *testing.T) {
	t.Parallel()

	m, _ := completing(t)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@dana:x", Body: "let us go north"},
		{ID: "$2", RoomID: "!a:x", Sender: "@dana:x", Body: "let us go north"},
	}}})
	m = typeInto(t, m, "let us go ")
	if got, ok := m.ghost(); !ok || got != "north " {
		t.Fatalf("ghost() = %q, %v; want the room's own continuation", got, ok)
	}
	taken, _ := press(t, m, keyCode(tea.KeyTab))
	if taken.compose.input != "let us go north " {
		t.Fatalf("composer = %q, want the continuation taken", taken.compose.input)
	}

	// No clear leader, nothing drawn.
	split, _ := completing(t)
	split = update(t, split, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@dana:x", Body: "let us go north"},
		{ID: "$2", RoomID: "!a:x", Sender: "@dana:x", Body: "let us go south"},
	}}})
	split = typeInto(t, split, "let us go ")
	if got, ok := split.ghost(); ok {
		t.Fatalf("ghost() = %q on a split field, want nothing", got)
	}
}

// After a space the live model outranks the local phrase index.
func TestModelOutranksTheLocalPhrase(t *testing.T) {
	t.Parallel()

	m, _ := modeling(t, domain.ModelResult{Text: "east", Model: "test-model"})
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@dana:x", Body: "let us go north"},
		{ID: "$2", RoomID: "!a:x", Sender: "@dana:x", Body: "let us go north"},
	}}})
	m = typeAndAsk(t, m, "let us go ")
	if got, ok := m.ghost(); !ok || got != "east" {
		t.Fatalf("ghost() = %q, %v; want the model's answer ahead of the local one", got, ok)
	}

	// Refused: the local index answers instead.
	refused, _ := modeling(t, domain.ModelResult{Refusal: domain.ModelRefusedRoom})
	refused = update(t, refused, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@dana:x", Body: "let us go north"},
		{ID: "$2", RoomID: "!a:x", Sender: "@dana:x", Body: "let us go north"},
	}}})
	refused = typeAndAsk(t, refused, "let us go ")
	if got, ok := refused.ghost(); !ok || got != "north " {
		t.Fatalf("ghost() = %q, %v; want the local continuation when the model refuses", got, ok)
	}
}

// /summary opens a reader and leaves the composer empty.
func TestSummaryOpensAReaderAndSendsNothing(t *testing.T) {
	t.Parallel()

	m, b := modeling(t, domain.ModelResult{Text: "• Dana asked you for the figures\n• the deploy is done"})
	m = typeInto(t, m, "/summary")
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	if m.compose.input != "" {
		t.Fatalf("composer = %q, want it cleared", m.compose.input)
	}
	if cmd == nil {
		t.Fatal("/summary asked for nothing")
	}
	m = deliver(t, m, cmd)
	if !m.reader.showing(readerSummary) {
		t.Fatalf("reader = %v, want the summary overlay", m.reader)
	}
	if got := strings.Join(m.summaryLines(), "\n"); !strings.Contains(got, "Dana asked you") {
		t.Fatalf("summary = %q, want the model's answer", got)
	}
	if len(b.asked) != 1 || b.asked[0].Task != domain.ModelSummary {
		t.Fatalf("asked %+v, want one summary task", b.asked)
	}
	// No draft, and in particular not the command text.
	if b.asked[0].Draft != "" {
		t.Fatalf("summary carried a draft %q, want none", b.asked[0].Draft)
	}
	if b.asked[0].RoomID != "!a:x" {
		t.Fatalf("summary asked about %q, want the open room", b.asked[0].RoomID)
	}
}

// A /summary refusal reaches the status line; with no endpoint nothing is asked.
func TestSummaryReportsWhyThereIsNone(t *testing.T) {
	t.Parallel()

	m, _ := modeling(t, domain.ModelResult{Refusal: domain.ModelRefusedEncrypted})
	m = typeInto(t, m, "/summary")
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	m = deliver(t, m, cmd)
	if m.reader.showing(readerSummary) {
		t.Fatal("a refused summary opened an overlay")
	}
	if !strings.Contains(m.status(), domain.ModelRefusedEncrypted) {
		t.Fatalf("status = %q, want the refusal in it", m.status())
	}

	off, b := modeling(t, domain.ModelResult{})
	off.conf.base.Assist.Endpoint = ""
	off = typeInto(t, off, "/summary")
	off, _ = press(t, off, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	if len(b.asked) != 0 {
		t.Fatalf("asked %+v with no endpoint, want nothing", b.asked)
	}
	if !strings.Contains(off.status(), "no model endpoint") {
		t.Fatalf("status = %q, want it to say the layer is off", off.status())
	}
}

// Regression: a Hebrew summary was drawn backwards. Reader lines are reordered for
// display and flushed right; English and pre-styled lines are untouched.
func TestSummaryOverlayOrdersRightToLeftText(t *testing.T) {
	t.Parallel()

	m, _ := modeling(t, domain.ModelResult{})
	m = sized(t, m)
	const hebrew = "הודעה על מניות"
	width := m.readerWidth()

	got := m.readerLines([]string{hebrew}, width, nil)
	if len(got) != 1 {
		t.Fatalf("readerLines() = %v, want one row", got)
	}
	if strings.HasSuffix(got[0], hebrew) && !strings.HasPrefix(strings.TrimSpace(got[0]), string([]rune(hebrew)[len([]rune(hebrew))-1])) {
		t.Fatalf("readerLines() = %q, want the line reordered for display", got[0])
	}
	if !strings.HasPrefix(got[0], " ") {
		t.Fatalf("readerLines() = %q, want a right-to-left line padded to the right edge", got[0])
	}
	if ansi.StringWidth(got[0]) != width {
		t.Errorf("width = %d, want the full measure %d", ansi.StringWidth(got[0]), width)
	}

	plain := m.readerLines([]string{"a plain english line"}, width, nil)
	if len(plain) != 1 || plain[0] != "a plain english line" {
		t.Fatalf("readerLines(english) = %v, want it untouched", plain)
	}

	styled := m.theme.Muted.Render("31 messages from 2h")
	if got := m.readerLines([]string{styled}, width, nil); got[0] != styled {
		t.Fatalf("readerLines(styled) = %q, want it untouched", got[0])
	}

	// Long lines wrap before they are reordered (per display row).
	long := strings.Repeat(hebrew+" ", 20)
	if rows := m.readerLines([]string{long}, width, nil); len(rows) < 2 {
		t.Fatalf("a %d-cell line produced %d rows at width %d, want it wrapped",
			ansi.StringWidth(long), len(rows), width)
	} else {
		for i, row := range rows {
			if w := ansi.StringWidth(row); w > width {
				t.Fatalf("row %d is %d cells wide, want at most %d", i, w, width)
			}
		}
	}
	if got := m.readerLines([]string{""}, width, nil); len(got) != 1 || got[0] != "" {
		t.Fatalf("readerLines(empty) = %v, want one empty row", got)
	}
}

// A summary paints speakers' names in their colors in this room.
func TestSummaryPaintsNamesInTheRoomsColors(t *testing.T) {
	t.Parallel()

	m, _ := modeling(t, domain.ModelResult{})
	m = sized(t, m)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bar:x", SenderName: "Sam Cohen", Body: "hello"},
		{ID: "$2", RoomID: "!a:x", Sender: "@noa:x", SenderName: "Noa Katz", Body: "hi"},
	}}})

	palette := m.roomPalette()
	// Full and given names both, since a model shortens after the first mention.
	for _, name := range []string{"Sam Cohen", "Sam", "Noa Katz", "Noa"} {
		if _, ok := palette[name]; !ok {
			t.Fatalf("roomPalette() = %v, want %q in it", palette, name)
		}
	}
	if palette["Sam"] != palette["Sam Cohen"] {
		t.Error("a given name has a different color from the full name")
	}
	paint := paintNames(palette)
	if paint == nil {
		t.Fatal("no painter for a room with speakers in it")
	}
	got := paint("- Sam Cohen — confirm receipt")
	if !strings.ContainsRune(got, escapeByte) {
		t.Fatalf("paint() = %q, want the name styled", got)
	}
	if plain := paint("- the deploy is done"); plain != "- the deploy is done" {
		t.Errorf("paint() = %q, want a line naming nobody left alone", plain)
	}
	if paintNames(nil) != nil {
		t.Error("an empty palette produced a painter")
	}
}

// Models write Markdown emphasis anyway; it is stripped.
func TestSummaryStripsEmphasisMarkers(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"**Waiting on you:**":      "Waiting on you:",
		"__Also discussed:__":      "Also discussed:",
		"- **Sam** asked for this": "- Sam asked for this",
		"nothing to strip":         "nothing to strip",
	} {
		if got := plainText(in); got != want {
			t.Errorf("plainText(%q) = %q, want %q", in, got, want)
		}
	}

	m, _ := modeling(t, domain.ModelResult{})
	m.model.summary = "**Waiting on you:**\n- Sam — reply"
	if got := strings.Join(m.summaryLines(), "\n"); strings.Contains(got, "**") {
		t.Fatalf("summaryLines() = %q, want the markers gone", got)
	}
}

// A given name shared by two people is left unpainted.
func TestPaletteSkipsAmbiguousGivenNames(t *testing.T) {
	t.Parallel()

	m, _ := modeling(t, domain.ModelResult{})
	m = sized(t, m)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bar1:x", SenderName: "Sam Cohen", Body: "one"},
		{ID: "$2", RoomID: "!a:x", Sender: "@bar2:x", SenderName: "Sam Levi", Body: "two"},
		{ID: "$3", RoomID: "!a:x", Sender: "@noa:x", SenderName: "Noa Katz", Body: "three"},
	}}})

	palette := m.roomPalette()
	if _, painted := palette["Sam"]; painted {
		t.Error("an ambiguous given name was given a color")
	}
	for _, name := range []string{"Sam Cohen", "Sam Levi", "Noa", "Noa Katz"} {
		if _, ok := palette[name]; !ok {
			t.Errorf("roomPalette() is missing %q", name)
		}
	}
}

// Painting must not nest "Sam" inside an already painted "Sam Cohen".
func TestPaintDoesNotNestStyles(t *testing.T) {
	t.Parallel()

	m, _ := modeling(t, domain.ModelResult{})
	m = sized(t, m)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bar:x", SenderName: "Sam Cohen", Body: "hello"},
	}}})
	paint := paintNames(m.roomPalette())

	got := paint("- Sam Cohen asked Sam Cohen about Sam")
	if opened := strings.Count(got, "\x1b[38"); opened != 3 {
		t.Fatalf("paint() opened %d styles in %q, want 3", opened, got)
	}
	if stripped := ansi.Strip(got); stripped != "- Sam Cohen asked Sam Cohen about Sam" {
		t.Fatalf("paint() changed the text to %q", stripped)
	}
}

// An empty "Waiting on you" section is hidden, however "nothing" is spelled.
func TestEmptySectionsAreHidden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"the answer on the heading's own line",
			[]string{"Waiting on you: none", "", "Also discussed:", "Sam:", "  - the deploy"},
			[]string{"Also discussed:", "Sam:", "  - the deploy"},
		},
		{
			"another word for nothing",
			[]string{"Waiting on you: nothing", "Also discussed:", "Sam:"},
			[]string{"Also discussed:", "Sam:"},
		},
		{
			"a dash in front of it",
			[]string{"Waiting on you: - none", "Also discussed:", "Sam:"},
			[]string{"Also discussed:", "Sam:"},
		},
		{
			"a heading with nothing under it at all",
			[]string{"Waiting on you:", "", "Also discussed:", "Sam:"},
			[]string{"Also discussed:", "Sam:"},
		},
		{
			"something is waiting",
			[]string{"Waiting on you:", "- Sam — review the reply", "", "Also discussed:", "Sam:"},
			[]string{"Waiting on you:", "- Sam — review the reply", "", "Also discussed:", "Sam:"},
		},
		{
			"a person is not a section",
			[]string{"Also discussed:", "Sam Cohen:", "  - nothing much"},
			[]string{"Also discussed:", "Sam Cohen:", "  - nothing much"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := dropEmptySections(tc.in)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("dropEmptySections() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// A wrapped sub-item keeps its indent on continuation rows.
func TestWrappedSubItemsStayIndented(t *testing.T) {
	t.Parallel()

	m, _ := modeling(t, domain.ModelResult{})
	m = sized(t, m)
	width := m.readerWidth()
	long := "  - " + strings.Repeat("a long topic that certainly wraps ", 4)

	rows := m.readerLines([]string{long}, width, nil)
	if len(rows) < 2 {
		t.Fatalf("a %d-cell line produced %d rows, want it wrapped", len(long), len(rows))
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, "  ") {
			t.Fatalf("row %d = %q, want it under the item's indent", i, row)
		}
		if w := ansi.StringWidth(row); w > width {
			t.Fatalf("row %d is %d cells, want at most %d", i, w, width)
		}
	}
}
