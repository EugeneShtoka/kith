package tui

import (
	"context"
	"image/color"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Completion in the composer: the word being typed (from the daemon's vocabulary),
// the next word at a boundary (the local model, then the room's own phrase index),
// and the keys that take them. A ghost is drawn only when the leader leads clearly
// (domain.Dominant). Nothing is suggested while the spelling walk is open.

// assistDebounce is how long the composer waits before asking for a word completion.
const assistDebounce = 120 * time.Millisecond

// assistTimeout bounds one lookup.
const assistTimeout = 3 * time.Second

// assistCandidates is how many to fetch: more than are shown, because the dominance
// test reads the runners-up.
const assistCandidates = 6

// assistState is what the composer knows about finishing the word in progress.
type assistState struct {
	// prefix is the partly typed word, folded like the vocabulary; empty means nothing
	// in flight. typed is the same prefix as written, used to re-case candidates.
	prefix     string
	typed      string
	candidates []domain.WordCandidate
	// gen counts prefixes so a stale tick or answer is dropped.
	gen     int
	applied int
}

// assistTickMsg is the debounce firing for one prefix.
type assistTickMsg struct{ gen int }

// assistDoneMsg is the daemon's answer, and the prefix it answers about.
type assistDoneMsg struct {
	gen        int
	prefix     string
	typed      string
	candidates []domain.WordCandidate
}

// assistTickCmd fires the completion debounce.
func assistTickCmd(gen int) tea.Cmd {
	return tea.Tick(assistDebounce, func(time.Time) tea.Msg { return assistTickMsg{gen: gen} })
}

// armCompletion notices the word under the caret has changed and schedules a lookup.
// Called from Update after every message, since many paths change the composer.
func (m Model) armCompletion() (Model, tea.Cmd) {
	if !m.conf.base.Complete.CompleteEnabled() || m.openRoom == "" {
		return m, nil
	}
	typed, ok := m.wordBeingTyped()
	if !ok {
		// Drop the state so the ghost disappears immediately.
		m.assist = assistState{}
		return m, nil
	}
	prefix := strings.ToLower(typed)
	if prefix == m.assist.prefix {
		return m, nil
	}
	m.assist.prefix, m.assist.typed = prefix, typed
	// The old candidates answer a different word.
	m.assist.candidates = nil
	m.assist.gen++
	return m, assistTickCmd(m.assist.gen)
}

// handleAssistTick asks, if the word it was armed for is still the word.
func (m Model) handleAssistTick(msg assistTickMsg) (Model, tea.Cmd) {
	if msg.gen != m.assist.gen || m.assist.prefix == "" {
		return m, nil
	}
	return m, m.completeWordCmd(msg.gen, m.assist.prefix, m.assist.typed)
}

// handleAssistDone files an answer, unless a newer one is already in.
func (m Model) handleAssistDone(msg assistDoneMsg) (Model, tea.Cmd) {
	if msg.gen != m.assist.gen || msg.gen <= m.assist.applied || msg.prefix != m.assist.prefix {
		return m, nil
	}
	m.assist.applied = msg.gen
	m.assist.candidates = msg.candidates
	// A popup opened before the answer landed is waiting for exactly this.
	if m.completion.active && m.completion.trigger == wordTrigger {
		return m.refreshCandidates(), nil
	}
	return m, nil
}

// completeWordCmd asks the daemon to finish the word.
func (m Model) completeWordCmd(gen int, prefix, typed string) tea.Cmd {
	room := m.openRoom
	req := domain.CompleteRequest{
		Prefix:     prefix,
		RoomIDs:    []domain.RoomID{room},
		SpaceRooms: m.spaceRoomsFor(room),
		Scope:      m.conf.base.Complete.ScopeOrDefault(),
		Sources:    m.conf.base.Complete.Sources,
		Limit:      assistCandidates,
	}
	parent, backend := m.ctx, m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, assistTimeout)
		defer cancel()
		candidates, err := backend.CompleteWord(ctx, req)
		if err != nil {
			// Unrequested, so a failure is not news.
			return nil
		}
		return assistDoneMsg{gen: gen, prefix: prefix, typed: typed, candidates: candidates}
	}
}

