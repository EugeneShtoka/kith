package local

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/llm"
	"github.com/EugeneShtoka/kith/internal/spell"
)

// The model layer: one endpoint, several tasks, and the rails around them. Every
// decision about whether to ask lives here (and in domain/assist.go): an endpoint
// must be configured, the room must be opted in (encrypted rooms twice, see
// domain.AllowModel), a per-minute cap applies, and a dry run shows the exact
// request without sending it.

// ModelSettings is the model layer's configuration, translated from the config file
// by the caller.
type ModelSettings struct {
	// Endpoint is one OpenAI-compatible chat URL. Empty is off, and is the default.
	Endpoint string
	// Model is the model name; Key the API key (may be empty for a local model).
	Model string
	Key   string
	// Scope is which rooms may be seen (see domain.AllowModel).
	Scope domain.ModelScope
	// Budget is roughly how many tokens may be quoted; Budgets overrides it per task.
	Budget  int
	Budgets map[string]int
	// Options is how many continuations a completion offers.
	Options int
	// Pick is what a completion quotes (see domain.CompletionContext).
	Pick domain.ContextPick
	// TodoRooms caps how many rooms the todo task reads.
	TodoRooms int
	// PerMinute caps requests; Timeout bounds one of them.
	PerMinute int
	Timeout   time.Duration
	// Tasks are the prompts by name; an unconfigured task cannot be asked for.
	Tasks map[string]llm.Task
}

// modelLayer is the running feature: client, settings and the cap's request stamps.
type modelLayer struct {
	mu       sync.Mutex
	settings ModelSettings
	client   *llm.Client
	// recent are the send times within the last minute (a rolling window).
	recent []time.Time
	// now is time.Now, replaced in tests.
	now func() time.Time
}

// UseModel wires the model layer, or turns it off. Called at startup and on reload.
func (s *Service) UseModel(settings ModelSettings) {
	client, err := llm.New(settings.Endpoint, settings.Model, settings.Key, settings.Timeout)
	if err != nil {
		// New's only error is "nothing configured": the layer then refuses with a reason.
		client = nil
	}
	s.model.mu.Lock()
	defer s.model.mu.Unlock()
	s.model.settings = settings
	s.model.client = client
	s.model.recent = nil
}

// ModelTask asks the model for one thing, or refuses and says why.
func (s *Service) ModelTask(ctx context.Context, req domain.ModelRequest) (domain.ModelResult, error) {
	s.model.mu.Lock()
	settings := s.model.settings
	client := s.model.client
	s.model.mu.Unlock()

	// A completion is answered by the local model only (see completionmodel.go); the
	// endpoint serves the deliberate tasks (summary, todo, rewrite).
	if req.Task == domain.ModelComplete {
		return s.completeFromModel(ctx, req, settings), nil
	}

	result := domain.ModelResult{Endpoint: settings.Endpoint, Model: settings.Model}
	task, known := settings.Tasks[req.Task]
	if !known {
		result.Refusal = fmt.Sprintf("no prompt configured for the %q task", req.Task)
		return result, nil
	}
	refusal, err := s.refuseAsk(ctx, req, settings)
	if err != nil {
		return result, err
	}
	if refusal != "" {
		result.Refusal = refusal
		return result, nil
	}

	fields, note, err := s.modelFields(ctx, req, settings)
	if err != nil {
		return result, err
	}
	result.Note = note
	if empty := emptyContextRefusal(req.Task, fields["{context}"]); empty != "" {
		result.Refusal = empty
		return result, nil
	}

	// A dry run returns the exact request instead of sending it (for `why`).
	if req.DryRun {
		result.Text = describeAsk(task, fields)
		return result, nil
	}
	if !s.model.spend() {
		result.Refusal = fmt.Sprintf("%d requests a minute is the configured cap", settings.PerMinute)
		return result, nil
	}

	text, err := client.Ask(ctx, task, fields)
	if err != nil {
		return result, fmt.Errorf("local: model %s: %w", req.Task, err)
	}
	result.Text = text
	return result, nil
}

// refuseAsk returns the first reason not to ask, cheapest first; it costs no request.
func (s *Service) refuseAsk(ctx context.Context, req domain.ModelRequest, settings ModelSettings) (string, error) {
	// Room-reading tasks have no draft (and dry runs are exempt from this check).
	if strings.TrimSpace(req.Draft) == "" && !req.DryRun && !readsRooms(req.Task) {
		return "nothing written to work from", nil
	}
	// todo picks its own rooms and checks each (see todoFields).
	if req.Task == domain.ModelTodo {
		return "", nil
	}
	permit, err := s.permitModel(ctx, req.RoomID, settings)
	if err != nil {
		return "", err
	}
	return permit.Refusal, nil
}

