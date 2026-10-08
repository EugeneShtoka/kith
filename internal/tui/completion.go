package tui

import (
	"log/slog"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// completionRows is how many candidates the popup shows at once; the rest are
// counted in a footer.
const completionRows = 8

// mentionCandidateLimit bounds how many members are ranked for the dropdown.
const mentionCandidateLimit = 500

// candidate is one row of the completion popup.
type candidate struct {
	text   string // inserted
	label  string // shown; may differ from text (a shortcode's emoji)
	detail string // dimmed suffix disambiguating otherwise identical rows
	// userID / roomID make accepting record a mention or a room link rather than
	// only inserting a name.
	userID string
	roomID string
	// emoji is set for an emoji row so accepting can record the choice for ranking.
	emoji string
	// spaceAfter appends a space on accept: names are followed by more words, emoji
	// often by punctuation.
	spaceAfter bool
}

// completionTarget is which text field the popup is completing.

// completionTarget is which text field the popup is completing. The popup was built
// for the composer, but the react prompt is where a `:shortcode:` is *most* typed —
// it is the reason the shortcode map exists — so the popup has to be able to edit
// either field rather than assuming one.
type completionTarget int

const (
	targetComposer completionTarget = iota
	targetReaction
	// targetPrompt is the search prompt (`from:`) or the command line.
	targetPrompt
)

// completionState is the open completion popup.
type completionState struct {
	active bool
	target completionTarget
	// trigger is the text that opened it — "@", ":", "from:", or "" for the command
	// line and word completion.
	trigger string
	// start is the byte offset of the trigger in the field's text.
	start int
	// userCommands is the commands directory scanned when this popup opened, so a
	// script added a minute ago needs no restart.
	userCommands []slashCommand
	// scanning is true between opening the "/" popup and its directory scan landing,
	// so an empty candidate list does not close the popup before the scan answers.
	scanning bool
	// query is what has been typed after the trigger.
	query      string
	candidates []candidate
	cursor     int
}

// openCompletion starts a popup for the trigger at the given offset in the given
// field. For "/" it also returns the off-loop scan of the commands directory; the
// built-ins show immediately.
func (m Model) openCompletion(target completionTarget, trigger string, start int) (Model, tea.Cmd) {
	m.completion = completionState{
		active: true, target: target, trigger: trigger, start: start,
		scanning: trigger == "/",
	}
	m = m.refreshCandidates()
	if trigger != "/" {
		return m, nil
	}
	return m, m.scanUserCommandsCmd()
}

// handleUserCommands folds a finished directory scan into the open "/" popup, if it
// is still open.
func (m Model) handleUserCommands(msg userCommandsMsg) (Model, tea.Cmd) {
	if !m.completion.active || m.completion.trigger != "/" {
		return m, nil
	}
	m.completion.userCommands = msg.commands
	m.completion.scanning = false
	return m.refreshCandidates(), nil
}

// fieldOf is which text field a completion target names.
func fieldOf(target completionTarget) field {
	switch target {
	case targetReaction:
		return fieldReact
	case targetPrompt:
		return fieldPrompt
	default:
		return fieldComposer
	}
}

// completionEditor is the field being completed, with its caret.
func (m Model) completionEditor() editor {
	return m.editorFor(fieldOf(m.completion.target))
}

// triggersFor is which sources are offered in a field, longest first so "from:" is
// recognized before its ":". A reaction is a single emoji, so only ":" there.
func triggersFor(target completionTarget) []string {
	switch target {
	case targetReaction:
		return []string{":"}
	case targetPrompt:
		return []string{"from:"}
	default:
		return []string{"@", ":", "#", "/"}
	}
}

// closeCompletion dismisses the popup, leaving the composer text as typed.
func (m Model) closeCompletion() Model {
	m.completion = completionState{}
	return m
}

// completionSources maps each typed trigger to the candidates it offers.
var completionSources = map[string]func(m Model, query string) []candidate{
	"@":     Model.mentionCandidates,
	"#":     Model.roomCandidates,
	":":     Model.emojiCandidates,
	"/":     Model.commandCandidates,
	"from:": Model.senderCandidates,
}

// completionSourceFor is which source answers for one popup. The command line and word
// completion have an empty trigger and are told apart by the field.
func completionSourceFor(target completionTarget, trigger string) (func(Model, string) []candidate, bool) {
	if trigger == "" {
		if target == targetPrompt {
			return Model.commandLineCandidates, true
		}
		return Model.wordCandidates, true
	}
	source, ok := completionSources[trigger]
	return source, ok
}

// triggerKept reports whether accepting leaves the trigger in place: "@" and ":"
// announce a value and are replaced by it; "from:" names a field and keeps its place.
func triggerKept(trigger string) bool { return strings.HasSuffix(trigger, ":") && len(trigger) > 1 }

// triggerOpens reports whether a trigger typed after text should open a popup: only
// at the start or after whitespace (so an email address does not), and "/" only at
// the very start, where it can be a command.
func triggerOpens(trigger, text string) bool {
	if trigger == "/" {
		return text == ""
	}
	if text == "" {
		return true
	}
	last := []rune(text)[len([]rune(text))-1]
	return unicode.IsSpace(last)
}

// refreshCandidates re-filters the popup against its query, closing it when nothing
// matches.
func (m Model) refreshCandidates() Model {
	if !m.completion.active {
		return m
	}
	source, ok := completionSourceFor(m.completion.target, m.completion.trigger)
	if !ok {
		return m.closeCompletion()
	}
	m.completion.candidates = source(m, m.completion.query)
	if len(m.completion.candidates) == 0 {
		if m.completion.scanning {
			// A pending scan may still add scripts; an empty popup draws nothing.
			return m
		}
		return m.closeCompletion()
	}
	m.completion.cursor = clampIndex(m.completion.cursor, len(m.completion.candidates))
	return m
}

// mentionCandidates filters the room's pre-ranked members for a query.
func (m Model) mentionCandidates(query string) []candidate {
	return m.matchPeople(query, m.timeline.members)
}

// roomCandidates are the rooms a `#` names, across every room this account is in. The
// room's name is inserted; the link is written on send (see domain.Mention).
func (m Model) roomCandidates(query string) []candidate {
	type scored struct {
		name string
		id   string
		rank int
	}
	q := strings.ToLower(strings.TrimSpace(query))
	matches := make([]scored, 0, len(m.rooms.all))
	for i := range m.rooms.all {
		room := m.rooms.all[i]
		name := m.roomLabel(room)
		if name == "" {
			continue
		}
		rank := roomMatch(strings.ToLower(name), strings.ToLower(string(room.ID)), q)
		if rank == 0 {
			continue
		}
		matches = append(matches, scored{name: name, id: string(room.ID), rank: rank})
	}
	// Stable, so the room list's own order breaks ties.
	sort.SliceStable(matches, func(a, b int) bool { return matches[a].rank < matches[b].rank })

	out := make([]candidate, 0, len(matches))
	for _, match := range matches {
		out = append(out, candidate{
			text:       match.name,
			label:      isolate(match.name),
			detail:     match.id,
			roomID:     match.id,
			spaceAfter: true,
		})
	}
	return out
}

// roomMatch ranks a room against the query: 1 name prefix, 2 name substring, 3 ID
// substring, 0 no match.
func roomMatch(name, id, query string) int {
	switch {
	case query == "":
		return 1
	case strings.HasPrefix(name, query):
		return 1
	case strings.Contains(name, query):
		return 2
	case strings.Contains(id, query):
		return 3
	}
	return 0
}

// searchPeople is who `from:` completes against: everyone who has posted in the
// search's scope, then anyone in the open room who has not (available instantly).
func (m Model) searchPeople() []domain.Member {
	people := m.search.people
	if len(people) == 0 {
		return m.timeline.members
	}
	seen := make(map[string]bool, len(people))
	for _, person := range people {
		seen[person.UserID] = true
	}
	out := make([]domain.Member, 0, len(people)+len(m.timeline.members))
	out = append(out, people...)
	for _, member := range m.timeline.members {
		if !seen[member.UserID] {
			out = append(out, member)
		}
	}
	return out
}

// matchPeople filters and orders a pre-ranked list of people for a query, best match
// first, keeping the incoming order within each match quality.
func (m Model) matchPeople(query string, people []domain.Member) []candidate {
	type scored struct {
		member domain.Member
		rank   int
	}
	matches := make([]scored, 0, len(people))
	for _, member := range people {
		if rank := member.Matches(query); rank > 0 {
			matches = append(matches, scored{member: member, rank: rank})
		}
	}
	sort.SliceStable(matches, func(a, b int) bool { return matches[a].rank < matches[b].rank })

	// One row per person: a configured identity merges several user IDs. The kept row is
	// the highest-ranked account, which is one actually in this room.
	rows := make([]candidate, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		person, name := m.personOf(match.member)
		if seen[person] {
			continue
		}
		seen[person] = true
		rows = append(rows, candidate{
			text: name, label: isolate(name), userID: match.member.UserID, spaceAfter: true,
		})
	}

	// A shared name shows the MXID; otherwise a bridged account shows its network.
	shared := make(map[string]int, len(rows))
	for _, row := range rows {
		shared[row.label]++
	}
	for i := range rows {
		if shared[rows[i].label] > 1 {
			rows[i].detail = rows[i].userID
			continue
		}
		if protocol := domain.ProtocolOf(rows[i].userID); protocol.IsBridged() {
			rows[i].detail = protocol.String()
		}
	}
	return rows
}

// personOf resolves a member to their person key and display name, applying a
// configured identity's alias.
func (m Model) personOf(member domain.Member) (person, name string) {
	if ident, ok := m.prefs.identities[member.UserID]; ok {
		if ident.alias != "" {
			return ident.key, ident.alias
		}
		return ident.key, m.memberName(member)
	}
	return member.UserID, m.memberName(member)
}

// selectedCandidate is the row under the cursor.
func (m Model) selectedCandidate() (candidate, bool) {
	if !m.completion.active || m.completion.cursor >= len(m.completion.candidates) {
		return candidate{}, false
	}
	return m.completion.candidates[m.completion.cursor], true
}

// acceptCompletion replaces the trigger and query with the selected candidate. For a
// member or room in the composer it also records the mention/link.
func (m Model) acceptCompletion() (Model, tea.Cmd) {
	chosen, ok := m.selectedCandidate()
	if !ok {
		return m.closeCompletion(), nil
	}
	ed := m.completionEditor()
	start := m.completion.start
	if start < 0 || start > ed.at {
		return m.closeCompletion(), nil
	}
	// The text after the caret stays. A trigger that names a field ("from:") is kept.
	head := ed.text[:start]
	if triggerKept(m.completion.trigger) {
		head += m.completion.trigger
	}
	inserted := chosen.text
	if chosen.spaceAfter {
		inserted += " "
	}
	// One undoable edit, so ctrl+z takes the completion back off.
	m = m.store(fieldOf(m.completion.target),
		ed.changed(head+inserted+ed.after(), len(head)+len(inserted), editWhole))
	// Mentions and room links belong to messages, not to search filters.
	if chosen.userID != "" && m.completion.target == targetComposer {
		m.compose.drafted = append(m.compose.drafted, domain.Mention{UserID: chosen.userID, Name: chosen.text})
	}
	if chosen.roomID != "" && m.completion.target == targetComposer {
		m.compose.drafted = append(m.compose.drafted, domain.Mention{RoomID: chosen.roomID, Name: chosen.text})
	}
	m = m.closeCompletion()
	if chosen.emoji != "" {
		return m, m.recordEmojiCmd(chosen.emoji)
	}
	// The command line is a chooser: accepting runs it.
	if m.prompt.kind == promptCommand {
		return m.submitPrompt()
	}
	return m, nil
}

// moveCompletionCursor walks the popup, wrapping.
func (m Model) moveCompletionCursor(delta int) (Model, tea.Cmd) {
	m.completion.cursor = moveCursor(m.completion.cursor, len(m.completion.candidates), delta, true)
	return m, nil
}

// handleCompletionKey gives the popup its keys while it is open; handled is false
// for anything it does not claim, so typing passes through. It is consulted before
// the global scope.
func (m Model) handleCompletionKey(key tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch m.keys.lookup(key.String(), scopeCompletion) {
	case actNext:
		return answered(m.moveCompletionCursor(1))
	case actPrev:
		return answered(m.moveCompletionCursor(-1))
	case actAcceptCompletion:
		return answered(m.acceptCompletion())
	case actDismiss:
		return m.closeCompletion(), nil, true
	}
	return m, nil, false
}

// composerTyped keeps the popup in step with the composer after its text changes.
func (m Model) composerTyped(typed string) (Model, tea.Cmd) {
	m, cmd := m.textTyped(targetComposer, typed)
	// Every composer change passes here: keep the typing notice in step and note
	// word boundaries for autocorrect.
	m, typing := m.noteTyping()
	return m.noteWordBoundary(typed), tea.Batch(cmd, typing)
}

// reactionTyped keeps the popup in step with the react prompt.
func (m Model) reactionTyped(typed string) (Model, tea.Cmd) {
	return m.textTyped(targetReaction, typed)
}

// textTyped keeps the popup in step with a text field after it changes: a trigger
// opens one, typing narrows it, and moving before the trigger closes it. The command
// records an emoji completed by typing the closing colon.
func (m Model) textTyped(target completionTarget, typed string) (Model, tea.Cmd) {
	ed := m.editorFor(fieldOf(target))
	if m.completion.active {
		if m.completion.target != target {
			// The popup belongs to another field.
			return m.closeCompletion(), nil
		}
		// The query is between the trigger and the caret.
		queryStart := m.completion.start + len(m.completion.trigger)
		if queryStart > len(ed.text) || ed.at < queryStart {
			return m.closeCompletion(), nil
		}
		m.completion.query = ed.text[queryStart:ed.at]
		// Typing the closing colon of ":tada:" accepts it.
		if m.completion.trigger == ":" && strings.HasSuffix(m.completion.query, ":") {
			return m.acceptClosedShortcode()
		}
		// Names do not span words.
		if strings.ContainsFunc(m.completion.query, unicode.IsSpace) {
			return m.closeCompletion(), nil
		}
		return m.refreshCandidates(), nil
	}
	if typed == "" {
		return m, nil
	}
	// A trigger ends at the caret.
	for _, trigger := range triggersFor(target) {
		start := ed.at - len(trigger)
		if start < 0 || ed.text[start:ed.at] != trigger {
			continue
		}
		// Only what was just typed opens a popup, not moving back over an old "@".
		if !strings.HasSuffix(trigger, typed) && !strings.HasSuffix(typed, trigger) {
			continue
		}
		if !triggerOpens(trigger, ed.text[:start]) {
			continue
		}
		return m.openCompletion(target, trigger, start)
	}
	return m, nil
}

// promptTyped keeps the popup in step with the search/command prompt.
func (m Model) promptTyped(typed string) (Model, tea.Cmd) {
	return m.textTyped(targetPrompt, typed)
}

// handleMembers applies a room's ranked mention candidates; stale results and
// failures are dropped.
func (m Model) handleMembers(msg membersMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "load mention candidates", msg.err, "room", msg.roomID)
	if msg.err != nil || msg.roomID != m.openRoom {
		return m, nil
	}
	m.timeline.members = msg.members
	m.timeline.membersRev++
	return m.refreshCandidates(), nil
}

