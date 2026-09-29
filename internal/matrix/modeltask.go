package matrix

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
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
func (b *InProc) UseModel(settings ModelSettings) {
	client, err := llm.New(settings.Endpoint, settings.Model, settings.Key, settings.Timeout)
	if err != nil {
		// New's only error is "nothing configured": the layer then refuses with a reason.
		client = nil
	}
	b.model.mu.Lock()
	defer b.model.mu.Unlock()
	b.model.settings = settings
	b.model.client = client
	b.model.recent = nil
}

// ModelTask asks the model for one thing, or refuses and says why.
func (b *InProc) ModelTask(ctx context.Context, req domain.ModelRequest) (domain.ModelResult, error) {
	b.model.mu.Lock()
	settings := b.model.settings
	client := b.model.client
	b.model.mu.Unlock()

	// A completion is answered by the local model only (see completionmodel.go); the
	// endpoint serves the deliberate tasks (summary, todo, rewrite).
	if req.Task == domain.ModelComplete {
		return b.completeFromModel(ctx, req, settings), nil
	}

	result := domain.ModelResult{Endpoint: settings.Endpoint, Model: settings.Model}
	task, known := settings.Tasks[req.Task]
	if !known {
		result.Refusal = fmt.Sprintf("no prompt configured for the %q task", req.Task)
		return result, nil
	}
	refusal, err := b.refuseAsk(ctx, req, settings)
	if err != nil {
		return result, err
	}
	if refusal != "" {
		result.Refusal = refusal
		return result, nil
	}

	fields, note, err := b.modelFields(ctx, req, settings)
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
	if !b.model.spend() {
		result.Refusal = fmt.Sprintf("%d requests a minute is the configured cap", settings.PerMinute)
		return result, nil
	}

	text, err := client.Ask(ctx, task, fields)
	if err != nil {
		return result, fmt.Errorf("matrix: model %s: %w", req.Task, err)
	}
	result.Text = text
	return result, nil
}