// readsRooms reports whether a task is about the conversation rather than a draft.
func readsRooms(task string) bool {
	return task == domain.ModelSummary || task == domain.ModelTodo ||
		task == domain.ModelThreadName
}

// permitModel resolves the opt-in for one room, matched by ID or displayed name.
func (s *Service) permitModel(ctx context.Context, roomID domain.RoomID, settings ModelSettings) (domain.ModelPermit, error) {
	facts, err := s.roomFacts(ctx, roomID)
	if err != nil && (!errors.Is(err, errNoPlaces) || settings.Scope.NeedsPlaces()) {
		return domain.ModelPermit{}, err
	}
	return domain.AllowModel(settings.Scope, facts,
		settings.Endpoint, s.roomEncrypted(ctx, roomID)), nil
}

// errNoPlaces refuses the model opt-in while the cache is off: where a room sits is
// unknown, and a scope decided on a guess fails open.
var errNoPlaces = errors.New("local: the cache is off, so which spaces a room is in is unknown")

// roomFacts gathers what a scope entry can match: name, spaces, DM flag, and the
// network, taken from the owning space's bridge rather than from senders.
func (s *Service) roomFacts(ctx context.Context, roomID domain.RoomID) (domain.RoomFacts, error) {
	if s.cache == nil {
		// No cache, no spaces: guessing "in none" would slip past an except = ["space:…"].
		return domain.RoomFacts{ID: string(roomID), Protocol: domain.NetworkOf(string(roomID))}, errNoPlaces
	}
	rooms, err := s.cache.Rooms(ctx)
	if err != nil {
		return domain.RoomFacts{}, fmt.Errorf("local: read rooms for the model opt-in: %w", err)
	}
	spaces, err := s.cache.Spaces(ctx)
	if err != nil {
		return domain.RoomFacts{}, fmt.Errorf("local: read spaces for the model opt-in: %w", err)
	}
	room := domain.Room{ID: roomID}
	if i := slices.IndexFunc(rooms, func(r domain.Room) bool { return r.ID == roomID }); i >= 0 {
		room = rooms[i]
	}
	return s.placesNow().Facts(room, domain.HoldersOf(roomID, spaces)), nil
}

// modelFields are the template's values. The context is this room's cached messages
// only: "help me write here" is not permission to quote elsewhere.
func (s *Service) modelFields(ctx context.Context, req domain.ModelRequest, settings ModelSettings) (map[string]string, string, error) {
	budget := settings.Budget
	if override, ok := settings.Budgets[req.Task]; ok && override > 0 {
		budget = override
	}
	var quoted []string
	var note string
	if req.Task == domain.ModelTodo {
		return s.todoFields(ctx, req, settings, budget)
	}
	if s.cache != nil {
		// A thread name reads the thread, not the room.
		if req.Task == domain.ModelThreadName {
			return s.threadNameFields(ctx, req, budget)
		}
		msgs, err := s.cache.Messages(ctx, req.RoomID, s.contextDepth(req.Task))
		if err != nil {
			return nil, "", fmt.Errorf("local: read context for the model: %w", err)
		}
		switch req.Task {
		case domain.ModelSummary:
			msgs, note = s.summarySpan(ctx, req.RoomID, msgs, req.Span)
		default:
			// Sentence tasks see the exchange they belong to, within the budget.
			msgs = domain.CompletionContext(msgs, req.ReplyTo, settings.Pick)
		}
		quoted = domain.ModelContext(msgs, s.account(), budget)
	}
	// A summary's language comes from the conversation (there is no draft).
	language := modelLanguage(req.Draft)
	if req.Task == domain.ModelSummary {
		language = modelLanguage(strings.Join(quoted, "\n"))
	}
	return s.promptFields(ctx, req.Draft, strings.Join(quoted, "\n"), language, req.Instruction), note, nil
}

// promptFields is the template's value map.
func (s *Service) promptFields(ctx context.Context, draft, context, language, instruction string) map[string]string {
	return map[string]string{
		"{draft}":       draft,
		"{context}":     context,
		"{language}":    language,
		"{instruction}": instruction,
		"{me}":          s.accountName(ctx),
		"{names}":       s.accountNames(ctx),
	}
}