// emojiCandidates offers composing emoji for a ":" query.
func (m Model) emojiCandidates(query string) []candidate {
	return m.emojiCandidatesOf(query, domain.EmojiComposed)
}

// emojiCandidatesOf lists emoji of one kind (composed and reaction vocabularies are
// ranked apart): ranked browse order with no query, else prefix matches then
// substring matches, each ranked.
func (m Model) emojiCandidatesOf(query string, kind domain.EmojiKind) []candidate {
	q := strings.ToLower(strings.TrimSpace(query))
	names := m.glyphs.set.names
	if q == "" {
		return m.emojiRows(m.rankedEmoji(kind))
	}

	var prefix, contains []string
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		emoji := m.glyphs.set.byName[name]
		switch {
		case strings.HasPrefix(name, q):
			if !seen[emoji] {
				seen[emoji] = true
				prefix = append(prefix, emoji)
			}
		case strings.Contains(name, q):
			contains = append(contains, emoji)
		}
	}
	ranker := m.ranker(kind)
	matched := ranker.sorted(prefix)
	for _, emoji := range ranker.sorted(contains) {
		if !seen[emoji] {
			seen[emoji] = true
			matched = append(matched, emoji)
		}
	}
	return m.emojiRows(matched)
}

// emojiRows turns emoji into popup rows labeled with their shortcode.
func (m Model) emojiRows(emojis []string) []candidate {
	out := make([]candidate, 0, len(emojis))
	for _, emoji := range emojis {
		// Shortcodes are keyed on the untoned form; the tone goes on what is inserted.
		name := m.shortcodeFor(emoji)
		emoji = m.tone(emoji)
		row := candidate{text: emoji, emoji: emoji, label: emoji}
		if name != "" {
			row.label = emoji + "  :" + name + ":"
		}
		out = append(out, row)
	}
	return out
}