// wordBeingTyped is the word the caret is at the end of, and false when there is none
// worth finishing: not composing, caret not at the end of the draft, shorter than
// domain.CompleteMinPrefix, or the walk or another popup owns the keys.
func (m Model) wordBeingTyped() (string, bool) {
	if !m.compose.insertMode || m.walk.active || m.compose.reacting {
		return "", false
	}
	// Another popup (name, emoji, room, command) is narrowing its own list.
	if m.completion.active && !m.completion.isWords() {
		return "", false
	}
	text := m.compose.input
	at := m.editorFor(fieldComposer).at
	if at != len(text) || text == "" {
		return "", false
	}
	word := trailingWord(text)
	if len([]rune(word)) < domain.CompleteMinPrefix {
		return "", false
	}
	return word, true
}

// trailingWord is the run of letters at the end of a string. Digits and apostrophes
// end the run, matching the vocabulary's tokenizer.
func trailingWord(text string) string {
	for i := len(text); i > 0; {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		if !unicode.IsLetter(r) {
			return text[i:]
		}
		i -= size
	}
	return text
}

// ghost is the suggestion to draw after the caret. `[complete] ghost` switches inline
// suggestions off; the popup is unaffected.
func (m Model) ghost() (string, bool) {
	if !m.conf.base.Complete.GhostEnabled() {
		return "", false
	}
	if word, ok := m.assistLead(); ok {
		return strings.TrimPrefix(word, m.assist.typed), true
	}
	// At a boundary: the model first, then the room's phrase index.
	if phrase, ok := m.modelGhost(); ok {
		return phrase, true
	}
	return m.phraseGhost()
}

// assistLead is the whole leading word when it leads clearly enough to be offered
// without being asked for.
func (m Model) assistLead() (string, bool) {
	typed, ok := m.wordBeingTyped()
	if !ok || typed != m.assist.typed {
		return "", false
	}
	lead, ok := domain.Dominant(m.offered(typed), m.conf.base.Complete.RatioOrDefault())
	if !ok {
		return "", false
	}
	return lead.Word, true
}

// wordTrigger is word completion's empty trigger: the popup is opened by a key over a
// word already being written.
const wordTrigger = ""

// isWords reports whether this popup is the word chooser (the other triggerless
// source is the command line, in the prompt).
func (c completionState) isWords() bool {
	return c.active && c.trigger == wordTrigger && c.target != targetPrompt
}

// wordCandidates are the completions for the word being typed, as popup rows.
func (m Model) wordCandidates(query string) []candidate {
	offered := m.offered(query)
	out := make([]candidate, 0, len(offered))
	for _, c := range offered {
		out = append(out, candidate{text: c.Word, label: c.Word, spaceAfter: true})
	}
	return out
}

// offered is the answer applied to what was typed: re-cased, dropping words that do
// not extend it, capped at domain.CompleteChoices. The ghost, the chooser and the
// number keys all read this one list so they agree on positions.
func (m Model) offered(typed string) []domain.WordCandidate {
	out := make([]domain.WordCandidate, 0, domain.CompleteChoices)
	for _, c := range m.assist.candidates {
		word := domain.RecaseLike(typed, c.Word)
		if !strings.HasPrefix(word, typed) || !domain.LongEnough(typed, word) {
			continue
		}
		out = append(out, domain.WordCandidate{Word: word, Score: c.Score})
		if len(out) == domain.CompleteChoices {
			break
		}
	}
	return out
}

// openWordCompletion opens the chooser over the word being typed from the
// candidates already fetched; if the answer is still in flight it fills on arrival.
func (m Model) openWordCompletion() (Model, tea.Cmd) {
	typed, ok := m.wordBeingTyped()
	if !ok {
		return m, nil
	}
	ed := m.editorFor(fieldComposer)
	return m.openCompletion(targetComposer, wordTrigger, ed.at-len(typed))
}

// assistKey handles the completion keys while composing, and reports whether it took
// the key; with nothing on offer the keys keep their ordinary meanings.
func (m Model) assistKey(key tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	act := m.keys.lookup(key.String(), scopeInsert)
	if choice, ok := choiceOf(act); ok {
		return m.takeChoice(choice)
	}
	switch act {
	case actComplete:
		if word, ok := m.assistLead(); ok {
			// One word, so the model can be asked again with it as context.
			return answered(m.takeWord(word))
		}
		if phrase, ok := m.modelGhost(); ok {
			return answered(m.takeFirstWord(phrase))
		}
		if phrase, ok := m.phraseGhost(); ok {
			return answered(m.takePhrase(phrase))
		}
		return m, nil, false
	case actCompleteAll:
		if word, ok := m.assistLead(); ok {
			return answered(m.takeWord(word))
		}
		if phrase, ok := m.modelGhost(); ok {
			return answered(m.takePhrase(phrase))
		}
		if phrase, ok := m.phraseGhost(); ok {
			return answered(m.takePhrase(phrase))
		}
		// Nothing confident enough to draw: escalate to the list.
		if typed, ok := m.wordBeingTyped(); ok && len(m.offered(typed)) > 0 {
			return answered(m.openWordCompletion())
		}
		return m, nil, false
	}
	return m, nil, false
}