// summarySpan is what a summary reads and a note saying which span it is: an explicit
// span if asked, else what you missed, else (too little missed, e.g. the room you are
// looking at) the whole cached tail.
func (s *Service) summarySpan(
	ctx context.Context, roomID domain.RoomID, msgs []domain.Message, asked domain.SummarySpan,
) ([]domain.Message, string) {
	// An explicit span wins even when empty.
	if asked.Given() {
		chosen := domain.MessagesIn(msgs, asked.Since, asked.Count)
		return chosen, fmt.Sprintf("%d messages from %s", len(chosen), asked.Said)
	}
	missed := domain.MessagesAfter(msgs, s.readMarker(ctx, roomID))
	if len(missed) >= summaryMinSpan {
		return missed, fmt.Sprintf("%d messages since you last read this room", len(missed))
	}
	if len(msgs) == 0 {
		return nil, ""
	}
	if len(missed) == 0 {
		return msgs, "you are up to date — this is the recent conversation"
	}
	return msgs, fmt.Sprintf("%d new since you last read, shown with what came before", len(missed))
}

// summaryMinSpan is how many unread messages make "what you missed" worth summarizing alone.
const summaryMinSpan = 5

// emptyContextRefusal says why there is nothing to work from, or "" to go on, so an
// empty context never spends a request. Other tasks may legitimately have none.
func emptyContextRefusal(task, context string) string {
	if strings.TrimSpace(context) != "" {
		return ""
	}
	switch task {
	case domain.ModelSummary:
		return "nothing cached in this room to summarize yet"
	case domain.ModelThreadName:
		return "nothing cached in that thread to name it from"
	case domain.ModelTodo:
		return "nothing unread anywhere — you are up to date"
	}
	return ""
}

// threadNameFields is the context for naming one thread: the thread itself.
func (s *Service) threadNameFields(
	ctx context.Context, req domain.ModelRequest, budget int,
) (map[string]string, string, error) {
	msgs, err := s.cache.ThreadMessages(ctx, req.RoomID, req.ReplyTo, threadNameMessages)
	if err != nil {
		return nil, "", fmt.Errorf("local: read thread for the model: %w", err)
	}
	quoted := domain.ModelContext(msgs, s.account(), budget)
	joined := strings.Join(quoted, "\n")
	// The thread's language: the name is persisted, so a wrong one would stick.
	return s.promptFields(ctx, "", joined, modelLanguage(joined), ""), "", nil
}

// contextDepth is how many messages to read before the budget trims them.
func (s *Service) contextDepth(task string) int {
	if task == domain.ModelSummary {
		return summaryContextMessages
	}
	return modelContextMessages
}

// readMarker is how far this account has read in a room ("" when unknown).
func (s *Service) readMarker(ctx context.Context, roomID domain.RoomID) domain.EventID {
	if s.cache == nil {
		return ""
	}
	unread, err := s.cache.Unread(ctx)
	if err != nil {
		s.warnIf(ctx, err, "read cached read marker", "room", roomID)
		return ""
	}
	for i := range unread {
		if unread[i].RoomID == roomID {
			return unread[i].ReadEvent
		}
	}
	return ""
}

// modelContextMessages bounds what is read before the budget trims it.
const modelContextMessages = 60

// summaryContextMessages is the same bound for a summary.
const summaryContextMessages = 400

// threadNameMessages bounds naming a thread: the opening says what it is about.
const threadNameMessages = 20

// modelLanguage names the draft's language, using the spellchecker's detector so the
// two never disagree.
func modelLanguage(draft string) string { return spell.LanguageOf(draft) }

// describeAsk renders a task exactly as it would be sent, for `why`.
func describeAsk(task llm.Task, fields map[string]string) string {
	var out strings.Builder
	if system := strings.TrimSpace(llm.Render(task.System, fields)); system != "" {
		out.WriteString("system:\n")
		out.WriteString(system)
		out.WriteString("\n\n")
	}
	out.WriteString("user:\n")
	out.WriteString(strings.TrimSpace(llm.Render(task.Prompt, fields)))
	return out.String()
}

// spend takes one slot from the per-minute cap, reporting whether there was one.
func (l *modelLayer) spend() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	clock := l.now
	if clock == nil {
		clock = time.Now
	}
	cap := l.settings.PerMinute
	if cap <= 0 {
		return true
	}
	cutoff := clock().Add(-time.Minute)
	kept := l.recent[:0]
	for _, at := range l.recent {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	l.recent = kept
	if len(l.recent) >= cap {
		return false
	}
	l.recent = append(l.recent, clock())
	return true
}