// acceptClosedShortcode handles a query finished with ":". A known shortcode becomes
// its emoji; an unknown one is left as typed (":30:" in "at 10:30:").
func (m Model) acceptClosedShortcode() (Model, tea.Cmd) {
	name := strings.ToLower(strings.TrimSuffix(m.completion.query, ":"))
	emoji, known := m.glyphs.set.byName[name]
	if !known {
		return m.closeCompletion(), nil
	}
	ed := m.completionEditor()
	if m.completion.start < 0 || m.completion.start > ed.at {
		return m.closeCompletion(), nil
	}
	m = m.store(fieldOf(m.completion.target),
		ed.changed(ed.text[:m.completion.start]+emoji+ed.after(), m.completion.start+len(emoji), editWhole))
	m = m.closeCompletion()
	return m, m.recordEmojiCmd(emoji)
}

// senderCandidates offers people for `from:`. The MXID is inserted, since the filter
// is matched against the index and display names collide.
func (m Model) senderCandidates(query string) []candidate {
	rows := m.matchPeople(query, m.searchPeople())
	out := make([]candidate, 0, len(rows))
	for _, row := range rows {
		if row.userID == "" {
			continue
		}
		out = append(out, candidate{
			// No trailing space: it would end the token.
			text:   row.userID,
			label:  row.label,
			detail: row.userID,
		})
	}
	return out
}