// takeChoice inserts the n-th thing on offer. While a word is being typed that is the
// vocabulary's list; at a boundary it is the model/phrase options.
func (m Model) takeChoice(n int) (Model, tea.Cmd, bool) {
	if typed, ok := m.wordBeingTyped(); ok && typed == m.assist.typed {
		offered := m.offered(typed)
		if n >= len(offered) {
			return m, nil, false
		}
		return answered(m.takeWord(offered[n].Word))
	}
	options := m.modelOptions()
	if n >= len(options) {
		return m, nil, false
	}
	return answered(m.takePhrase(options[n]))
}

// modelOptions are the next-word options for the draft: this room's phrase index
// first (instant, offline), then the model's, deduplicated. Before the model answers
// for this exact draft, the phrase index fills all but one slot.
func (m Model) modelOptions() []string {
	draft, ok := m.draftForModel()
	if !ok {
		return nil
	}
	if !sameAsk(draft, m.model.text) {
		return m.phraseOptions(draft, domain.MaxCompletionOptions-1)
	}
	return merge(m.model.options, m.phraseOptions(draft, ladderSlots))
}

// merge is the two sources in order, case-insensitively deduplicated, up to the
// strip's width.
func merge(first, second []string) []string {
	out := make([]string, 0, domain.MaxCompletionOptions)
	seen := make(map[string]bool, domain.MaxCompletionOptions)
	for _, source := range [][]string{first, second} {
		for _, option := range source {
			if len(out) >= domain.MaxCompletionOptions {
				return out
			}
			if key := strings.ToLower(option); !seen[key] {
				seen[key] = true
				out = append(out, option)
			}
		}
	}
	return out
}

// ladderSlots is how many of the five slots the local phrase index keeps once the
// model has answered: measured, one beats both zero and two (the room's own jargon
// is what a general model cannot know).
const ladderSlots = 1

// phraseOptions is what this room and account have said next before, up to n.
func (m Model) phraseOptions(draft string, n int) []string {
	return m.phrases.phrases().NextWords(draft, n)
}

// takeWord replaces the word being typed with the completed one, as one undo step.
func (m Model) takeWord(word string) (Model, tea.Cmd) {
	typed, ok := m.wordBeingTyped()
	if !ok || !strings.HasPrefix(word, typed) {
		return m, nil
	}
	ed := m.editorFor(fieldComposer)
	start := ed.at - len(typed)
	if start < 0 {
		return m, nil
	}
	// A trailing space, as a completed name gets.
	head := ed.text[:start] + word + " "
	m = m.store(fieldComposer, ed.changed(head+ed.after(), len(head), editWhole))
	// Clear now so the ghost does not flash back over the finished word.
	m.assist.candidates = nil
	m = m.closeCompletion()
	return m.composerTyped("")
}

// takePhrase inserts a suggestion at the caret, as one undo step. It adds to a whole
// draft rather than replacing a part-typed word.
func (m Model) takePhrase(text string) (Model, tea.Cmd) {
	draft, ok := m.draftForModel()
	if !ok || text == "" {
		return m, nil
	}
	ed := m.editorFor(fieldComposer)
	// A trailing space unless the suggestion ends in punctuation.
	head := draft + text
	if last, _ := utf8.DecodeLastRuneInString(text); unicode.IsLetter(last) || unicode.IsDigit(last) {
		head += " "
	}
	m = m.store(fieldComposer, ed.changed(head+ed.after(), len(head), editWhole))
	m.model.suggestion, m.model.options, m.model.text = "", nil, ""
	return m.composerTyped("")
}

// takeFirstWord inserts only the first word of a suggestion, so the next ask can
// continue from what was accepted.
func (m Model) takeFirstWord(text string) (Model, tea.Cmd) {
	first := text
	if cut := strings.IndexFunc(text, unicode.IsSpace); cut > 0 {
		first = text[:cut+1]
	}
	return m.takePhrase(first)
}