// refuseAsk returns the first reason not to ask, cheapest first; it costs no request.
func (b *InProc) refuseAsk(ctx context.Context, req domain.ModelRequest, settings ModelSettings) (string, error) {
	// Room-reading tasks have no draft (and dry runs are exempt from this check).
	if strings.TrimSpace(req.Draft) == "" && !req.DryRun && !readsRooms(req.Task) {
		return "nothing written to work from", nil
	}
	// todo picks its own rooms and checks each (see todoFields).
	if req.Task == domain.ModelTodo {
		return "", nil
	}
	permit, err := b.permitModel(ctx, req.RoomID, settings)
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
func (b *InProc) permitModel(ctx context.Context, roomID domain.RoomID, settings ModelSettings) (domain.ModelPermit, error) {
	facts, err := b.roomFacts(ctx, roomID)
	if err != nil && (!errors.Is(err, errNoPlaces) || settings.Scope.NeedsPlaces()) {
		return domain.ModelPermit{}, err
	}
	return domain.AllowModel(settings.Scope, facts,
		settings.Endpoint, b.roomEncrypted(ctx, roomID)), nil
}

// errNoPlaces refuses the model opt-in while the cache is off: where a room sits is
// unknown, and a scope decided on a guess fails open.
var errNoPlaces = errors.New("matrix: the cache is off, so which spaces a room is in is unknown")

// roomFacts gathers what a scope entry can match: name, spaces, DM flag, and the
// network, taken from the owning space's bridge rather than from senders.
func (b *InProc) roomFacts(ctx context.Context, roomID domain.RoomID) (domain.RoomFacts, error) {
	facts := domain.RoomFacts{ID: string(roomID), Protocol: domain.ProtocolMatrix}
	if b.cache == nil {
		// No cache, no spaces: guessing "in none" would slip past an except = ["space:…"].
		return facts, errNoPlaces
	}
	rooms, err := b.cache.Rooms(ctx)
	if err != nil {
		return facts, fmt.Errorf("matrix: read rooms for the model opt-in: %w", err)
	}
	for i := range rooms {
		if rooms[i].ID == roomID {
			facts.Name, facts.Direct = rooms[i].Name, rooms[i].IsDirect
			break
		}
	}
	spaces, err := b.cache.Spaces(ctx)
	if err != nil {
		return facts, fmt.Errorf("matrix: read spaces for the model opt-in: %w", err)
	}
	for i := range spaces {
		for _, child := range spaces[i].Children {
			if child != roomID {
				continue
			}
			facts.Spaces = append(facts.Spaces, spaces[i].DisplayName())
			if spaces[i].Bridge.IsBridged() {
				facts.Protocol = spaces[i].Bridge
			}
			break
		}
	}
	return facts, nil
}

// modelFields are the template's values. The context is this room's cached messages
// only: "help me write here" is not permission to quote elsewhere.
func (b *InProc) modelFields(ctx context.Context, req domain.ModelRequest, settings ModelSettings) (map[string]string, string, error) {
	budget := settings.Budget
	if override, ok := settings.Budgets[req.Task]; ok && override > 0 {
		budget = override
	}
	var quoted []string
	var note string
	if req.Task == domain.ModelTodo {
		return b.todoFields(ctx, req, settings, budget)
	}
	if b.cache != nil {
		// A thread name reads the thread, not the room.
		if req.Task == domain.ModelThreadName {
			return b.threadNameFields(ctx, req, budget)
		}
		msgs, err := b.cache.Messages(ctx, req.RoomID, b.contextDepth(req.Task))
		if err != nil {
			return nil, "", fmt.Errorf("matrix: read context for the model: %w", err)
		}
		switch req.Task {
		case domain.ModelSummary:
			msgs, note = b.summarySpan(ctx, req.RoomID, msgs, req.Span)
		default:
			// Sentence tasks see the exchange they belong to, within the budget.
			msgs = domain.CompletionContext(msgs, req.ReplyTo, settings.Pick)
		}
		quoted = domain.ModelContext(msgs, b.accountID(), budget)
	}
	// A summary's language comes from the conversation (there is no draft).
	language := modelLanguage(req.Draft)
	if req.Task == domain.ModelSummary {
		language = modelLanguage(strings.Join(quoted, "\n"))
	}
	return b.promptFields(ctx, req.Draft, strings.Join(quoted, "\n"), language, req.Instruction), note, nil
}

// promptFields is the template's value map.
func (b *InProc) promptFields(ctx context.Context, draft, context, language, instruction string) map[string]string {
	return map[string]string{
		"{draft}":       draft,
		"{context}":     context,
		"{language}":    language,
		"{instruction}": instruction,
		"{me}":          b.accountName(ctx),
		"{names}":       b.accountNames(ctx),
	}
}

// summarySpan is what a summary reads and a note saying which span it is: an explicit
// span if asked, else what you missed, else (too little missed, e.g. the room you are
// looking at) the whole cached tail.
func (b *InProc) summarySpan(
	ctx context.Context, roomID domain.RoomID, msgs []domain.Message, asked domain.SummarySpan,
) ([]domain.Message, string) {
	// An explicit span wins even when empty.
	if asked.Given() {
		chosen := domain.MessagesIn(msgs, asked.Since, asked.Count)
		return chosen, fmt.Sprintf("%d messages from %s", len(chosen), asked.Said)
	}
	missed := domain.MessagesAfter(msgs, b.readMarker(ctx, roomID))
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
func (b *InProc) threadNameFields(
	ctx context.Context, req domain.ModelRequest, budget int,
) (map[string]string, string, error) {
	msgs, err := b.cache.ThreadMessages(ctx, req.RoomID, req.ReplyTo, threadNameMessages)
	if err != nil {
		return nil, "", fmt.Errorf("matrix: read thread for the model: %w", err)
	}
	quoted := domain.ModelContext(msgs, b.accountID(), budget)
	joined := strings.Join(quoted, "\n")
	// The thread's language: the name is persisted, so a wrong one would stick.
	return b.promptFields(ctx, "", joined, modelLanguage(joined), ""), "", nil
}

// contextDepth is how many messages to read before the budget trims them.
func (b *InProc) contextDepth(task string) int {
	if task == domain.ModelSummary {
		return summaryContextMessages
	}
	return modelContextMessages
}

// readMarker is how far this account has read in a room ("" when unknown).
func (b *InProc) readMarker(ctx context.Context, roomID domain.RoomID) domain.EventID {
	if b.cache == nil {
		return ""
	}
	unread, err := b.cache.Unread(ctx)
	if err != nil {
		b.warnIf(ctx, err, "read cached read marker", "room", roomID)
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

// ReplaceDraft stores (or, when empty, removes) a room's draft in the cache, only
// while the stored one is still over.
func (b *InProc) ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (bool, error) {
	return fromCache(b, "replace draft", func(c *db.Cache) (bool, error) {
		return c.ReplaceDraft(ctx, draft, over)
	})
}

// Drafts returns every stored draft.
func (b *InProc) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	return fromCache(b, "read drafts", func(c *db.Cache) ([]domain.StoredDraft, error) {
		return c.Drafts(ctx)
	})
}

// RoomsWith finds rooms all these people are in, from the cache.
func (b *InProc) RoomsWith(ctx context.Context, userIDs []string, rooms domain.RoomSet, limit int) ([]domain.Room, error) {
	return fromCache(b, "rooms with", func(c *db.Cache) ([]domain.Room, error) {
		return c.RoomsWith(ctx, userIDs, rooms, limit)
	})
}

// MessagesAround is one cached message with its neighbors.
func (b *InProc) MessagesAround(
	ctx context.Context, roomID domain.RoomID, event domain.EventID, before, after int,
) ([]domain.Message, error) {
	return fromCache(b, "messages around", func(c *db.Cache) ([]domain.Message, error) {
		return c.MessagesAround(ctx, roomID, event, before, after)
	})
}

// RoomEncryption reports which of these rooms are encrypted, erring towards yes.
func (b *InProc) RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, roomID := range roomIDs {
		out[roomID] = b.roomEncrypted(ctx, roomID)
	}
	return out, nil
}

// todoFields quotes what unread rooms are waiting on, for the todo task. Only rooms
// with something unread, each checked against the scope as it is chosen, sharing
// the budget.
func (b *InProc) todoFields(
	ctx context.Context, req domain.ModelRequest, settings ModelSettings, budget int,
) (map[string]string, string, error) {
	if b.cache == nil {
		return nil, "", nil
	}
	unread, err := b.roomsWithSomethingUnread(ctx)
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
		permit, perr := b.permitModel(ctx, unread[i].RoomID, settings)
		if perr != nil {
			return nil, "", perr
		}
		if !permit.Allowed {
			continue
		}
		msgs, merr := b.cache.Messages(ctx, unread[i].RoomID, modelContextMessages)
		if merr != nil {
			return nil, "", fmt.Errorf("matrix: read a room for the todo list: %w", merr)
		}
		missed := domain.MessagesAfter(msgs, unread[i].ReadEvent)
		if len(missed) == 0 {
			continue
		}
		lines := domain.ModelContext(missed, b.accountID(), each)
		if len(lines) == 0 {
			continue
		}
		read++
		quoted = append(quoted,
			"## "+b.namedRoom(ctx, unread[i].RoomID), strings.Join(lines, "\n"), "")
	}
	note := fmt.Sprintf("%d rooms with something unread", read)
	if read == 0 {
		note = "nothing unread anywhere"
	}
	context := strings.Join(quoted, "\n")
	return b.promptFields(ctx, "", context, modelLanguage(context), req.Instruction), note, nil
}

// roomsWithSomethingUnread is the rooms with unread messages (local counts, falling
// back to the server's when no read position resolves), mentions then volume first.
func (b *InProc) roomsWithSomethingUnread(ctx context.Context) ([]domain.Unread, error) {
	counts, err := b.cache.CountUnreadAll(ctx, b.me())
	if err != nil {
		return nil, fmt.Errorf("matrix: count what is unread: %w", err)
	}
	positions, err := b.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("matrix: read unread positions: %w", err)
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
func (b *InProc) namedRoom(ctx context.Context, roomID domain.RoomID) string {
	if b.cache == nil {
		return string(roomID)
	}
	rooms, err := b.cache.Rooms(ctx)
	if err != nil {
		b.warnIf(ctx, err, "read cached rooms", "room", roomID)
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
func (b *InProc) accountNames(ctx context.Context) string {
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
	full := b.accountName(ctx)
	add(full)
	if first, _, multiword := strings.Cut(full, " "); multiword {
		add(first)
	}
	if local, _, found := strings.Cut(strings.TrimPrefix(b.accountID(), "@"), ":"); found {
		add(local)
	}
	return strings.Join(names, ", ")
}

// accountName is what to call this account in a prompt: its display name (what the
// conversation uses), else the localpart, else "you".
func (b *InProc) accountName(ctx context.Context) string {
	me := b.accountID()
	if me == "" {
		return "you"
	}
	if b.cache != nil {
		if name, err := b.cache.MemberName(ctx, me); err == nil && name != "" {
			return name
		}
	}
	if local, _, found := strings.Cut(strings.TrimPrefix(me, "@"), ":"); found && local != "" {
		return local
	}
	return "you"
}