// todoFields quotes what unread rooms are waiting on, for the todo task. Only rooms
// with something unread, each checked against the scope as it is chosen, sharing
// the budget.
func (s *Service) todoFields(
	ctx context.Context, req domain.ModelRequest, settings ModelSettings, budget int,
) (map[string]string, string, error) {
	if s.cache == nil {
		return nil, "", nil
	}
	unread, err := s.roomsWithSomethingUnread(ctx)
	if err != nil {
		return nil, "", err
	}

	rooms := settings.TodoRooms
	if rooms <= 0 {
		rooms = 1
	}
	each := budget / rooms
	var quoted []string
	var read int
	for i := range unread {
		if read >= rooms {
			break
		}
		permit, perr := s.permitModel(ctx, unread[i].RoomID, settings)
		if perr != nil {
			return nil, "", perr
		}
		if !permit.Allowed {
			continue
		}
		msgs, merr := s.cache.Messages(ctx, unread[i].RoomID, modelContextMessages)
		if merr != nil {
			return nil, "", fmt.Errorf("local: read a room for the todo list: %w", merr)
		}
		missed := domain.MessagesAfter(msgs, unread[i].ReadEvent)
		if len(missed) == 0 {
			continue
		}
		lines := domain.ModelContext(missed, s.account(), each)
		if len(lines) == 0 {
			continue
		}
		read++
		quoted = append(quoted,
			"## "+s.namedRoom(ctx, unread[i].RoomID), strings.Join(lines, "\n"), "")
	}
	note := fmt.Sprintf("%d rooms with something unread", read)
	if read == 0 {
		note = "nothing unread anywhere"
	}
	context := strings.Join(quoted, "\n")
	return s.promptFields(ctx, "", context, modelLanguage(context), req.Instruction), note, nil
}

// roomsWithSomethingUnread is the rooms with unread messages (local counts, falling
// back to the server's when no read position resolves), mentions then volume first.
func (s *Service) roomsWithSomethingUnread(ctx context.Context) ([]domain.Unread, error) {
	counts, err := s.cache.CountUnreadAll(ctx, s.me())
	if err != nil {
		return nil, fmt.Errorf("local: count what is unread: %w", err)
	}
	positions, err := s.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("local: read unread positions: %w", err)
	}
	out := make([]domain.Unread, 0, len(positions))
	for _, position := range positions {
		room := position
		if counted, ok := counts[position.RoomID]; ok {
			room.Messages = counted.Messages
		} else {
			room.Messages = position.Notifications
		}
		if room.Messages == 0 {
			continue
		}
		out = append(out, room)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Highlights != out[j].Highlights {
			return out[i].Highlights > out[j].Highlights
		}
		return out[i].Messages > out[j].Messages
	})
	return out, nil
}

// namedRoom is a room's cached name, or its ID.
func (s *Service) namedRoom(ctx context.Context, roomID domain.RoomID) string {
	if s.cache == nil {
		return string(roomID)
	}
	rooms, err := s.cache.Rooms(ctx)
	if err != nil {
		s.warnIf(ctx, err, "read cached rooms", "room", roomID)
		return string(roomID)
	}
	for i := range rooms {
		if rooms[i].ID == roomID && rooms[i].Name != "" {
			return rooms[i].Name
		}
	}
	return string(roomID)
}

// accountNames is every way a conversation might address this account (display name,
// its first word, MXID localpart), so the model recognizes you in the text.
func (s *Service) accountNames(ctx context.Context) string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[strings.ToLower(name)] {
			return
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	full := s.accountName(ctx)
	add(full)
	if first, _, multiword := strings.Cut(full, " "); multiword {
		add(first)
	}
	if local, _, found := strings.Cut(strings.TrimPrefix(s.account(), "@"), ":"); found {
		add(local)
	}
	return strings.Join(names, ", ")
}

// accountName is what to call this account in a prompt: its display name (what the
// conversation uses), else the localpart, else "you".
func (s *Service) accountName(ctx context.Context) string {
	me := s.account()
	if me == "" {
		return "you"
	}
	if s.cache != nil {
		if name, err := s.cache.MemberName(ctx, me); err == nil && name != "" {
			return name
		}
	}
	if domain.IsMatrixUserID(me) {
		if local := domain.Localpart(me); local != "" {
			return local
		}
	}
	return "you"
}