// swapArrows turns the arrow keys around when the caret is in right-to-left text, so
// the key pointing into a suggestion means "forward" in both directions. It is
// decided by the word at the caret, not the paragraph (Hebrew drafts often embed
// Latin). Only the arrows swap; backspace/delete/home/end are logical, and pane
// movement never mirrors.
func swapArrows(key tea.KeyPressMsg, text string, at int) tea.KeyPressMsg {
	if !caretInRTL(text, at) {
		return key
	}
	switch key.Code {
	case tea.KeyLeft:
		key.Code = tea.KeyRight
	case tea.KeyRight:
		key.Code = tea.KeyLeft
	}
	return key
}

// caretInRTL reports whether the last strong character before the caret is
// right-to-left. Neutral characters are skipped, as bidi does.
func caretInRTL(text string, at int) bool {
	if at > len(text) {
		at = len(text)
	}
	for i := at; i > 0; {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		switch {
		case isRTLRune(r):
			return true
		case unicode.IsLetter(r):
			return false
		}
		i -= size
	}
	return false
}

// phraseLeast is how many times a continuation must have been seen before the local
// phrase ghost draws it.
const phraseLeast = 2

// armPhrases rebuilds the room's phrase index when its messages change, keyed on the
// timeline revision counter rather than comparing slices.
func (m Model) armPhrases() Model {
	if !m.conf.base.Complete.CompleteEnabled() || m.openRoom == "" {
		return m
	}
	if m.phrases.rev == m.timeline.rev && m.phrases.room == m.openRoom {
		return m
	}
	p := m.phrases
	// The maps are extended in place, so only by the copy that grew them to this point
	// (store.n == n): an older copy re-arming would count the same messages twice.
	grown := p.room == m.openRoom && p.me == m.me && p.n <= len(m.timeline.messages) &&
		p.store != nil && p.store.n == p.n
	if grown {
		grown = phraseSum(phraseSeed, m.timeline.messages[:p.n]) == p.sum
	}
	if grown {
		// The timeline grew at the end and nothing already indexed changed (the usual
		// live message): index only what is new.
		for i := p.n; i < len(m.timeline.messages); i++ {
			p.store.phrases.Add(&m.timeline.messages[i], m.me)
		}
		p.sum = phraseSum(p.sum, m.timeline.messages[p.n:])
	} else {
		p = phraseIndex{room: m.openRoom, me: m.me,
			store: &phraseStore{phrases: domain.PhrasesOf(m.timeline.messages, m.me)},
			sum:   phraseSum(phraseSeed, m.timeline.messages)}
	}
	p.rev, p.n = m.timeline.rev, len(m.timeline.messages)
	p.store.n = p.n
	m.phrases = p
	return m
}

// phraseIndex is the open room's trigram index, and which timeline it was built from:
// the first n messages, fingerprinted by sum, so a timeline that only grew at the end
// can be added to rather than indexed again.
type phraseIndex struct {
	room domain.RoomID
	me   string
	rev  uint64
	n    int
	sum  uint64
	// store is shared on purpose (like derived): copying 16 KB of maps per live message
	// is what incremental indexing avoids. See armPhrases for who may extend it.
	store *phraseStore
}

// phraseStore is the index's maps and how many messages they hold.
type phraseStore struct {
	phrases domain.Phrases
	n       int
}

// phrases is the index to read, empty before the first build.
func (p phraseIndex) phrases() domain.Phrases {
	if p.store == nil {
		return domain.Phrases{}
	}
	return p.store.phrases
}

// phraseSeed is FNV-1a's offset basis, the fingerprint of no messages.
const phraseSeed = uint64(14695981039346656037)

// phraseSum extends fingerprint h over what the index reads from each message.
func phraseSum(h uint64, msgs []domain.Message) uint64 {
	const prime = uint64(1099511628211)
	mix := func(s string) {
		for i := 0; i < len(s); i++ {
			h = (h ^ uint64(s[i])) * prime
		}
		h = (h ^ 0xff) * prime // a separator, so fields cannot run into each other
	}
	for i := range msgs {
		mix(string(msgs[i].ID))
		mix(msgs[i].Sender)
		mix(msgs[i].Body)
		if msgs[i].Redacted {
			mix("r")
		}
	}
	return h
}

// phraseGhost is what this room usually says next, when it says it clearly enough.
func (m Model) phraseGhost() (string, bool) {
	if !m.conf.base.Complete.GhostEnabled() {
		return "", false
	}
	draft, ok := m.draftForModel()
	if !ok {
		return "", false
	}
	first, second, ok := domain.PhraseContext(draft)
	if !ok {
		return "", false
	}
	next, ok := m.phrases.phrases().Next(first, second, m.conf.base.Complete.RatioOrDefault(), phraseLeast)
	if !ok {
		return "", false
	}
	return next + " ", true
}

// ghostTail is the suggestion drawn after the caret on this row, styled and clipped
// to the room left. It is clipped before styling: the composer's grapheme clip would
// cut an ANSI escape in half.
func (m Model) ghostTail(row string, at int, dir bidi.Direction, width int) string {
	// Only on the row the caret ends, at the end of the draft.
	if at != len(row) || m.completion.active {
		return ""
	}
	tail, ok := m.ghost()
	if !ok || tail == "" {
		return ""
	}
	room := width - ansi.StringWidth(row)
	if room < 1 {
		return ""
	}
	return m.theme.Muted.Render(drawFragment(cutLogical(tail, room), dir, nil))
}

// The model layer asks only at a word boundary, only in rooms named in
// `[complete.model] rooms` (decided by the daemon), and a live room shows a marker in
// the gutter. There is no debounce: stale answers are dropped by gen and sameAsk,
// and a held space key is coalesced by sameAsk.

// modelState is what the composer knows about the model layer.
type modelState struct {
	// text is the draft the suggestion answers for; nothing is drawn for another draft.
	text string
	// suggestion is what the ghost draws (options[0] when there are options).
	suggestion string
	// options are the continuations offered, best first (domain.CompletionOptions).
	options []string
	// refusal is why nothing was asked, for the marker and the preview.
	refusal string
	// endpoint and model name what is answering, for the gutter marker.
	endpoint string
	model    string
	// live is set once a request has actually gone out for this room.
	live bool
	// failure is the last request's error. Unlike other unrequested failures it is
	// kept and shown: a misconfigured endpoint otherwise looks like the feature is off.
	failure string
	// gen counts drafts asked about, so a late answer cannot be drawn over a newer one.
	gen     int
	applied int
	// summary/todo are the last `/summary` and `:todo` answers and their notes, and
	// preview the last dry run; the overlays outlive the messages that filled them.
	summary     string
	summaryNote string
	todo        string
	todoNote    string
	preview     domain.ModelResult
}

// modelTickMsg asks for the draft armed at gen.
type modelTickMsg struct{ gen int }

// modelDoneMsg is the answer, and the draft it answers about.
type modelDoneMsg struct {
	gen    int
	text   string
	result domain.ModelResult
	// failure is the request's error, if any.
	failure string
}

// modelAskNow schedules the ask for the next pass of the event loop (Update must not
// do I/O).
func modelAskNow(gen int) tea.Cmd {
	return func() tea.Msg { return modelTickMsg{gen: gen} }
}

// sameAsk reports whether two drafts are the same question. Trailing whitespace is
// not part of it (internal/llamacpp strips it), so a held space key asks once.
func sameAsk(a, b string) bool {
	return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t")
}

// armModelAsk schedules a model ask when the draft has reached a word boundary.
func (m Model) armModelAsk() (Model, tea.Cmd) {
	if !m.conf.base.Complete.CompleteEnabled() || !m.modelLayerOn() {
		return m, nil
	}
	draft, ok := m.draftForModel()
	if !ok {
		// Not the model's turn; drop the suggestion.
		m.model.text, m.model.suggestion, m.model.options = "", "", nil
		return m, nil
	}
	if sameAsk(draft, m.model.text) {
		return m, nil
	}
	m.model.text, m.model.suggestion, m.model.options = draft, "", nil
	m.model.gen++
	return m, modelAskNow(m.model.gen)
}

// handleModelTick asks, if the draft it was armed for is still the draft.
func (m Model) handleModelTick(msg modelTickMsg) (Model, tea.Cmd) {
	if msg.gen != m.model.gen || m.model.text == "" {
		return m, nil
	}
	return m, m.modelAskCmd(msg.gen, m.model.text, domain.ModelComplete, "")
}

// handleModelDone files an answer, unless a newer one is already in.
func (m Model) handleModelDone(msg modelDoneMsg) (Model, tea.Cmd) {
	if msg.gen != m.model.gen || msg.gen <= m.model.applied || !sameAsk(msg.text, m.model.text) {
		return m, nil
	}
	m.model.applied = msg.gen
	if msg.failure != "" {
		m.model.suggestion, m.model.options = "", nil
		m.model.live, m.model.failure = false, msg.failure
		return m, nil
	}
	m.model.failure = ""
	m.model.endpoint, m.model.model = msg.result.Endpoint, msg.result.Model
	m.model.refusal = msg.result.Refusal
	if !msg.result.Asked() {
		// Refused (room not opted in, cap spent): not an error; the marker says so.
		m.model.suggestion, m.model.options, m.model.live = "", nil, false
		return m, nil
	}
	m.model.live = true
	m.model.options = msg.result.Options
	m.model.suggestion = strings.TrimSpace(msg.result.Text)
	return m, nil
}

// modelAskCmd asks the daemon's model layer.
func (m Model) modelAskCmd(gen int, draft, task, instruction string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	req := domain.ModelRequest{
		Task: task, RoomID: m.openRoom, Draft: draft, Instruction: instruction,
		ReplyTo: m.answering(),
	}
	return func() tea.Msg {
		result, err := backend.ModelTask(ctx, req)
		if err != nil {
			// Kept: a wrong model name or key otherwise looks like a feature that is off.
			return modelDoneMsg{gen: gen, text: draft, failure: err.Error()}
		}
		return modelDoneMsg{gen: gen, text: draft, result: result}
	}
}

// draftForModel is the draft to ask about: composing, caret at the end, at least
// modelMinWords, and ending in a space (a word boundary).
func (m Model) draftForModel() (string, bool) {
	if !m.compose.insertMode || m.walk.active || m.compose.reacting || m.completion.active {
		return "", false
	}
	text := m.compose.input
	if m.editorFor(fieldComposer).at != len(text) {
		return "", false
	}
	if len(strings.Fields(text)) < modelMinWords || !strings.HasSuffix(text, " ") {
		return "", false
	}
	return text, true
}

// modelMinWords is how much must be written before the model is asked at all.
const modelMinWords = 3

// modelGhost is the model's suggestion when it answers the current draft.
func (m Model) modelGhost() (string, bool) {
	if !m.conf.base.Complete.GhostEnabled() || m.model.suggestion == "" {
		return "", false
	}
	draft, ok := m.draftForModel()
	if !ok || draft != m.model.text {
		return "", false
	}
	return m.model.suggestion, true
}

// modelMarker is the composer gutter's note about the model layer, shown whenever it
// is configured (including refusing and failing), empty otherwise.
func (m Model) modelMarker() string {
	if !m.modelLayerOn() || m.openRoom == "" {
		return ""
	}
	switch {
	case m.model.failure != "":
		return "⌂ failing"
	case m.model.live:
		return "⌂ " + m.model.model
	case m.model.refusal != "":
		return "⌂ not ready"
	default:
		return ""
	}
}

// modelLayerOn reports whether the local model may run, from config alone. Whether
// the weights exist is the daemon's question.
func (m Model) modelLayerOn() bool {
	model := m.conf.base.Complete.Model
	return model.ModelEnabled() && !model.Declined
}

// `:model` asks the daemon for a dry run: the exact request that would be sent,
// nothing sent.
// modelPreviewMsg carries a dry run into the overlay.
type modelPreviewMsg struct{ result domain.ModelResult }

// openModelPreview asks what would be sent for the draft as it stands.
func (m Model) openModelPreview() (Model, tea.Cmd) {
	ctx, backend := m.ctx, m.backend
	if m.openRoom == "" {
		return m.say("open a room first — what is sent depends on which one"), nil
	}
	// An empty draft is allowed: what the room would send is worth reading first.
	draft := m.compose.input
	req := domain.ModelRequest{
		Task: domain.ModelComplete, RoomID: m.openRoom, Draft: draft,
		ReplyTo: m.answering(), DryRun: true,
	}
	return m, func() tea.Msg {
		result, err := backend.ModelTask(ctx, req)
		if err != nil {
			return modelPreviewMsg{result: domain.ModelResult{Refusal: err.Error()}}
		}
		return modelPreviewMsg{result: result}
	}
}

// handleModelPreview puts the dry run on screen.
func (m Model) handleModelPreview(msg modelPreviewMsg) (Model, tea.Cmd) {
	m.model.preview = msg.result
	m.reader = m.reader.opening(readerAsk)
	return m, nil
}

// modelPreviewLines is the overlay's body: where it would go, and exactly what would go.
func (m Model) modelPreviewLines() []string {
	result := m.model.preview
	var lines []string
	if result.Endpoint == "" {
		lines = append(lines, "no endpoint is configured, so nothing would be sent anywhere")
	} else {
		lines = append(lines,
			m.theme.Muted.Render("to:    ")+result.Endpoint,
			m.theme.Muted.Render("model: ")+result.Model)
	}
	if m.model.failure != "" {
		lines = append(lines, "", m.theme.Muted.Render("the last request failed: ")+m.model.failure)
	}
	if !result.Asked() {
		return append(lines, "", m.theme.Muted.Render("nothing would be sent: ")+result.Refusal)
	}
	lines = append(lines, "", m.theme.Muted.Render("this would be sent, exactly as it stands:"), "")
	return append(lines, strings.Split(result.Text, "\n")...)
}

// `/summary` summarizes what you missed in the open room (after your read receipt, or
// the recent conversation if up to date). The answer opens a reader, never the
// composer, so it cannot be sent by accident.
// summaryMsg carries the answer into the overlay.
type summaryMsg struct {
	room   domain.RoomID
	result domain.ModelResult
	err    string
}

// openSummary asks for one and says so while it is coming.
func (m Model) openSummary(room domain.Room, arg string) (Model, tea.Cmd) {
	ctx, backend := m.ctx, m.backend
	// Clear the "/summary" text so it is not one enter from being sent.
	m.compose = m.compose.cleared()
	if !m.conf.base.Assist.AssistEnabled() {
		return m.say("no model endpoint configured — see [assist] in the config"), nil
	}
	span, err := domain.ParseSummarySpan(arg, time.Now())
	if err != nil {
		return m.sayErr("/summary", err), nil
	}
	m.model.summary, m.model.summaryNote = "", ""
	roomID := room.ID
	return m.say("summarizing " + m.roomName(room) + "…"), func() tea.Msg {
		result, err := backend.ModelTask(ctx, domain.ModelRequest{
			Task: domain.ModelSummary, RoomID: roomID, Span: span,
		})
		if err != nil {
			return summaryMsg{room: roomID, err: err.Error()}
		}
		return summaryMsg{room: roomID, result: result}
	}
}

// handleSummary puts it on screen, or says why there is none. Unlike the ghost, a
// refusal reaches the status line: this was asked for.
func (m Model) handleSummary(msg summaryMsg) (Model, tea.Cmd) {
	if msg.room != m.openRoom {
		// The room changed while it was being written.
		return m, nil
	}
	switch {
	case msg.err != "":
		m.log.Warn("summary failed", "room", msg.room, "err", msg.err)
		return m.say("summary failed: " + msg.err), nil
	case !msg.result.Asked():
		return m.say("no summary: " + msg.result.Refusal), nil
	case strings.TrimSpace(msg.result.Text) == "":
		return m.say("the model returned an empty summary"), nil
	}
	m.model.summary, m.model.summaryNote = msg.result.Text, msg.result.Note
	m.reader = m.reader.opening(readerSummary)
	return m.clearStatus(), nil
}

// summaryLines is the overlay's body: the dimmed note on what was read, then the
// summary.
func (m Model) summaryLines() []string {
	return m.notedLines(m.model.summaryNote, m.model.summary)
}

// notedLines is a dimmed note, a blank line, then a model answer cleaned for display.
func (m Model) notedLines(note, body string) []string {
	var lines []string
	if note != "" {
		lines = append(lines, m.theme.Muted.Render(note), "")
	}
	return append(lines, dropEmptySections(strings.Split(plainText(body), "\n"))...)
}

// `:todo` lists what people are waiting on you for across unread rooms (chosen by the
// daemon within the model layer's scope).

// todoMsg carries the list into the overlay.
type todoMsg struct {
	result domain.ModelResult
	err    string
}

// openTodo asks for it.
func (m Model) openTodo() (Model, tea.Cmd) {
	ctx, backend := m.ctx, m.backend
	if !m.conf.base.Assist.AssistEnabled() {
		return m.say("no model endpoint configured — see [assist] in the config"), nil
	}
	m.model.todo, m.model.todoNote = "", ""
	return m.say("reading what is unread…"), func() tea.Msg {
		result, err := backend.ModelTask(ctx, domain.ModelRequest{Task: domain.ModelTodo})
		if err != nil {
			return todoMsg{err: err.Error()}
		}
		return todoMsg{result: result}
	}
}

// handleTodo puts it on screen, or says why there is none.
func (m Model) handleTodo(msg todoMsg) (Model, tea.Cmd) {
	switch {
	case msg.err != "":
		m.log.Warn("todo failed", "err", msg.err)
		return m.say("todo failed: " + msg.err), nil
	case !msg.result.Asked():
		// "Nothing unread anywhere" is an answer, not a failure.
		return m.say(msg.result.Refusal), nil
	case strings.TrimSpace(msg.result.Text) == "":
		return m.say("the model returned an empty list"), nil
	}
	m.model.todo, m.model.todoNote = msg.result.Text, msg.result.Note
	m.reader = m.reader.opening(readerTodo)
	return m.clearStatus(), nil
}

// todoLines is the overlay's body: what was read, then what is owed.
func (m Model) todoLines() []string { return m.notedLines(m.model.todoNote, m.model.todo) }

// roomPalette maps every name a summary might use for the open room's speakers
// (cached and shown names, and unambiguous given names) to their message color, so a
// summary reads like the timeline. Painting happens on the visual row, after the
// bidi reorder, longest name first.
func (m Model) roomPalette() map[string]color.Color {
	colors := m.senderColorMap()
	out := make(map[string]color.Color, len(colors))
	given := map[string]map[string]bool{}
	for i := range m.timeline.messages {
		c, known := colors[m.timeline.messages[i].Sender]
		if !known {
			continue
		}
		for _, name := range []string{m.timeline.messages[i].SenderName, displayName(m.processedName(m.timeline.messages[i]))} {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			out[name] = c
			first, _, multiword := strings.Cut(name, " ")
			if !multiword || len([]rune(first)) < 2 {
				continue
			}
			if given[first] == nil {
				given[first] = map[string]bool{}
			}
			given[first][m.timeline.messages[i].Sender] = true
		}
	}
	for first, senders := range given {
		if len(senders) != 1 {
			continue
		}
		for sender := range senders {
			if _, taken := out[first]; !taken {
				out[first] = colors[sender]
			}
		}
	}
	return out
}

// paintNames returns a painter for one room's palette, or nil when there is nothing
// to paint.
func paintNames(palette map[string]color.Color) func(string) string {
	if len(palette) == 0 {
		return nil
	}
	names := make([]string, 0, len(palette))
	for name := range palette {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })

	styles := make(map[string]lipgloss.Style, len(palette))
	for name, c := range palette {
		styles[name] = lipgloss.NewStyle().Foreground(c)
	}
	// One left-to-right pass: replacing per name would re-match inside painted text.
	return func(row string) string {
		var out strings.Builder
		for i := 0; i < len(row); {
			matched := ""
			for _, name := range names {
				if strings.HasPrefix(row[i:], name) {
					matched = name
					break // names are longest-first, so the first hit is the longest
				}
			}
			if matched == "" {
				out.WriteByte(row[i])
				i++
				continue
			}
			out.WriteString(styles[matched].Render(matched))
			i += len(matched)
		}
		return out.String()
	}
}

// dropEmptySections removes a "Waiting on you" heading with nothing under it — a
// model says "none" but a person reads a heading as meaning content. Forgiving about
// how "nothing" is spelled.
func dropEmptySections(lines []string) []string {
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		head, rest, isHeading := strings.Cut(lines[i], ":")
		if !isHeading || !isSectionHead(head) {
			out = append(out, lines[i])
			continue
		}
		if nothingWord(rest) {
			// Drop the following blank line too.
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}
			continue
		}
		if strings.TrimSpace(rest) == "" && emptyBelow(lines, i+1) {
			// Drop the blank lines that separated it too.
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}
			continue
		}
		out = append(out, lines[i])
	}
	return out
}

// isSectionHead reports whether this is the summary's own heading rather than a
// person's name (the other thing ending in a colon).
func isSectionHead(head string) bool {
	return strings.EqualFold(strings.TrimSpace(head), "waiting on you")
}

// nothingWord reports whether a heading's remainder is a way of saying there is nothing.
func nothingWord(rest string) bool {
	rest = strings.ToLower(strings.Trim(strings.TrimSpace(rest), "-–—.* "))
	switch rest {
	case "none", "nothing", "nothing outstanding", "no", "n/a", "empty":
		return true
	}
	return false
}

// emptyBelow reports whether the lines from i onwards hold nothing before the next
// heading — a heading with no items under it.
func emptyBelow(lines []string, i int) bool {
	for ; i < len(lines); i++ {
		text := strings.TrimSpace(lines[i])
		if text == "" {
			continue
		}
		head, _, isHeading := strings.Cut(lines[i], ":")
		return isHeading && !strings.HasPrefix(lines[i], " ") && !strings.HasPrefix(head, "-")
	}
	return true
}

// plainText strips the emphasis markers models write despite being asked not to.
func plainText(s string) string {
	for _, marker := range []string{"**", "__"} {
		s = strings.ReplaceAll(s, marker, "")
	}
	return s
}

// answering is what the composer is aimed at: the reply target (more specific), else
// the thread root.
func (m Model) answering() domain.EventID {
	if m.compose.replyTo != "" {
		return m.compose.replyTo
	}
	return m.thread.root
}
