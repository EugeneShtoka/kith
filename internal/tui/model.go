// Package tui is kith's presentation layer: a Bubble Tea program that talks to
// the world only through api.Backend. The frame is a spaces rail, the room list for
// the selected rail group, and the timeline with its composer.
package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/media"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/theme"
)

// timelinePageSize is how many events one scrollback fetch requests.
const timelinePageSize = 50

// maxTimelineMessages caps a full-history backfill of one room.
const maxTimelineMessages = 20000

// pane identifies which of the three panes currently has focus.
type pane int

const (
	paneRail pane = iota
	paneRooms
	paneTimeline
	paneCount
)

// group is a rail entry: a stable key ("home", "dms", … or a space's name), a
// possibly renamed label, and a room filter.
type group struct {
	key   string
	label string
	// admits reports whether a room belongs, judged by the view it is handed, which
	// is the Model's current one: a group captures only its own shape (a space's
	// children), never state that later changes.
	admits func(unreadView, domain.Room) bool
	// sepAfter draws a divider row beneath this group (a separator token in the rail order).
	sepAfter bool
	// sticky keeps the open room listed until you move off it; first puts the row at
	// the top unless the rail order places it; countInLabel adds how many rooms it
	// holds to the label; hideWhenEmpty drops the row while it holds nothing.
	sticky, first, countInLabel, hideWhenEmpty bool
}

// railGroups builds the rail — a row per tag, then a group per space — and applies the
// user's rail config. There are no rows of the client's own: All, Unread, Archived and
// the rest are tags the config defines. A rail with nothing to show gets one All row,
// so the room list is never out of reach.
func railGroups(
	spaces []domain.Space,
	cfg config.Rail,
	// names are the user's display names for rooms, threads and rail rows.
	names []config.DisplayName,
	view unreadView,
	rooms []domain.Room,
) []group {
	groups := tagGroups(view)
	for _, s := range spaces {
		children := make(map[domain.RoomID]bool, len(s.Children))
		for _, id := range s.Children {
			children[id] = true
		}
		name, spaceID := s.DisplayName(), s.ID
		// A space a bridge keeps (a network's own grouping, a community) is where its
		// rooms belong; a space a person made is not.
		belongs := s.Keeper != "" || s.Bridge.IsBridged()
		groups = append(groups, group{
			key:   name,
			label: name,
			admits: func(v unreadView, r domain.Room) bool {
				// An invitation and spam are not in any space until dealt with.
				if !children[r.ID] || r.IsInvite() || v.isSpam(r) {
					return false
				}
				// A space-exclusive tag keeps a room only where it belongs: a bridge's
				// space, or its own canonical home.
				return !v.leavesMadeSpaces(r) || belongs || v.parents[r.ID] == spaceID
			},
		})
	}
	if len(groups) == 0 {
		groups = []group{fallbackGroup()}
	}
	groups = applyRailConfig(groups, cfg, names, view, rooms)
	return withoutTrailingSeparator(promoteFirst(groups, cfg))
}

// fallbackGroupKey is the rail key of the one row a rail with no spaces and no tags
// gets.
const fallbackGroupKey = "*all"

// fallbackGroup is every room but invitations and spam: what a config with no tags
// and an account with no spaces still needs to reach its rooms.
func fallbackGroup() group {
	return group{
		key:    fallbackGroupKey,
		label:  "All",
		admits: func(v unreadView, r domain.Room) bool { return !r.IsInvite() && !v.isSpam(r) },
	}
}

// withoutTrailingSeparator drops a divider under the last group, e.g. from
// `["*", "-", "tag:Archived"]` on a day nothing is archived.
func withoutTrailingSeparator(groups []group) []group {
	if n := len(groups); n > 0 && groups[n-1].sepAfter {
		groups[n-1].sepAfter = false
	}
	return groups
}

// rebuiltRail rebuilds the rail from the model's state, keeping the cursor on the same group.
func (m Model) rebuiltRail() Model {
	prevKey := m.rail.key()
	m.rail.groups = railGroups(m.rooms.spaces, m.prefs.display.Rail, m.prefs.display.Names, m.unreadView(), m.rooms.all)
	m.rail.cursor = indexOfGroup(m.rail.groups, prevKey)
	return m
}

// startupPrefs is the settings New starts with, before a config is applied.
func startupPrefs(display config.Display, unreadLocal bool) prefsState {
	return prefsState{
		display:     display,
		identities:  buildIdentities(display.Identities),
		openInsert:  display.OpenInInsertMode(),
		unreadLocal: unreadLocal,
		// A working default even with no config.
		external: externalCommands{open: config.Clipboard{}.OpenCommandOrDefault()},
	}
}

// tagGroupKey is a tag's rail key: `tag:<name>`, as the rail order names it.
func tagGroupKey(name string) string { return domain.TagEntry(name) }

// isTagGroup reports whether a rail key is a tag's.
func isTagGroup(key string) bool {
	_, ok := domain.TagOf(key)
	return ok
}

// isSpaceGroup reports whether a rail key is a space's: neither a tag nor the
// fallback row. A space's key is its name.
func isSpaceGroup(key string) bool { return key != fallbackGroupKey && !isTagGroup(key) }

// tagGroups is a rail row per tag not hidden, in configured order, with the tag's
// properties (see unreadView.showsInTag for who it lists).
func tagGroups(view unreadView) []group {
	groups := make([]group, 0, view.tags.Len())
	for i := range view.tags.Len() {
		t := view.tags.At(i)
		if t.Hidden {
			continue
		}
		groups = append(groups, group{
			key:    tagGroupKey(t.Name),
			label:  t.Name,
			admits: func(v unreadView, r domain.Room) bool { return v.showsInTag(i, r) },
			sticky: t.Sticky, first: t.First, countInLabel: t.CountInLabel, hideWhenEmpty: t.HideWhenEmpty,
		})
	}
	return groups
}

// promoteFirst moves the rows marked first (a tag with `first`, as Invites) to the top,
// in their order, unless the rail order places them: applyRailConfig demotes unnamed
// groups, which would bury pending invitations.
func promoteFirst(groups []group, cfg config.Rail) []group {
	var front, rest []group
	for _, g := range groups {
		if g.first && !containsKey(cfg.Order, g.key) {
			front = append(front, g)
		} else {
			rest = append(rest, g)
		}
	}
	if len(front) == 0 {
		return groups
	}
	return append(front, rest...)
}

// applyRailConfig renames, hides, then reorders groups per config; hiding everything
// keeps Home so the rail is never empty. Order: named keys first, a separator token
// draws a divider after the preceding group, the wildcard stands for every unnamed
// group, and anything left keeps its default order at the end.
func applyRailConfig(groups []group, cfg config.Rail, names []config.DisplayName, view unreadView, rooms []domain.Room) []group {
	display := config.Display{Names: names}
	for i := range groups {
		if label := display.NameFor(config.GroupTarget(groups[i].key)); label != "" {
			groups[i].label = label
		}
	}

	hidden := make(map[string]bool, len(cfg.Hidden))
	for _, k := range cfg.Hidden {
		hidden[k] = true
	}
	// A tag with hide_when_empty, as if listed in [display.rail] hide_when_empty.
	for _, g := range groups {
		if g.hideWhenEmpty && !hidden[g.key] && !anyRoomIn(g, view, rooms) {
			hidden[g.key] = true
		}
	}
	// hide_when_empty drops a group whose filter matches no room.
	for _, name := range cfg.HideWhenEmpty {
		key := matchGroupKey(groups, name)
		if key == "" || hidden[key] {
			continue
		}
		if g, ok := findGroup(groups, key); ok && !anyRoomIn(g, view, rooms) {
			hidden[key] = true
		}
	}
	byKey := make(map[string]group, len(groups))
	kept := make([]group, 0, len(groups))
	for _, g := range groups {
		if hidden[g.key] {
			continue
		}
		byKey[g.key] = g
		kept = append(kept, g)
	}
	if len(kept) == 0 { // never leave the rail empty
		return groups[:1]
	}
	if len(cfg.Order) == 0 {
		return kept
	}

	return orderGroups(kept, byKey, cfg.Order)
}

// orderGroups emits the kept groups in configured order, unplaced ones last.
func orderGroups(kept []group, byKey map[string]group, order []string) []group {
	// The wildcard expands against the whole order, so a group named after it
	// (`["*", "archived"]`) is not swallowed by it.
	named := make(map[string]bool, len(order))
	for _, tok := range order {
		if !isSeparator(tok) && !isWildcard(tok) {
			named[tok] = true
		}
	}

	placed := make(map[string]bool, len(kept))
	out := make([]group, 0, len(kept))
	for _, tok := range order {
		switch {
		case isSeparator(tok):
			if len(out) > 0 {
				out[len(out)-1].sepAfter = true
			}
		case isWildcard(tok):
			out = placeUnnamed(out, kept, named, placed)
		default:
			if g, ok := byKey[tok]; ok && !placed[tok] {
				out, placed[tok] = append(out, g), true
			}
		}
	}
	for _, g := range kept {
		if !placed[g.key] {
			out = append(out, g)
		}
	}
	return out
}

// placeUnnamed appends every group the order neither names nor has placed — the wildcard's share.
func placeUnnamed(out, kept []group, named, placed map[string]bool) []group {
	for _, g := range kept {
		if !named[g.key] && !placed[g.key] {
			out, placed[g.key] = append(out, g), true
		}
	}
	return out
}

// railWildcard is the rail-order token standing for every group the order does not name.
const railWildcard = "*"

// isWildcard reports whether a rail-order token is the wildcard.
func isWildcard(tok string) bool { return tok == railWildcard }

// isSeparator reports whether a rail-order token is one or more dashes (a divider).
func isSeparator(tok string) bool { return tok != "" && strings.Trim(tok, "-") == "" }

// matchGroupKey resolves a hide_when_empty entry to a group key by key or label, case-insensitively.
func matchGroupKey(groups []group, name string) string {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return ""
	}
	for _, g := range groups {
		if strings.EqualFold(g.key, want) || strings.EqualFold(g.label, want) {
			return g.key
		}
	}
	return ""
}

// anyRoomIn reports whether a group would show anything.
func anyRoomIn(g group, view unreadView, rooms []domain.Room) bool {
	return slices.ContainsFunc(rooms, func(r domain.Room) bool { return g.admits(view, r) })
}

// findGroup returns the group with the given key.
func findGroup(groups []group, key string) (group, bool) {
	if i := slices.IndexFunc(groups, func(g group) bool { return g.key == key }); i >= 0 {
		return groups[i], true
	}
	return group{}, false
}

// scrollback is a pane's position in a conversation's history: the next page's
// token, whether the start is loaded, and whether a page is in flight.
type scrollback struct {
	// token is where the next page starts; empty asks for the newest messages.
	token string
	// atStart says the first message is loaded, so there is nothing left to ask for.
	atStart bool
	// loading is set while a page is in flight, so a second pgup does not ask twice.
	loading bool
}

// roomScrollback is the room pane's, which has two states a thread does not.
type roomScrollback struct {
	scrollback
	// ready says a live fetch returned a real pagination token (a cached preview has none).
	ready bool
	// backfilling is set while a full-history pull walks older pages to the room start.
	backfilling bool
}

// overlayTarget is what an open overlay is aimed at, captured by ID when it opens:
// the room list re-sorts as messages arrive, so reading the cursor at accept time
// could hit a different row.
type overlayTarget struct {
	// creating is the room being created by the new-room prompt.
	creating domain.NewRoom
	// member is the room an open invite or unban prompt aims at.
	member domain.RoomID
	// binding is the place an open jump-binding prompt is for.
	binding domain.JumpTarget
	// bindings are the places the shortcut prompt's tab cycles binding among.
	bindings []domain.JumpTarget
	// space is the room an open space picker was opened for.
	space domain.RoomID
	// The targets of the three rename prompts.
	renamingRoom   domain.RoomID
	renamingGroup  string
	renamingThread domain.EventID
	// ruleScopes are the scopes offered for a notification rule, rule the one chosen.
	ruleScopes []ruleTarget
	rule       ruleTarget
}

// timelineState is the open room's loaded timeline and what the reader has done in
// it: the messages and their reactions, stars and uncovered spoilers, the members to
// mention, how far back the pane has paged, the scroll and the cursor. A room change
// resets it.
type timelineState struct {
	// messages is the loaded timeline; assigned only by setMessages.
	messages []domain.Message
	// rev counts changes to messages (bumped by setMessages); it invalidates the derived cache.
	rev uint64
	// reactions is the reactions by target event. Changed through putReaction and
	// dropReaction, which tell the row cache.
	reactions map[domain.EventID][]domain.Reaction
	// starred is which messages you bookmarked.
	starred map[domain.EventID]bool
	// revealed are the messages whose spoilers and kept deleted words are uncovered. Not persisted.
	revealed map[domain.EventID]bool
	// members is the room's ranked mention candidates.
	members []domain.Member
	// opened is what was true of the room when it was opened. See openedRoom.
	opened openedRoom
	// hist is where the room pane sits in the conversation's history.
	hist roomScrollback
	// scroll is how many rows of the newest messages are hidden below the viewport; 0 follows live.
	scroll int
	// selected is the focused message in normal mode (empty = the newest).
	selected domain.EventID
	// layout is the open room's side (direction.go).
	layout roomLayout
}

// prefsState is the settings resolved from the config each time it is applied (see
// applyConfig): read everywhere, written nowhere else.
type prefsState struct {
	display config.Display
	// identities maps each merged MXID to its resolved person, from display.Identities.
	identities map[string]resolvedIdentity
	// roomAliases maps a room ID to its configured display name.
	roomAliases map[domain.RoomID]string
	// threadAliases maps a thread root to its given name (a thread's default label is a snippet).
	threadAliases map[domain.EventID]string
	// tracked is the tracked-word rules. See trackedIn.
	tracked []domain.TrackedRule
	// unreadLocal counts unread messages (the daemon's count) rather than notifying ones.
	// See domain.Unread.Count.
	unreadLocal bool
	// codes is what counts as a verification code and where to look. See codes.go.
	codes codeSettings
	// external is the [clipboard] commands for copying and opening links. See externalCommands.
	external externalCommands
	// openInsert is display.open_in_insert_mode.
	openInsert bool
}

// configState is the config file a session edits: where it lives, the whole of it
// (so a save keeps what this app does not edit), the writer that keeps saves in order,
// and a count of live reloads.
type configState struct {
	// path is where settings are written back; empty is not saved.
	path string
	// writer keeps saves to path in order; shared by every copy of the Model.
	writer *configWriter
	base   config.Config
	// rev counts live config reloads, which also invalidate derived answers.
	rev uint64
	// applied counts the changes applied, so one kithd was still checking can tell
	// whether it would land over a newer one (applyConfig).
	applied uint64
}

// Model is the root Bubble Tea model driving the three-pane frame.
type Model struct {
	// motion is the count typed before a motion and the one it took. See motionCount.
	motion motionCount

	backend api.Backend
	ctx     context.Context //nolint:containedctx // the model owns the app-lifetime context for backend calls
	// log is the client's log file (never the terminal); safe to use from a tea.Cmd.
	log   *slog.Logger
	theme theme.Theme
	// namedThreads is which threads the model has asked to be named, so a request is
	// never armed twice.
	namedThreads map[domain.EventID]bool

	width, height int
	ready         bool // a WindowSizeMsg has arrived, so we know the frame size
	focus         pane

	// rail is the space rail. See railState.
	rail railState

	// rooms is the joined rooms, invitations, their union and the space hierarchy. See inventory.go.
	rooms inventory
	// link is the daemon streams' state; it only drives the badge. See linkState.
	link linkState
	// openRoom is the ID of the room whose timeline is shown; tracked by ID so reindexing cannot move it.
	openRoom domain.RoomID

	// drafts are the unsent compositions of rooms other than the open one. See draft.go.
	drafts map[domain.RoomID]draft

	// quotes are reply targets that are not among the loaded messages. See quotes.go.
	quotes quoteState

	// edits is each text field's undo/redo history; the editor itself is rebuilt from
	// the stored text on every key. See editHistory.
	edits [fieldHelpFilter + 1]editHistory

	// jumps is back/forward over rooms (session state). See jumplist.go.
	jumps jumplist

	// unread is each room's unread state, seeded from cache and updated live. Mutated in place.
	unread map[domain.RoomID]domain.Unread
	// lastMessage is when each room last had a message, for activity ordering. A room
	// with nothing cached is absent, not zero.
	lastMessage map[domain.RoomID]time.Time
	// loadArmed counts room selections, so a load armed for a room the cursor left is dropped.
	loadArmed int
	// running is the user command in flight, blocking a second one. See runcmd.go.
	running string
	// live is what other people are doing right now (typing, reading); never persisted.
	live liveActivity
	// sentTyping is the typing notice we last sent. See typingNotice.
	sentTyping typingNotice
	// parents maps a room to the space it lives in, resolved on demand for archived
	// rooms.
	parents map[domain.RoomID]domain.SpaceID

	// glyphs is the emoji vocabulary and its ranking. See emojiState.
	glyphs emojiState
	// pics is the attachment display state. See mediaState.
	pics mediaState
	// player is the loaded voice note. See player.go.
	player playerState

	// thread is the conversation the timeline pane is showing, if any. See threadState.
	thread threadState
	// timeline is the open room's loaded messages and where the reader is in them.
	// See timelineState.
	timeline timelineState
	// completion is the open completion popup.
	completion completionState

	// derived caches the full-history passes the layout needs. A pointer so it survives
	// Update's value copy; see derived.go. Update writes it (row cache, color slots) and
	// View only reads it, and only for the Model it was primed for (frame); the vendored
	// Bubble Tea fork calls View between Updates on their goroutine — asserted by
	// TestRenderRunsOnTheUpdateGoroutine, while cmdreach_test.go checks no tea.Cmd
	// reaches it.
	derived *derivedCache
	// frames and frame say whether the shared cache is final for this Model (see
	// frameClock): View reads it only when it is.
	frames *frameClock
	frame  uint64
	// receipts is the read-receipt policy and what has been sent. See receiptState.
	receipts receiptState

	// compose is the composer's text, caret and send context. See composeState.
	compose composeState
	// st is the status line's left slot, written only via say/doing/clearStatus so events expire.
	st statusState

	// me is our own MXID ("" without Matrix), and selves every ID the daemon says is
	// this person (see isMe). Notifications are the daemon's; the TUI keeps the rules
	// only to describe them.
	me     string
	selves []string
	// roomAccounts is each network account the room list has had rooms from (see
	// selvesAfterRooms); nil before the first list.
	roomAccounts []string
	// notifications is the notification rules, delivery switch and do-not-disturb state. See dnd.go.
	notifications notifyState

	// pendingSave is the attachment waiting on the folder chooser. See attach.go.
	pendingSave pendingSave

	// verify holds the in-progress device-verification overlay, if any.
	verify verifyState
	// login is the :login sign-in under way. See login.go.
	login loginState

	// entering is a just-created room to open as soon as a refresh lists it.
	entering domain.RoomID
	// dmCandidates is who this account could start a DM with, refreshed when the switcher opens.
	dmCandidates []domain.Member
	// sweepArmed says a status sweep is already scheduled.
	sweepArmed bool
	// aimedAt is what the open overlay is aimed at. See overlayTarget.
	aimedAt overlayTarget
	// schedules queues messages for later; nil without a daemon, where the commands refuse.
	schedules Schedules
	// picker is the open chooser overlay; choosing what its answer is for. See pickerWalk.
	picker   picker
	choosing pickerWalk
	// pager is a script's output, held while its reader is up.
	pager pagerState
	// script is the user command waiting on the context it declared. See script.go.
	script pendingScript
	// conf is the config file this session edits. See configState.
	conf configState
	// prefs is what the config says, resolved when it is applied. See prefsState.
	prefs prefsState
	// offers is the one-time download offers. See oneTimeOffers.
	offers oneTimeOffers
	// spell is the composer's spelling state. See spell.go.
	spell spellState
	// assist is the word completion in progress. See assist.go.
	assist assistState
	// draftSync debounces draft writes. See draftSaver.
	draftSync draftSaver
	// phrases is the open room's trigram index, rebuilt when messages change. See assist.go.
	phrases phraseIndex
	// model is the optional language-model suggestion state; the zero value is off. See assist.go.
	model modelState
	// history is the open message-history view. See history.go.
	history historyState
	// followAt is a link this client was started to follow (`kith --open`), held until rooms load.
	followAt string
	// plainSend makes the next send skip Markdown (`/plain`); cleared by it.
	plainSend bool
	// walk is the open spelling-correction walk. See spellwalk.go.
	walk spellWalk

	// confirm is a destructive action awaiting a yes, prompt the one-line prompt, search the results pane.
	confirm confirmState
	prompt  promptState
	search  searchState
	// rows is the room list's thread rows. See roomrows.go.
	rows threadRows
	// frameRows is the room list built once for one View: set on View's own copy of
	// the Model, so no Update ever sees it (asModel's check in the tests).
	frameRows    []roomRow
	hasFrameRows bool

	// jump is a message to select once the timeline contains it. See jumpTarget.
	jump jumpTarget

	// chord is the key sequence in progress; only set where keys are commands, and any
	// key that cannot continue it ends it.
	chord []string
	// keys resolves a keypress to an action per mode; the `?` overlay is generated from it.
	keys keymap
	// reader is the read-and-dismiss overlay (help, why, …). See reader.go.
	reader readerState
}

// verifyState is the device-verification overlay; stage is the request prompt or SAS comparison.
type verifyState struct {
	active bool
	txnID  string
	stage  domain.VerificationKind
	// ours marks a verification this session requested: nothing to accept, only cancel.
	ours     bool
	from     string
	device   string
	emojis   []domain.SASEmoji
	decimals []int
	waiting  bool // an accept/confirm is in flight, awaiting the next step
}

// motionCount is the digits typed before a motion (count) and what the motion took
// (repeat, `12j`). Kept apart so an abandoned count cannot multiply a later key.
type motionCount struct {
	count  int
	repeat int
}

// openedRoom is what was true of the open room when it was opened.
type openedRoom struct {
	// wasUnread is the open room if it was unread when opened, keeping it in the unread band while read.
	wasUnread domain.RoomID
	// unreadFrom is the last read message when the room was opened — a snapshot, so the
	// unread line does not walk down as receipts move.
	unreadFrom domain.EventID
}

// oneTimeOffers is the one-time download offers.
type oneTimeOffers struct {
	// dictionaries is the spelling-dictionary offer. See dictionaries.go.
	dictionaries dictionaryOffer
	// model is the local completion model offer. See completionmodel.go.
	model modelOffer
}

// resolvedIdentity is a merged person: group key, alias (empty = none) and optional pinned color.
type resolvedIdentity struct {
	key    string
	alias  string
	color  color.Color
	pinned bool
}

// New returns a Model wired to backend, using ctx for all backend calls.
func New(ctx context.Context, backend api.Backend, display config.Display) Model {
	// unread is shared with the rail's Unread filter, so it is never reassigned.
	unread := make(map[domain.RoomID]domain.Unread)
	mediaMode := startupMediaMode(display)
	reactScope := display.Reactions.Scope
	if reactScope == "" {
		reactScope = "room"
	}
	static, tone, tier := startupEmoji(display)
	// Validated at startup; an error here means a config built in code.
	source, _ := setup.UnreadSource(display.Unread)
	unreadLocal := source != config.UnreadNotifications
	parents := make(map[domain.RoomID]domain.SpaceID)
	// Naming and media are set by applyNaming/applyMedia below, the path every settings change uses.
	m := Model{
		backend:      backend,
		log:          logging.Discard(),
		ctx:          ctx,
		theme:        theme.New(paletteOf(display)),
		prefs:        startupPrefs(display, unreadLocal),
		unread:       unread,
		live:         newLiveActivity(),
		rows:         newThreadRows(),
		timeline:     timelineState{reactions: map[domain.EventID][]domain.Reaction{}},
		namedThreads: map[domain.EventID]bool{},
		glyphs: emojiState{
			skin:    tone,
			set:     newEmojiSet(tier, display.Emoji.Extra),
			orders:  map[domain.RoomID]emojiOrder{},
			scope:   reactScope,
			static:  static,
			palette: static,
		},
		pics: mediaState{
			mode:         mediaMode,
			graphics:     resolveGraphics(mediaMode),
			blocks:       resolveBlocks(display.Media.Detail),
			imageRows:    map[domain.EventID][]string{},
			imageLoading: map[domain.EventID]bool{},
		},
		keys:     newKeymap(config.DefaultKeys()),
		focus:    paneRail,
		derived:  newDerivedCache(),
		frames:   &frameClock{},
		receipts: receiptState{policy: readSettingsFrom(display)},
		parents:  parents,
		// Tags arrive with the config (WithConfigFile): until then, the fallback row.
		rail: railState{groups: []group{fallbackGroup()}, tagMemo: &tagMemo{}},
		st:   statusState{standing: "loading rooms…"},
	}
	m = m.applyNaming(display)
	return m.applyMedia(display.Media)
}

// startupEmoji resolves the react palette (toned), the skin tone and the emoji tier.
// Validated at startup, so errors fall back to the defaults.
func startupEmoji(display config.Display) (static []string, tone string, tier string) {
	static = cappedStatic(display.Reactions.Static)
	tone, _ = setup.SkinTone(display.SkinTone)
	tier, _ = setup.EmojiTier(display.Emoji.Set)
	return toneEach(static, tone), tone, tier
}

// startupMediaMode resolves how attachments are drawn; an invalid mode (only possible
// from a config built in code) draws placeholders.
func startupMediaMode(display config.Display) string {
	mode, err := setup.MediaMode(display.Media.Mode)
	if err != nil {
		return mediaPlaceholder
	}
	return mode
}

// buildIdentities indexes each configured identity by its MXIDs. The alias (else the
// first MXID) is the shared color key; a valid color pins it.
func buildIdentities(ids []config.Identity) map[string]resolvedIdentity {
	out := make(map[string]resolvedIdentity)
	for i, id := range ids {
		key := id.Alias
		if key == "" && len(id.IDs) > 0 {
			key = id.IDs[0]
		}
		if key == "" {
			key = fmt.Sprintf("identity-%d", i)
		}
		ri := resolvedIdentity{key: "id:" + key, alias: id.Alias}
		if c, ok := theme.ParseColor(id.Color); ok {
			ri.color, ri.pinned = c, true
		}
		for _, mxid := range id.IDs {
			out[mxid] = ri
		}
	}
	return out
}

// buildRoomAliases indexes room display names by room ID (targets that are one).
func buildRoomAliases(names []config.DisplayName) map[domain.RoomID]string {
	out := make(map[domain.RoomID]string, len(names))
	for _, entry := range names {
		if entry.Name == "" || !domain.IsRoomID(entry.Target) {
			continue
		}
		out[domain.RoomID(entry.Target)] = entry.Name
	}
	return out
}

// buildThreadAliases indexes thread names by root event, skipping half-written entries.
func buildThreadAliases(names []config.DisplayName) map[domain.EventID]string {
	out := make(map[domain.EventID]string, len(names))
	for _, entry := range names {
		root, ok := strings.CutPrefix(entry.Target, config.NameTargetThread)
		if !ok || entry.Name == "" || root == "" {
			continue
		}
		out[domain.EventID(root)] = entry.Name
	}
	return out
}

// roomName is a room's label isolated for embedding in a sentence (see isolate).
// Things not drawn — folder names, config matches, sort keys — use roomLabel.
func (m Model) roomName(room domain.Room) string {
	return isolate(m.roomLabel(room))
}

// roomLabel is a room's logical-order label: its alias, else its name, first-named
// per its own space's rule when the room is named after its members.
func (m Model) roomLabel(room domain.Room) string {
	return m.labeled(room, m.nameRuleFor(m.ownSpace(room.ID)))
}

// roomLabelHere is roomLabel for a room-list row, where the rule comes from the
// space being viewed so a column is uniform. See firstNameOnlyHere.
func (m Model) roomLabelHere(room domain.Room) string {
	return m.labeled(room, m.nameRuleFor(m.listedSpace(room.ID)))
}

// labeled applies an alias, else rule-based shortening of a room named after its members.
func (m Model) labeled(room domain.Room, rule nameRule) string {
	if alias, ok := m.prefs.roomAliases[room.ID]; ok {
		return alias
	}
	name := room.DisplayName()
	if short, ok := rule.within(name, room.Members); ok {
		return short
	}
	return name
}

// shortenByMembers first-names each member within the name, only if the name is
// entirely member names plus list punctuation. Longest members match first so an
// overlapping name is not partially replaced.
func shortenByMembers(name string, members []string) (string, bool) {
	ordered := append([]string(nil), members...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })

	shortened, residue, matched := name, name, false
	for _, mem := range ordered {
		if mem == "" || !strings.Contains(residue, mem) {
			continue
		}
		matched = true
		shortened = strings.Replace(shortened, mem, firstName(mem), 1)
		residue = strings.Replace(residue, mem, "", 1)
	}
	if !matched || peopleNameResidue(residue) != "" {
		return name, false
	}
	return shortened, true
}

// peopleNameResidue is what remains after member names are removed, minus list
// punctuation (",", "and", "N others"). Non-empty means the name is a title.
func peopleNameResidue(s string) string {
	fields := strings.Fields(strings.ReplaceAll(s, ",", " "))
	kept := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		if fields[i] == "and" {
			continue
		}
		// Drop a trailing "N others"/"N other" count from FormatHeroes.
		if isAllDigits(fields[i]) && i+1 < len(fields) &&
			(fields[i+1] == "others" || fields[i+1] == "other") {
			i++
			continue
		}
		kept = append(kept, fields[i])
	}
	return strings.Join(kept, " ")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Init renders from the cache, starts background refreshes and the sync streams.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadRoomsCmd(), m.refreshRoomsCmd(),
		m.loadSpacesCmd(), m.refreshSpacesCmd(),
		m.loadUnreadCmd(), m.loadInvitesCmd(),
		m.startSyncCmd(), m.listenCmd(), m.listenVerifyCmd(), m.listenUnreadCmd(),
		m.listenReactionsCmd(), m.listenInvitesCmd(), m.listenActivityCmd(),
		m.listenFollowCmd(), m.listenSeatCmd(),
		m.loadDraftsCmd(),
		m.loadSpamCmd(),
		m.readDNDCmd(), m.pollTickCmd(), m.refusalsCmd(),
		m.listenAttachedCmd(),
		m.lastMessagesCmd(),
		m.loadSelvesCmd(),
	)
}

// Update routes a message to its handler, then runs the steps every state change
// needs (jumplist, derived cache, arming timers, quotes, inline pictures) once here
// rather than at each call site.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.route(msg)
	next = next.notedArrival()
	cmds := []tea.Cmd{cmd}
	// Each arm* step compares state once per message, instead of a hook at every
	// place the composer text or status can change. tea.Batch drops nil commands.
	for _, arm := range []func(Model) (Model, tea.Cmd){
		Model.armStatusSweep, Model.armSpellCheck, Model.armCompletion, Model.armModelAsk,
	} {
		var c tea.Cmd
		next, c = arm(next)
		cmds = append(cmds, c)
	}
	next = next.armPhrases()
	var c tea.Cmd
	next, c = next.armDraftSave()
	cmds = append(cmds, c)
	next, c = next.loadQuotes()
	cmds = append(cmds, c)
	if next.pics.graphics != graphicsNone {
		next, c = next.loadInlineImages()
		cmds = append(cmds, c)
	}
	// Settle what the timeline caches here, not in View: rendering only reads. After
	// every step that can change what it reads, so the frame draws what was primed.
	next = next.primeTimeline()
	// Last: the cache is now final for exactly this Model (see View).
	if next.frames != nil {
		next.frame = next.frames.stamp()
	}
	return next, tea.Batch(cmds...)
}

// route dispatches to the mode-specific handlers.
func (m Model) route(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)
	case tea.PasteMsg:
		// A bracketed paste arrives as one message.
		return m.handlePaste(msg.Content)
	case tea.ClipboardMsg:
		// The terminal's answer to ctrl+v's clipboard request.
		return m.handlePaste(msg.Content)
	default:
		return m.handleDataMsg(msg)
	}
}

// notedArrival records the open room in the jumplist whenever the timeline has
// focus, covering every way of opening a room. Idempotent.
func (m Model) notedArrival() Model {
	if m.focus != paneTimeline || m.openRoom == "" {
		return m
	}
	m.jumps = m.jumps.arrived(m.openRoom)
	return m
}

// handleDataMsg routes backend and command results to their handlers.
func (m Model) handleDataMsg(msg tea.Msg) (Model, tea.Cmd) {
	if mdl, cmd, handled := m.handleRoomListMsg(msg); handled {
		return mdl, cmd
	}
	if mdl, cmd, handled := m.handleAppMsg(msg); handled {
		return mdl, cmd
	}
	return m.handleContentMsg(msg)
}

// handleAppMsg handles outcomes of things done on the user's behalf: settings written,
// links opened, the sync loop ending.
func (m Model) handleAppMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	if mdl, cmd, handled := m.handleComposeMsg(msg); handled {
		return mdl, cmd, true
	}
	switch msg := msg.(type) {
	case spaceFiledMsg:
		return answered(m.handleSpaceFiled(msg))
	case memberChangedMsg:
		return answered(m.handleMemberChanged(msg))
	case roomCreatedMsg:
		return answered(m.handleRoomCreated(msg))
	case configCheckedMsg, configSavedMsg, configReloadedMsg:
		return answered(m.handleConfigNews(msg))
	case loginMsg, loginNetworksMsg:
		return answered(m.handleLogin(msg))
	case dndMsg:
		return answered(m.handleDND(msg))
	case markedReadMsg:
		return answered(m.handleMarkedRead(msg))
	case markedUnreadMsg:
		return answered(m.handleMarkedUnread(msg))
	case roomThreadsMsg:
		return answered(m.handleRoomThreads(msg))
	case threadNamedMsg:
		return answered(m.handleThreadNamed(msg))
	case threadPageMsg:
		return answered(m.handleThreadPage(msg))
	case openedMsg:
		return m.sayFailure("could not open the link: ", msg.err), nil, true
	case focusedMsg:
		// The link opened; only raising the browser failed.
		return m.sayFailure("opened it, but could not go to the browser: ", msg.err), nil, true
	case attachedMsg:
		return answered(m.handleAttached(msg))
	case syncEndedMsg:
		return answered(m.handleSyncEnded(msg))
	default:
		if mdl, cmd, handled := m.handleAttachmentMsg(msg); handled {
			return mdl, cmd, true
		}
		return m.handleHousekeepingMsg(msg)
	}
}

// sayFailure reports err after prefix, and does nothing for a nil err.
func (m Model) sayFailure(prefix string, err error) Model {
	if err == nil {
		return m
	}
	m.logErr(slog.LevelWarn, strings.TrimSuffix(strings.TrimSpace(prefix), ":"), err)
	return m.say(prefix + err.Error())
}

// handleComposeMsg handles results about writing: external editor, colors, commands, schedules, spelling.
func (m Model) handleComposeMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case externalEditMsg:
		return answered(m.handleExternalEdit(msg))
	case readTimerMsg:
		return answered(m.handleReadTimer(msg))
	case senderSlotsMsg:
		return answered(m.handleSenderSlots(msg))
	case userCommandsMsg:
		return answered(m.handleUserCommands(msg))
	case scheduledMsg:
		return answered(m.handleScheduled(msg))
	case scheduleDoneMsg:
		return answered(m.handleScheduleDone(msg))
	case queuedUnsentMsg:
		return answered(m.handleQueuedUnsent(msg))
	case spellTickMsg:
		return answered(m.handleSpellTick(msg))
	case spellCheckedMsg:
		return answered(m.handleSpellChecked(msg))
	case spellLearnedMsg:
		return answered(m.handleSpellLearned(msg))
	case spellBeforeSendMsg:
		return answered(m.handleSpellBeforeSend(msg))
	default:
		return m.handleAssistMsg(msg)
	}
}

// handleAssistMsg handles writing aids and drafts: completion, the language model, spam and drafts.
func (m Model) handleAssistMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case assistTickMsg:
		return answered(m.handleAssistTick(msg))
	case assistDoneMsg:
		return answered(m.handleAssistDone(msg))
	case draftTickMsg:
		return answered(m.handleDraftTick(msg))
	case draftSavedMsg:
		return answered(m.handleDraftSaved(msg))
	case spamLoadedMsg:
		return answered(m.handleSpamLoaded(msg))
	case spamReleasedMsg:
		return answered(m.handleSpamReleased(msg))
	case draftsLoadedMsg:
		return answered(m.handleDraftsLoaded(msg))
	case playerControlMsg:
		return answered(m.handlePlayerControl(msg))
	case seatLostMsg:
		return answered(m.handleSeatLost())
	case draftFileMsg:
		return answered(m.handleDraftFile(msg))
	case quitNowMsg:
		return answered(m, tea.Quit)
	case summaryMsg:
		return answered(m.handleSummary(msg))
	case todoMsg:
		return answered(m.handleTodo(msg))
	case modelPreviewMsg:
		return answered(m.handleModelPreview(msg))
	case modelTickMsg:
		return answered(m.handleModelTick(msg))
	case modelDoneMsg:
		return answered(m.handleModelDone(msg))
	default:
		return m, nil, false
	}
}

// handleAttachmentMsg handles file results: chosen, sent, saved, viewed, played.
func (m Model) handleAttachmentMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case fileChosenMsg:
		return answered(m.handleFileChosen(msg))
	case folderChosenMsg:
		return answered(m.handleFolderChosen(msg))
	case attachSentMsg:
		return answered(m.handleAttachSent(msg))
	case downloadedMsg:
		return answered(m.handleDownloaded(msg))
	case mediaViewedMsg:
		return answered(m.handleMediaViewed(msg))
	case videoPlayedMsg:
		return answered(m.handleVideoPlayed(msg))
	case fileOpenedMsg:
		return answered(m.handleFileOpened(msg))
	case audioReadyMsg:
		return answered(m.handleAudioReady(msg))
	default:
		return m, nil, false
	}
}

// handleHousekeepingMsg handles the client's own timers and records.
func (m Model) handleHousekeepingMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case statusTickMsg:
		return answered(m.handleStatusTick())
	case refusalsMsg:
		return answered(m.handleRefusals(msg))
	case emojiScoresMsg:
		return answered(m.handleEmojiScores(msg))
	case pollTickMsg:
		return answered(m.handlePollTick())
	case playerTickMsg:
		// Forces the repaint that moves the playback bar.
		return answered(m.handlePlayerTick())
	case playerCloseMsg:
		// One-shot at the end of a note.
		return answered(m.handlePlayerClose(msg))
	default:
		return m, nil, false
	}
}

// handleRoomListMsg handles messages that reshape the room list and the rail. handled
// is false for anything else.
func (m Model) handleRoomListMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case activityMsg:
		return answered(m.handleActivity(msg))
	case parentsMsg:
		return answered(m.handleParents(msg))
	case roomsMsg:
		return answered(m.handleRoomsThenOffer(msg))
	case verifyStartedMsg:
		return answered(m.handleVerifyStarted(msg))
	case dictionaryOfferMsg:
		return answered(m.handleDictionaryOffer(msg))
	case modelOfferMsg:
		return answered(m.handleModelOffer(msg))
	case modelInstalledMsg:
		return answered(m.handleModelInstalled(msg))
	case frequenciesInstalledMsg:
		return answered(m.handleFrequenciesInstalled(msg))
	case dictionariesInstalledMsg:
		return answered(m.handleDictionariesInstalled(msg))
	case spacesMsg:
		return answered(m.handleSpaces(msg))
	case unreadMsg:
		return answered(m.handleUnread(msg))
	case unreadUpdateMsg:
		return answered(m.handleUnreadUpdate(msg))
	case invitesMsg:
		return answered(m.handleCachedInvites(msg))
	case searchResultsMsg:
		return answered(m.handleSearchResults(msg))
	case membersMsg:
		return answered(m.handleMembers(msg))
	case inviteUpdateMsg:
		return answered(m.handleInviteUpdate(msg))
	case joinedMsg:
		return answered(m.handleJoined(msg))
	case leftMsg:
		return answered(m.handleLeft(msg))
	default:
		return m, nil, false
	}
}

// handleRoomsThenOffer handles the room list and, on the first one, starts the chain
// of one-time offers.
func (m Model) handleRoomsThenOffer(msg roomsMsg) (Model, tea.Cmd) {
	next, cmd := m.handleRooms(msg)
	next, offer := next.maybeOfferDictionaries()
	next, selves := next.selvesAfterRooms(msg.rooms)
	return next, tea.Batch(cmd, offer, selves)
}

// handleSideMsg handles account-wide loads, verification, and reactions and images
// on messages already shown.
func (m Model) handleSideMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case directCandidatesMsg:
		return answered(m.handleDirectCandidates(msg))
	case selvesMsg:
		return answered(m.handleSelves(msg))
	case searchSendersMsg:
		return answered(m.handleSearchSenders(msg))
	case lastMessagesMsg:
		return answered(m.handleLastMessages(msg))
	case verifyMsg:
		return answered(m.handleVerify(msg))
	case verifyFailedMsg:
		return answered(m.handleVerifyFailed(msg))
	case cachedReactionsMsg:
		return answered(m.handleCachedReactions(msg))
	case reactionUpdateMsg:
		return answered(m.handleReactionUpdate(msg))
	case imageLoadedMsg:
		return answered(m.handleImageLoaded(msg))
	}
	return m, nil, false
}

// handleContentMsg handles messages about the open room's contents, plus verification.
func (m Model) handleContentMsg(msg tea.Msg) (Model, tea.Cmd) {
	if mdl, cmd, handled := m.handleStarMsg(msg); handled {
		return mdl, cmd
	}
	if mdl, cmd, handled := m.handleSideMsg(msg); handled {
		return mdl, cmd
	}
	switch msg := msg.(type) {
	case incomingMsg:
		return m.handleIncoming(msg)
	case timelineMsg:
		return m.handleTimeline(msg)
	case cachedTimelineMsg:
		return m.handleCachedTimeline(msg)
	case fetchedQuoteMsg:
		return m.handleFetchedQuote(msg)
	case fetchedEventMsg:
		return m.handleFetchedEvent(msg)
	case followMsg:
		return m.handleFollow(msg)
	case historyMsg:
		return m.handleHistory(msg)
	case commandRanMsg:
		return m.handleCommandRan(msg)
	case roomLoadMsg:
		return m.handleRoomLoad(msg)
	case redactedMsg:
		return m.handleRedacted(msg)
	case editedMsg:
		return m.handleEdited(msg)
	case sentMsg:
		return m.handleSent(msg)
	case reactionSentMsg:
		return m.handleReactionSent(msg)
	case receiptSentMsg:
		return m.handleReceiptSent(msg)
	default:
		return m, nil
	}
}

// firstKey handles the keys checked before anything can capture one: interrupt quits
// from any mode, and paste asks the terminal for its clipboard (for terminals that
// pass the key through).
func (m Model) firstKey(key tea.KeyPressMsg) (tea.Cmd, bool) {
	switch m.keys.lookup(key.String(), scopeGlobal) {
	case actInterrupt:
		return tea.Quit, true
	case actPaste:
		return tea.ReadClipboard, true
	}
	return nil, false
}

func (m Model) handleKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	if cmd, handled := m.firstKey(key); handled {
		return m, cmd
	}
	if mdl, cmd, captured := m.handleCapturedKey(key); captured {
		return mdl, cmd
	}
	// The completion popup is asked before global bindings so it can shadow them (ctrl+n).
	if m.completion.active {
		if mdl, cmd, handled := m.handleCompletionKey(key); handled {
			return mdl, cmd
		}
	}
	// The correction walk owns the keyboard while up.
	if m.walk.active {
		return m.handleSpellWalkKey(key)
	}
	// An open chooser owns the keyboard.
	if m.picker.active() {
		if mdl, cmd, handled := m.handlePickerKey(key); handled {
			return mdl, cmd
		}
		return m, nil
	}
	// Chords and counts only where keys are commands, never while typing.
	press := key.String()
	if !m.typing() {
		var waiting bool
		m, press, waiting = m.advanceChord(press)
		if waiting {
			return m, nil
		}
		// A count, checked after chords so a sequence bound to a digit wins.
		if n, ok := countDigit(press, m.motion.count); ok {
			m.motion.count = n
			return m.say(strconv.Itoa(n)), nil
		}
		m.motion.repeat, m.motion.count = m.motion.count, 0
	}
	if mdl, cmd, handled := m.globalKey(key); handled {
		return mdl, cmd
	}
	// Place and script bindings come after the action scopes, so they cannot shadow built-ins.
	if mdl, cmd, handled := m.jumpKey(press); handled {
		return mdl, cmd
	}
	if mdl, cmd, handled := m.scriptKey(press); handled {
		return mdl, cmd
	}
	switch m.focus {
	case paneRail:
		return m.handleRailKey(press)
	case paneRooms:
		return m.handleRoomsKey(press)
	case paneTimeline:
		return m.handleTimelineKey(key, press)
	default:
		return m, nil
	}
}

// globalKey handles bindings live in every mode, typing included (so none produce
// text). handled is false for anything it does not claim.
func (m Model) globalKey(key tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch m.keys.lookup(key.String(), scopeGlobal) {
	case actFocusNext:
		return m.moveFocus(1)
	case actFocusPrev:
		return m.moveFocus(-1)
	case actToggleDND:
		return answered(m.toggleDND())
	case actToggleMute:
		return answered(m.toggleMute())
	case actJumpTo:
		return answered(m.openJump())
	case actJumpBack:
		return answered(m.jumpBack())
	case actJumpForward:
		return answered(m.jumpForward())
	case actRedraw:
		return m, repaint(), true
	}
	return m, nil, false
}

// handleCapturedKey gives the key to whatever owns the keyboard — an overlay, a
// pending question, an open prompt. captured is false when nothing does.
func (m Model) handleCapturedKey(key tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	switch {
	// Any key closes the explanation.
	case m.reader.showing(readerWhy):
		return answered(m.handleWhyKey(key))
	// The help overlay captures everything while it's up, as does verification.
	case m.reader.showing(readerAsk), m.reader.showing(readerSummary),
		m.reader.showing(readerTodo), m.reader.showing(readerTopic),
		m.reader.showing(readerScript), m.reader.showing(readerHelp):
		return answered(m.handleHelpKey(key))
	case m.verify.active:
		return answered(m.handleVerifyKey(key))
	// A pending confirmation captures the keyboard so it cannot be answered by accident later.
	case m.confirm.active():
		return answered(m.handleConfirmKey(key))
	case m.prompt.active():
		return answered(m.handlePromptKey(key))
	// While the daemon signs in, the pane is the sign-in's; the cancel key cancels it.
	case m.login.stage == loginWorking:
		return answered(m.handleLoginKey(key))
	}
	return m, nil, false
}

// moveFocus moves focus one pane left or right. It stops at the ends rather than
// wrapping and reports false there, so the key (tab) falls through to the composer's
// completion.
func (m Model) moveFocus(delta int) (Model, tea.Cmd, bool) {
	next := m.focus + pane(delta)
	if next < 0 || next >= paneCount {
		return m, nil, false
	}
	m.focus = next
	m.compose = m.compose.left()
	return m, nil, true
}

// handleHelpKey scrolls the reader overlays; help, quit and back close them. Keys
// resolve as sequences too ("gg"). In help, search narrows the list (see
// handleHelpFilterKey), and back clears a filter before it closes.
func (m Model) handleHelpKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	help := m.reader.showing(readerHelp)
	if help && m.reader.filtering {
		return m.handleHelpFilterKey(key)
	}
	m, seq, waiting := m.advanceChordIn(key.String(), scopeCommand, scopeNav)
	if waiting {
		return m, nil
	}
	switch act := m.keys.lookup(seq, scopeCommand, scopeNav); act {
	case actBack:
		if help && m.reader.filter != "" {
			m = m.store(fieldHelpFilter, newEditor(""))
			return m, nil
		}
		m.reader = m.reader.closed()
	case actHelp, actQuit:
		m.reader = m.reader.closed()
	case actSearchRoom, actSearchAll:
		if help {
			m.reader.filtering = true
		}
	default:
		// The overlay is a plain scrolled list; its end is past everything in it.
		if delta, ok := navDelta(act, m.take(), m.helpRows(), len(m.readerContent())); ok {
			m = m.scrollHelp(m.reader.scroll + delta)
		}
	}
	return m, nil
}

// handleHelpFilterKey types the help filter: text narrows the list as it is typed,
// submit keeps it and gives the keys back to scrolling, and cancel drops it.
func (m Model) handleHelpFilterKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	if text := key.Text; text != "" {
		return m.store(fieldHelpFilter, m.editorFor(fieldHelpFilter).insert(text)), nil
	}
	switch m.keys.lookup(key.String(), scopePrompt) {
	case actSubmit:
		m.reader.filtering = false
		return m, nil
	case actCancel:
		m = m.store(fieldHelpFilter, newEditor(""))
		m.reader.filtering = false
		return m, nil
	}
	if ed, ok := m.editorFor(fieldHelpFilter).editTail(key, m.keys); ok {
		return m.store(fieldHelpFilter, ed), nil
	}
	return m, nil
}

// scrollHelp clamps the open reader's scroll to its own content length.
func (m Model) scrollHelp(want int) Model {
	m.reader = m.reader.scrolledTo(want, len(m.readerContent())-m.helpRows())
	return m
}

// commandAction handles actions that mean the same in every pane. handled is false
// when act is the pane's own business.
func (m Model) commandAction(act action) (Model, tea.Cmd, bool) {
	// Player keys first: the bar is on screen from every pane.
	if mdl, cmd, handled := m.playerAction(act); handled {
		return mdl, cmd, true
	}
	switch act {
	case actQuit:
		// Flush the open room's colors (no room boundary will after quit), then the
		// drafts still to be written.
		mdl, cmd := m.quitAfterDrafts(m.flushSenderSlots())
		return mdl, cmd, true
	case actHelp:
		m.reader = m.reader.opening(readerHelp)
		return m, nil, true
	case actSettings:
		return answered(m.openSettings())
	case actCommand:
		return answered(m.openCommandLine())
	case actSearchRoom:
		return answered(m.openSearch(false))
	case actSearchAll:
		return answered(m.openSearch(true))
	case actMentions:
		return answered(m.openMentions())
	case actFiles:
		return answered(m.openFiles(false))
	}
	return m, nil, false
}

// handleRailKey moves between rail groups; the rail is leftmost, so there is no back.
func (m Model) handleRailKey(press string) (Model, tea.Cmd) {
	act := m.keys.lookup(press, m.withPlayerScope(scopeRail, scopeNav, scopeCommand)...)
	if mdl, cmd, handled := m.commandAction(act); handled {
		return mdl, cmd
	}
	switch act {
	case actName:
		return m.renameGroup()
	case actNotifyRule:
		return m.openRuleForGroup()
	case actMarkRead:
		return m.askMarkGroupRead()
	case actUp:
		if next, moved := m.rail.moved(-m.take()); moved {
			m.rail = next
			return m.selectGroup()
		}
	case actDown:
		if next, moved := m.rail.moved(m.take()); moved {
			m.rail = next
			return m.selectGroup()
		}
	case actSelectOldest, actScrollOldest, actSelectNewest, actScrollNewest:
		return m.railEnd(act)
	case actOpen:
		m.focus = paneRooms
	}
	return m, nil
}

// roomStandingAction handles room-list keys that change how a room stands: unread
// state, spaces, archive, pin, spam and the list's sort order.
func (m Model) roomStandingAction(act action) (Model, tea.Cmd, bool) {
	switch act {
	case actToggleUnreadFirst:
		return answered(m.toggleSortKey(domain.SortUnread))
	case actToggleMentionsFirst:
		return answered(m.toggleSortKey(domain.SortMentions))
	case actToggleDraftsFirst:
		return answered(m.toggleSortKey(domain.SortDrafts))
	case actSortRecent:
		// Name as the tie-break, like the default chain.
		return answered(m.setRoomOrder(domain.SortRecent, domain.SortName))
	case actSortAlpha:
		return answered(m.setRoomOrder(domain.SortName))
	case actMarkRead:
		// A thread row gets a thread receipt.
		if row, ok := m.selectedRow(); ok && row.isThread() {
			return answered(m.markThreadRowRead(row))
		}
		return answered(m.markRoomRead())
	case actMarkUnread:
		return answered(m.toggleRoomUnread())
	case actSpaces:
		return answered(m.openSpacePicker())
	case actInvite:
		return answered(m.openInvite())
	case actSpam:
		return answered(m.toggleSpam())
	}
	return m, nil, false
}

// roomThreadAction handles room-list keys about threads: naming a thread row, listing the room's threads.
func (m Model) roomThreadAction(act action) (Model, tea.Cmd, bool) {
	// Only the naming keys need the cursor's row; building the list for every other
	// key was half of what a keypress in a large room list cost.
	thread := func() (domain.EventID, bool) {
		row, ok := m.selectedRow()
		return row.thread.Root, ok && row.isThread()
	}
	switch act {
	case actName:
		if root, onThread := thread(); onThread {
			return answered(m.renameThread(root))
		}
		return answered(m.renameRoom())
	case actRenameThread:
		// The chord names only threads; `a` names whichever row is selected.
		if root, onThread := thread(); onThread {
			return answered(m.renameThread(root))
		}
		return m, nil, true
	case actListThreads:
		return answered(m.listRoomThreads())
	}
	return m, nil, false
}

// handleRoomsKey drives the room list; the rooms scope is consulted first so its keys
// win over navigation.
func (m Model) handleRoomsKey(press string) (Model, tea.Cmd) {
	act := m.keys.lookup(press, m.withPlayerScope(scopeRooms, scopeNav, scopeCommand)...)
	if mdl, cmd, handled := m.commandAction(act); handled {
		return mdl, cmd
	}
	if mdl, cmd, handled := m.roomStandingAction(act); handled {
		return mdl, cmd
	}
	if mdl, cmd, handled := m.roomThreadAction(act); handled {
		return mdl, cmd
	}
	switch act {
	case actAccept:
		return m.acceptInvite()
	case actReject:
		return m.askReject()
	case actShowPeople:
		return m.openPeopleForRoom()
	case actViewMedia:
		return m.viewMedia()
	case actNotifyRule:
		return m.openRuleForRoom()
	case actJoin:
		return m.openPrompt(promptJoin), nil
	case actLeave:
		return m.askLeave()
	case actUp:
		return m.stepRows(m.take(), -1)
	case actDown:
		return m.stepRows(m.take(), 1)
	case actSelectOldest, actScrollOldest, actSelectNewest, actScrollNewest:
		return m.rowEnd(act)
	case actOpen:
		return m.openSelectedRoom()
	case actBack:
		m.focus = paneRail
	}
	return m, nil
}

// rowEnd answers gg and G in the room list: with a count, that row (`12G`).
func (m Model) rowEnd(act action) (Model, tea.Cmd) {
	if n := m.take(); n > 1 {
		return m.gotoRow(n)
	}
	if act == actSelectNewest || act == actScrollNewest {
		return m.gotoLastRow()
	}
	return m.gotoRow(1)
}

// railEnd answers gg and G in the rail: the ends, or with a count that group.
func (m Model) railEnd(act action) (Model, tea.Cmd) {
	n := m.take()
	target := n
	switch {
	case n > 1:
	case act == actSelectNewest || act == actScrollNewest:
		target = len(m.rail.groups)
	default:
		target = 1
	}
	next, moved := m.rail.went(target)
	if !moved {
		return m, nil
	}
	m.rail = next
	return m.selectGroup()
}

// openSelectedRoom moves into the open room's timeline: focus, mode, read receipt and backfill.
func (m Model) openSelectedRoom() (Model, tea.Cmd) {
	// An invitation has no timeline; its decision pane is already shown.
	if room, ok := m.currentRoom(); ok && room.IsInvite() {
		return m, nil
	}
	// A thread row opens its thread.
	if row, ok := m.selectedRow(); ok && row.isThread() {
		return m.openSelectedThread(row.thread.Root)
	}
	m.focus = paneTimeline
	// display.open_in_insert_mode decides the starting mode.
	m.compose.insertMode, m.compose.reacting = m.prefs.openInsert, false
	// Enter issues the armed loads now; bumping loadArmed drops the pending timer.
	m.loadArmed++
	loadNow := m.loadRoomCmd(m.openRoom)

	// Opening reads the room, then backfills the full history in the background.
	var readCmd tea.Cmd
	m, readCmd = m.markRead()
	if !m.timeline.hist.atStart {
		m.timeline.hist.backfilling = true
		mdl, loadCmd := m.loadOlder()
		return mdl, tea.Batch(readCmd, loadCmd, loadNow)
	}
	return m, tea.Batch(readCmd, loadNow)
}

// handleTimelineKey dispatches by the timeline's mode.
func (m Model) handleTimelineKey(key tea.KeyPressMsg, press string) (Model, tea.Cmd) {
	switch {
	case m.history.active():
		return m.handleHistoryKey(key)
	case m.search.active:
		return m.handleSearchKey(key)
	case m.compose.reacting:
		return m.handleReactKey(key)
	case m.compose.insertMode:
		return m.handleInsertKey(key)
	default:
		return m.handleNormalKey(press)
	}
}

// handleNormalKey drives the timeline as a message browser. The timeline scope is
// consulted first so its reply binding wins over nav's open.
func (m Model) handleNormalKey(press string) (Model, tea.Cmd) {
	act := m.keys.lookup(press, m.withPlayerScope(scopeTimeline, scopeNav, scopeCommand)...)
	if mdl, cmd, handled := m.timelineAction(act); handled {
		return mdl, cmd
	}
	return m.timelineMotion(act)
}

// threadAction handles keys about a thread in the room: open, start, list, rename —
// and back, which closes a thread before leaving the pane.
func (m Model) threadAction(act action) (Model, tea.Cmd, bool) {
	switch act {
	case actBack:
		// Back climbs out of the thread first.
		if !m.thread.open() {
			return m, nil, false
		}
		return answered(m.closeThread())
	case actOpenThread:
		return answered(m.openThread())
	case actStartThread:
		return answered(m.startThread())
	case actListThreads:
		return answered(m.listThreads())
	case actRenameThread:
		return answered(m.renameThreadInView())
	}
	return m, nil, false
}

// timelineAction acts on the conversation; handled is false for motion, which timelineMotion takes.
func (m Model) timelineAction(act action) (Model, tea.Cmd, bool) {
	if mdl, cmd, handled := m.messageAction(act); handled {
		return mdl, cmd, true
	}
	if mdl, cmd, handled := m.commandAction(act); handled {
		return mdl, cmd, true
	}
	if mdl, cmd, handled := m.threadAction(act); handled {
		return mdl, cmd, true
	}
	switch act {
	case actBack:
		m.focus = paneRooms
	case actInsert:
		m.compose.insertMode = true
	case actOpenEmoji:
		// From the message cursor the palette reacts.
		return answered(m.openReactionPalette())
	case actReply:
		return answered(m.startReply())
	case actGoReply:
		return answered(m.goToReplied())
	case actRedact:
		return answered(m.askRedact())
	case actEdit:
		return answered(m.askEdit())
	case actReact:
		if _, ok := m.selectedMessage(); ok {
			m.compose = m.compose.startReacting()
		}
	case actNextMention:
		return answered(m.jumpMatch(1, mentionMatch))
	case actPrevMention:
		return answered(m.jumpMatch(-1, mentionMatch))
	case actNextAttachment:
		return answered(m.jumpMatch(1, attachmentMatch))
	case actPrevAttachment:
		return answered(m.jumpMatch(-1, attachmentMatch))
	default:
		return m, nil, false
	}
	return m, nil, true
}

// messageAction acts on the selected message.
func (m Model) messageAction(act action) (Model, tea.Cmd, bool) {
	switch act {
	case actName:
		return answered(m.openIdentityForSender())
	case actNotifyRule:
		return answered(m.openRuleForSender())
	case actCopyText:
		return answered(m.copyMessageText())
	case actCopyURL, actOpenURL, actOpenURLFocus, actFollowLink, actCopyCode:
		// Each takes a thing out of the message; see context.go.
		return answered(m.takeContext(act))
	case actTopic:
		return answered(m.openTopic())
	case actStar:
		return answered(m.toggleStar())
	case actHistory:
		return answered(m.openHistory())
	case actDownload:
		return answered(m.download())
	case actViewMedia:
		return answered(m.viewMedia())
	case actPlay:
		return answered(m.play())
	case actSaveAs:
		return answered(m.saveAs())
	default:
		return m, nil, false
	}
}

// timelineMotion moves the message cursor (in messages) and the viewport (in visual
// rows).
func (m Model) timelineMotion(act action) (Model, tea.Cmd) {
	// Paging moves the view; the cursor follows it on screen.
	if rows, ok := m.pageScroll(act); ok {
		return m.followScroll(m.scrollBy(rows))
	}
	switch act {
	case actDown:
		return m.moveSelection(m.take())
	case actUp:
		return m.moveSelection(-m.take())
	case actSelectOldest:
		return m.selectEnd(-1)
	case actSelectNewest:
		return m.selectEnd(1)
	case actExternalEdit:
		return m.openExternalEditor()
	case actReveal:
		return m.toggleReveal()
	case actSpellWalk:
		return m.openSpellWalk()
	case actPrevDay:
		return m.jumpDay(m.take(), -1)
	case actNextDay:
		return m.jumpDay(m.take(), 1)
	case actScrollOldest:
		return m.jumpToStart()
	case actScrollNewest:
		return m.selectEnd(1)
	}
	return m, nil
}

// pageScroll is how far a paging action moves the timeline view, in rows toward the
// oldest (the scroll offset counts up from the newest row, so the opposite sign to
// navDelta's). ok is false for an action that does not page.
func (m Model) pageScroll(act action) (int, bool) {
	switch act {
	case actPageUp, actPageDown, actHalfPageUp, actHalfPageDown:
		rows, _ := navDelta(act, 1, m.msgAreaRows(), 0)
		return -rows, true
	}
	return 0, false
}

// jumpToStart goes to the conversation's beginning: the oldest message, then keeps
// loading history to the real start (gg only goes to the oldest loaded).
func (m Model) jumpToStart() (Model, tea.Cmd) {
	m, selected := m.selectEnd(-1)
	m, backfill := m.backfillToStart()
	return m, tea.Batch(selected, backfill)
}

// handleInsertKey composes a message: text types at the caret, editing keys edit,
// send posts, cancel returns to the message cursor. Paging still works.
func (m Model) handleInsertKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	// Swap arrows first so every layer sees "forward" the same way in RTL text. See swapArrows.
	key = swapArrows(key, m.compose.input, m.editorFor(fieldComposer).at)
	if text := key.Text; text != "" {
		// Text always types, so no keymap can make a letter unreachable.
		m = m.store(fieldComposer, m.editorFor(fieldComposer).insert(text))
		return m.composerTyped(text)
	}
	switch m.keys.lookup(key.String(), scopeInsert) {
	case actCancel:
		m = m.cancelEdit() // and gives back whatever the composer held before it
		m.compose.insertMode = false
		m.compose.replyTo = "" // leaving insert cancels a pending reply
		m.compose.drafted = nil
		// Tell the room we stopped typing.
		var stop tea.Cmd
		m, stop = m.closeCompletion().stopTyping()
		return m, stop
	case actSend:
		return m.submit()
	case actNewline:
		// Enter never arrives as key.Text, so a newline is an action.
		m = m.store(fieldComposer, m.editorFor(fieldComposer).insert("\n"))
		return m.composerTyped("\n")
	case actAttach:
		return m.openAttach()
	case actExternalEdit:
		return m.openExternalEditor()
	case actCut:
		return m.cutDraft()
	case actOpenEmoji:
		// Mid-sentence the palette inserts.
		return m.openEmojiPalette()
	case actSpellWalk:
		return m.openSpellWalk()
	case actModelPreview:
		return m.openModelPreview()
	}
	return m.insertEditKey(key)
}

// insertEditKey handles non-command composer keys: completion, row motions, the line
// editor, then scrolling.
func (m Model) insertEditKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	// Completion keys first; they fall through when there is no suggestion.
	if mdl, cmd, ok := m.assistKey(key); ok {
		return mdl, cmd
	}
	// Rows are the composer's own motion: no other field wraps.
	switch m.keys.lookup(key.String(), scopeEdit) {
	case actEditRowUp:
		return m.moveComposerRow(-1)
	case actEditRowDown:
		return m.moveComposerRow(1)
	}
	// After the action lookup, so a binding there wins (ctrl+e means emoji.open here).
	if ed, ok := m.editorFor(fieldComposer).edit(key, m.keys); ok {
		m = m.store(fieldComposer, ed)
		return m.composerTyped("")
	}
	return m.scrollKey(key.String())
}

// scrollKey applies a bulk-scroll binding only, so text-entry modes can page the
// timeline without the nav scope claiming keys they need.
func (m Model) scrollKey(key string) (Model, tea.Cmd) {
	act := m.keys.lookup(key, scopeNav)
	if rows, ok := m.pageScroll(act); ok {
		return m.scrollBy(rows)
	}
	switch act {
	case actScrollOldest:
		return m.scrollToStart()
	case actScrollNewest:
		m.timeline.scroll = 0
	}
	return m, nil
}

// handleReactKey drives the emoji prompt: a digit picks from the palette and sends,
// text builds a :shortcode: or emoji, send posts it, cancel closes.
func (m Model) handleReactKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	// A pick key reacts only while nothing is typed, so a :shortcode: can contain a digit.
	if m.compose.reactInput == "" {
		if n, ok := pickOf(m.keys.lookup(key.String(), scopeReact)); ok && n < len(m.glyphs.palette) {
			return m.sendReaction(m.glyphs.palette[n])
		}
	}
	if text := key.Text; text != "" {
		m = m.store(fieldReact, m.editorFor(fieldReact).insert(text))
		return m.reactionTyped(text)
	}
	switch m.keys.lookup(key.String(), scopeReact) {
	case actCancel:
		m.compose = m.compose.doneReacting()
		return m.closeCompletion(), nil
	case actSend:
		// A typed :shortcode: gets the configured tone; a pasted emoji is sent as-is.
		key, ok := m.resolveReaction(m.compose.reactInput)
		if !ok {
			// Keep the prompt open so the input can be corrected.
			return m.say(key), nil
		}
		return m.sendReaction(m.tone(key))
	}
	if ed, ok := m.editorFor(fieldReact).edit(key, m.keys); ok {
		m = m.store(fieldReact, ed)
		return m.reactionTyped("")
	}
	return m, nil
}

// sendReaction reacts to the selected message and closes the prompt; empty just closes.
func (m Model) sendReaction(reaction string) (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	m.compose = m.compose.doneReacting()
	m = m.closeCompletion()
	if !ok || strings.TrimSpace(reaction) == "" {
		return m, nil
	}
	m = m.say("reacting…")
	return m, m.sendReactionCmd(msg.RoomID, msg.ID, reaction)
}

// keepSelectionVisible moves the message cursor into the viewport after the view
// moved on its own, so the next arrow does not drag the view back.
func (m Model) keepSelectionVisible() Model {
	rows := m.msgAreaRows()
	from, to, ok := m.selectionRows()
	if !ok {
		return m
	}
	switch {
	case to <= m.timeline.scroll:
		// Below the bottom edge: take the newest message still on screen.
		if msg, found := m.messageAtRow(m.timeline.scroll); found {
			m.timeline.selected = msg.ID
		}
	case from >= m.timeline.scroll+rows:
		// Above the top edge: take the oldest message still on screen.
		if msg, found := m.messageAtRow(m.timeline.scroll + rows - 1); found {
			m.timeline.selected = msg.ID
		}
	}
	return m
}

// followScroll applies a view-only move, then brings the cursor into view.
func (m Model) followScroll(moved Model, cmd tea.Cmd) (Model, tea.Cmd) {
	return moved.keepSelectionVisible(), cmd
}

// scrollToStart pins the view to the oldest loaded row and backfills to the room start.
func (m Model) scrollToStart() (Model, tea.Cmd) {
	m.timeline.scroll = m.maxScroll()
	return m.backfillToStart()
}

// backfillToStart keeps loading history until the start. A thread pages one page at a
// time: backfilling is the room's mechanism and would pull the room's whole history.
func (m Model) backfillToStart() (Model, tea.Cmd) {
	if m.atStartHere() {
		return m, nil
	}
	if !m.thread.open() {
		m.timeline.hist.backfilling = true
	}
	return m.loadOlder()
}

// atStartHere reports whether the shown conversation has no more history: the
// thread's flag while one is open, the room's otherwise.
func (m Model) atStartHere() bool {
	if m.thread.open() {
		return m.thread.atStart
	}
	return m.timeline.hist.atStart
}

// resetSelection returns the timeline to normal mode with the newest message selected.
// The reply target is the draft's, and goes with it (stashDraft), not with the cursor.
func (m Model) resetSelection() Model {
	m.compose = m.compose.left().doneReacting()
	m.timeline.selected = ""
	// A thread belongs to its room.
	m.thread = threadState{}
	return m
}

// selectedIndex is the selected message's position in what the pane shows,
// defaulting to the newest; -1 for an empty timeline.
func (m Model) selectedIndex() int {
	return indexIn(m.shownMessages(), m.timeline.selected)
}

// indexIn is selectedIndex over a message list already in hand.
func indexIn(msgs []domain.Message, selected domain.EventID) int {
	if len(msgs) == 0 {
		return -1
	}
	if selected != "" {
		for i := range msgs {
			if msgs[i].ID == selected {
				return i
			}
		}
	}
	return len(msgs) - 1
}

// selectedMessage returns the selected message; ok is false for an empty timeline.
func (m Model) selectedMessage() (domain.Message, bool) {
	msgs := m.shownMessages()
	if i := indexIn(msgs, m.timeline.selected); i >= 0 {
		return msgs[i], true
	}
	return domain.Message{}, false
}

// selectedID is the effective selected message ID (explicit, else the newest).
func (m Model) selectedID() domain.EventID {
	if msg, ok := m.selectedMessage(); ok {
		return msg.ID
	}
	return ""
}

// moveSelection moves the message cursor by delta (positive = newer), keeping it in
// view; moving past the oldest loaded message loads more.
func (m Model) moveSelection(delta int) (Model, tea.Cmd) {
	msgs := m.shownMessages()
	idx := indexIn(msgs, m.timeline.selected)
	if idx < 0 {
		return m, nil
	}
	target := idx + delta
	if target < 0 {
		m.timeline.selected = msgs[0].ID
		m = m.scrollToSelection()
		if !m.atStartHere() {
			return m.loadOlder()
		}
		return m, nil
	}
	if target >= len(msgs) {
		target = len(msgs) - 1
	}
	m.timeline.selected = msgs[target].ID
	m = m.scrollToSelection()
	return m, nil
}

// timelineMatch is a kind of message the cursor can step between (mentions, attachments).
type timelineMatch struct {
	// plural names the thing in "no mentions in this room".
	plural string
	is     func(domain.Message) bool
}

var (
	mentionMatch    = timelineMatch{"mentions", func(msg domain.Message) bool { return msg.Mentioned }}
	attachmentMatch = timelineMatch{"attachments", func(msg domain.Message) bool { return msg.Media != nil }}
)

// jumpMatch moves the selection to the nearest match in dir (dir>0 = newer),
// wrapping like vim's wrapscan and saying so. With nothing selected the cursor is
// the newest message, so without the wrap "next" would never find anything.
func (m Model) jumpMatch(dir int, match timelineMatch) (Model, tea.Cmd) {
	msgs := m.shownMessages()
	start := indexIn(msgs, m.timeline.selected)
	if start < 0 {
		return m, nil
	}
	n := len(msgs)
	// n steps, so a lone match under the cursor is found (as a wrap).
	for step := 1; step <= n; step++ {
		i := ((start+dir*step)%n + n) % n
		if !match.is(msgs[i]) {
			continue
		}
		m.timeline.selected = msgs[i].ID
		m = m.scrollToSelection()
		if wrappedPast(start, i, dir) {
			m = m.say(wrapNote(dir))
		}
		return m, nil
	}
	return m.say("no " + match.plural + " in " + m.spanName()), nil
}

// wrappedPast reports whether landing on to from at, going dir, went past an end.
func wrappedPast(from, to, dir int) bool {
	if dir > 0 {
		return to <= from
	}
	return to >= from
}

// wrapNote says which end was passed; newest is the bottom of the pane.
func wrapNote(dir int) string {
	if dir > 0 {
		return "hit the newest — continuing from the oldest"
	}
	return "hit the oldest — continuing from the newest"
}

// spanName names what the motions searched: the open thread or the room.
func (m Model) spanName() string {
	if m.thread.open() {
		return "this thread"
	}
	return "this room"
}

// selectEnd selects the oldest (dir<0) or newest (dir>0) loaded message.
func (m Model) selectEnd(dir int) (Model, tea.Cmd) {
	msgs := m.shownMessages()
	if len(msgs) == 0 {
		return m, nil
	}
	if dir < 0 {
		m.timeline.selected = msgs[0].ID
		// dayAtTop rather than scrollToSelection, so the first date separator is visible too.
		return m.dayAtTop(), nil
	}
	m.timeline.selected = msgs[len(msgs)-1].ID
	m = m.scrollToSelection()
	return m, nil
}

// handleSenderSlots adopts the cached colors for the open room; a late answer for
// another room is dropped.
func (m Model) handleSenderSlots(msg senderSlotsMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelDebug, "load sender colors", msg.err, "room", msg.roomID)
	if msg.err != nil || m.derived == nil || msg.roomID != m.openRoom {
		return m, nil
	}
	m.derived.adoptSlots(msg.roomID, msg.slots)
	return m, nil
}

// flushSenderSlots writes back colors assigned since the last flush. Called at room
// boundaries (leaving, quitting) rather than every Update, which would make every key
// return a command. Colors of a room never left are lost on kill and re-derived.
func (m Model) flushSenderSlots() tea.Cmd {
	if m.derived == nil {
		return nil
	}
	roomID, slots := m.derived.takeUnsaved()
	if roomID == "" || len(slots) == 0 {
		return nil
	}
	return m.saveSenderSlotsCmd(roomID, slots)
}

// setMessages is the only way messages are replaced; it bumps timelineRev, which
// invalidates the derived cache (TestMessagesAssignedOnlyBySetMessages).
func (m Model) setMessages(msgs []domain.Message) Model {
	m.timeline.messages = msgs
	m.timeline.rev++
	return m
}

// scrollToSelection nudges scroll so the selected message is in view.
func (m Model) scrollToSelection() Model {
	rows := m.msgAreaRows()
	// Measured up from the newest message, like scroll.
	from, to, ok := m.selectionRows()
	if !ok {
		return m
	}
	switch {
	case to > m.timeline.scroll+rows:
		// Above the top edge: bring its last row onto the top of the viewport.
		m.timeline.scroll = to - rows
	case from < m.timeline.scroll:
		// Below the bottom edge: rest the viewport on it.
		m.timeline.scroll = from
	}
	if m.timeline.scroll < 0 {
		m.timeline.scroll = 0
	}
	if mx := m.maxScroll(); m.timeline.scroll > mx {
		m.timeline.scroll = mx
	}
	return m
}

// scrollBy moves the viewport by delta visual rows (positive = older), clamped to the
// loaded range; scrolling past the top loads older history.
func (m Model) scrollBy(delta int) (Model, tea.Cmd) {
	target := m.timeline.scroll + delta
	if target < 0 {
		m.timeline.scroll = 0
		return m, nil
	}
	rows := m.msgAreaRows()
	// Only lay out as far as the target needs, so paging costs the same at any depth.
	limit, exhausted := m.scrollLimit(target, rows)
	if target >= limit {
		m.timeline.scroll = limit
		// Only the true top loads more; an unexhausted tail already holds older messages.
		if delta > 0 && exhausted {
			return m.loadOlder()
		}
		return m, nil
	}
	m.timeline.scroll = target
	return m, nil
}

// maxScroll is the furthest-back offset that still fills the viewport. Exact when
// the layout reached the oldest loaded message, otherwise a lower bound no smaller
// than the current scroll — enough for a clamp.
func (m Model) maxScroll() int {
	rows := m.msgAreaRows()
	// A page of headroom past the current position.
	limit, _ := m.scrollLimit(m.timeline.scroll+rows+1, rows)
	return limit
}

// selectGroup responds to a rail move: open the new group's first room, or clear
// the timeline for an empty group.
func (m Model) selectGroup() (Model, tea.Cmd) {
	// Stash before clearing openRoom: the ID is how the draft's room is known.
	m = m.stashDraft(m.openRoom)
	m.openRoom = "" // drop any Unread-group pin carried over from the previous group
	fr := m.filteredRooms()
	if len(fr) == 0 {
		return m.clearRoom()
	}
	return m.selectRoom(fr[0])
}

// leavingFor settles what must be read before the open room changes: the unread
// band, the unread-line snapshot, and the outgoing draft.
func (m Model) leavingFor(room domain.Room) Model {
	m.timeline.opened.wasUnread = ""
	if m.unreadView().tallies(room) {
		m.timeline.opened.wasUnread = room.ID
	}
	m.timeline.opened.unreadFrom = m.unread[room.ID].ReadEvent
	// Stars belong to the room; the new set arrives via starredInCmd.
	m.timeline.starred = nil
	return m.stashDraft(m.openRoom)
}

// selectRoom makes room the open room and arms its loads.
func (m Model) selectRoom(room domain.Room) (Model, tea.Cmd) {
	leaving := m.openRoom != room.ID
	if leaving {
		m = m.leavingFor(room)
	}
	saveColors := m.flushSenderSlots()
	m.openRoom = room.ID
	m = m.emptiedTimeline()
	m.glyphs.palette = m.glyphs.static // fall back to static until the ranked palette lands
	// Leaving a room drops the thread-row cursor, unless the move landed on one of the
	// new room's thread rows.
	if leaving && m.rows.cursor != "" && !hasThreadCursorIn(m.unread[room.ID], m.rows.cursor) {
		m.rows.cursor = ""
	}
	// An invitation has no readable history (fetching it would 403).
	if room.IsInvite() {
		m = m.clearStatus()
		if leaving {
			var aimed tea.Cmd
			m, aimed = m.restoreDraft(room.ID)
			return m, tea.Batch(saveColors, repaint(), aimed)
		}
		return m, saveColors
	}
	m.timeline.hist.loading = true
	m = m.doing("loading history…")
	// Whether resting on a room reads it is the read policy's call.
	m, focusRead := m.armFocusRead()
	m.timeline.members = nil
	// Restore this room's draft; rebuild the rail since the Drafts set changed.
	var aimed tea.Cmd
	if leaving {
		m, aimed = m.restoreDraft(room.ID)
		m = m.rebuiltRail()
	}
	// Loads are armed, not issued: they fire once the cursor rests (roomLoadDelay), so
	// scrolling past rooms costs no round trips.
	m.loadArmed++
	cmds := []tea.Cmd{saveColors, focusRead, m.armRoomLoad(room.ID), aimed}
	// Repaint on a room change: cells the terminal laid out differently than measured
	// would otherwise survive into the new room. Not on re-selecting the open room,
	// which happens on every re-sort.
	if leaving {
		cmds = append(cmds, repaint())
	}
	return m, tea.Batch(cmds...)
}

// roomLoadDelay is how long the cursor must rest on a room before its loads are issued.
const roomLoadDelay = 120 * time.Millisecond

// armRoomLoad loads once the cursor settles; a later move bumps loadArmed and drops this timer.
func (m Model) armRoomLoad(roomID domain.RoomID) tea.Cmd {
	armed := m.loadArmed
	return tea.Tick(roomLoadDelay, func(time.Time) tea.Msg {
		return roomLoadMsg{roomID: roomID, armed: armed}
	})
}

// handleRoomLoad issues a settled room's loads, or drops a stale timer.
func (m Model) handleRoomLoad(msg roomLoadMsg) (Model, tea.Cmd) {
	if msg.armed != m.loadArmed || msg.roomID != m.openRoom {
		return m, nil
	}
	return m, m.loadRoomCmd(msg.roomID)
}

// loadRoomCmd is everything a room needs once settled on; opening a room calls it directly.
func (m Model) loadRoomCmd(roomID domain.RoomID) tea.Cmd {
	cmds := []tea.Cmd{
		m.cachedTimelineCmd(roomID), m.cachedReactionsCmd(roomID),
		m.membersCmd(roomID), m.refreshMembersCmd(roomID),
		m.loadTimelineCmd(roomID, ""),
		// Cached sender colors, so hues are stable across opens.
		m.senderSlotsCmd(roomID),
		m.starredInCmd(roomID),
	}
	// The emoji ranking, once per session, so the palette does not re-sort under the cursor.
	if _, ranked := m.glyphs.orders[roomID]; !ranked {
		cmds = append(cmds, m.emojiScoresCmd(roomID, domain.EmojiReaction), m.emojiScoresCmd(roomID, domain.EmojiComposed))
	}
	if m.prefs.display.Threads.Mode() == config.ThreadsAll {
		cmds = append(cmds, m.roomThreadsCmd(roomID))
	}
	return tea.Batch(append(cmds, m.listThreadsCmd())...)
}

// clearRoom closes the open room, leaving the timeline blank. The status line goes
// with the room only when there was one: with none open, what it says (a network
// logged out) is about the account, not a room.
func (m Model) clearRoom() (Model, tea.Cmd) {
	wasOpen := m.openRoom != ""
	// The draft goes with the room, wherever the room went.
	m = m.stashDraft(m.openRoom)
	saveColors := m.flushSenderSlots()
	m.openRoom = ""
	m = m.emptiedTimeline()
	m.rows.cursor = ""
	if wasOpen {
		m = m.clearStatus()
	}
	return m, saveColors
}

// emptiedTimeline resets everything the timeline holds for the open room.
func (m Model) emptiedTimeline() Model {
	m = m.setMessages(nil).dropImages().resetSelection()
	m.timeline.reactions = map[domain.EventID][]domain.Reaction{}
	m.timeline.hist = roomScrollback{}
	m.timeline.scroll = 0
	m.receipts.marked, m.receipts.sending = "", ""
	return m
}

func (m Model) submit() (Model, tea.Cmd) {
	body := strings.TrimSpace(m.compose.input)
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	// An edit first: its text is a message someone already received, so a leading slash
	// is not a command, and emptying it is reported (see submitEdit).
	if m.compose.isEditing() {
		return m.submitEdit(room, body)
	}
	if body == "" {
		return m, nil
	}
	// Slash commands (e.g. `/upload`) are not messages.
	if handled, mdl, cmd := m.composerCommand(body, room); handled {
		return mdl, cmd
	}
	// [spell] check_before_send may open the walk instead; after commands, so paths are not checked.
	if mdl, cmd, held := m.spellGate(); held {
		return mdl, cmd
	}
	// `//text` escapes a command: one slash is eaten (Element's convention).
	if strings.HasPrefix(body, "//") {
		body = body[1:]
	}
	return m.sendComposed(room, body, false)
}

// sendComposed sends the composer's text, as text or as an emote (`/me`), and clears it.
func (m Model) sendComposed(room domain.Room, body string, emote bool) (Model, tea.Cmd) {
	// An upgraded room refuses sends; say where to go and keep the draft.
	if room.Replacement != "" {
		return m.say("this room was replaced — /replacement goes to the room that continues it"), nil
	}
	// An open thread receives the message; otherwise the room does.
	draft := domain.Draft{
		Body: body, Mentions: m.compose.drafted, ReplyTo: m.compose.replyTo, ThreadRoot: m.thread.root, Emote: emote,
		// Markup-or-not travels with the draft, so a queued or scheduled send renders as written.
		Plain: m.plainSend || !m.conf.base.Composer.MarkdownEnabled(),
		// One TxnID per message: retries reuse it so the homeserver dedupes (see domain.Draft.TxnID).
		TxnID: newTxnID(),
	}
	m.compose = m.compose.cleared()
	m.plainSend = false // one message, not a mode
	// The message ends the typing notice.
	m, stop := m.stopTyping()
	m = m.say("sending…")
	return m, tea.Batch(m.sendCmd(room.ID, draft), stop)
}

// markRead sends a receipt for the newest shown message, deduped by receipts.marked so it
// can be called on open, on history and per live message; it does not gate on unread
// counts, which can lag. The room receipt covers the main timeline only (threads keep
// their own positions); an open thread gets a thread receipt, and a thread with no
// reply yet gets none.
func (m Model) markRead() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	private := !m.readPolicy().Send
	if m.thread.open() {
		last := newestIn(m.timeline.messages, m.thread.root)
		if !m.receipts.due(last) {
			return m, nil
		}
		m.receipts.sending = last
		return m, m.markThreadReadCmd(room.ID, m.thread.root, last, private)
	}
	last := newestIn(m.timeline.messages, "")
	if !m.receipts.due(last) {
		return m, nil
	}
	m.receipts.sending = last
	return m, m.markReadCmd(room.ID, last, private)
}

// due reports whether event still needs a receipt: not the one that landed, nor the
// one in flight.
func (r receiptState) due(event domain.EventID) bool {
	return event != "" && event != r.marked && event != r.sending
}

// handleReceiptSent records a landed receipt, or says why it failed and leaves the
// event unmarked so the next trigger retries it. A receipt for a room since left
// changes nothing but is still reported.
func (m Model) handleReceiptSent(msg receiptSentMsg) (Model, tea.Cmd) {
	room, open := m.currentRoom()
	current := open && room.ID == msg.roomID && msg.event == m.receipts.sending
	if current {
		m.receipts.sending = ""
	}
	if msg.err != nil {
		return m.sayErr("read receipt failed", msg.err), nil
	}
	if current {
		m.receipts.marked = msg.event
	}
	return m, nil
}

// newestIn is the newest loaded message in one thread, or in the main timeline when root is empty.
func newestIn(msgs []domain.Message, root domain.EventID) domain.EventID {
	for i := range slices.Backward(msgs) {
		if msgs[i].ThreadRoot == root && msgs[i].ID != "" {
			return msgs[i].ID
		}
	}
	return ""
}

// loadOlder requests the next-older page unless one is in flight, the start is
// reached, or no pagination token has arrived yet.
func (m Model) loadOlder() (Model, tea.Cmd) {
	// A thread pages itself rather than pulling the room's history.
	if m.thread.open() {
		return m.loadOlderInThread()
	}
	room, ok := m.currentRoom()
	if m.timeline.hist.loading || m.timeline.hist.atStart || !m.timeline.hist.ready || !ok {
		return m, nil
	}
	m.timeline.hist.loading = true
	m = m.doing("loading older…")
	return m, m.loadTimelineCmd(room.ID, m.timeline.hist.token)
}

// handleRooms applies a room list from cache or refresh, keeping the open room if it
// still exists; a cold start or vanished room selects the group's first. A refresh
// error is ignored once a list is shown.
func (m Model) handleRooms(msg roomsMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		if len(m.rooms.joined) == 0 {
			m = m.sayErr("error", msg.err)
		}
		return m, nil
	}
	// The cursor before the change, so a removed room hands selection to a neighbor.
	was := m.roomCursor()
	m.rooms = m.rooms.withJoined(msg.rooms)
	m = m.refreshPlaces()
	// Archived rooms need their parent space; asked only for archived rooms.
	parents := m.resolveParentsCmd()
	if mdl, cmd, entered := m.enterPending(); entered {
		return mdl, tea.Batch(cmd, parents)
	}
	// Before the cold-start selection, so a client started for a link lands there.
	if mdl, cmd, followed := m.followPendingLink(); followed {
		return mdl, tea.Batch(cmd, parents)
	}
	// With a room open, the cursor rule decides (see keepCursorNearby).
	if m.openRoom != "" {
		moved, cmd := m.keepCursorNearby(was)
		return moved, tea.Batch(cmd, parents)
	}
	// Cold start: land on the top of the list.
	fr := m.filteredRooms()
	if len(fr) == 0 {
		mdl, cmd := m.clearRoom()
		return mdl, tea.Batch(cmd, parents)
	}
	mdl, cmd := m.selectRoom(fr[0])
	return mdl, tea.Batch(cmd, parents)
}

// handleSpaces applies a space list; spaces are additive, so an error keeps the current rail.
func (m Model) handleSpaces(msg spacesMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "load spaces", msg.err)
	if msg.err != nil {
		return m, nil
	}
	// A room removed from a space arrives this way, so it needs the cursor rule too.
	was := m.roomCursor()
	m.rooms = m.rooms.withSpaces(msg.spaces)
	// Archived-space coverage depends on the hierarchy.
	return m.refreshPlaces().rebuiltRail().keepCursorNearby(was)
}

// handleUnread seeds unread state from the cache; errors are ignored (the stream repopulates it).
func (m Model) handleUnread(msg unreadMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "load cached unread", msg.err)
	if msg.err != nil {
		return m, nil
	}
	unread := maps.Clone(m.unread)
	if unread == nil {
		unread = make(map[domain.RoomID]domain.Unread, len(msg.list))
	}
	for _, u := range msg.list {
		unread[u.RoomID] = u
	}
	m.unread = unread
	return m, nil
}

// handleUnreadUpdate applies one streamed unread state (full, not a delta) and keeps listening.
func (m Model) handleUnreadUpdate(msg unreadUpdateMsg) (Model, tea.Cmd) {
	m.unread = withEntry(m.unread, msg.u.RoomID, msg.u)
	return m, m.listenUnreadCmd()
}

// handleCachedReactions merges the open room's cached reactions.
func (m Model) handleCachedReactions(msg cachedReactionsMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "load cached reactions", msg.err)
	if msg.err != nil {
		return m, nil
	}
	room, ok := m.currentRoom()
	if !ok || msg.roomID != room.ID {
		return m, nil
	}
	return m.withReactions(msg.reactions...), nil
}

// handleReactionUpdate applies a streamed reaction change for the open room and keeps listening.
func (m Model) handleReactionUpdate(msg reactionUpdateMsg) (Model, tea.Cmd) {
	if room, ok := m.currentRoom(); ok && msg.u.Reaction.RoomID == room.ID {
		if msg.u.Removed {
			m = m.withoutReaction(msg.u.Reaction.Target, msg.u.Reaction.ID)
		} else {
			m = m.withReactions(msg.u.Reaction)
		}
	}
	cmds := []tea.Cmd{m.listenReactionsCmd()}
	// Our own reaction removed in a bridged room means the bridge refused it; the daemon
	// has recorded that, so re-read the refusals.
	if msg.u.Removed && m.isMe(msg.u.Reaction.Sender) && m.roomProtocol().IsBridged() {
		cmds = append(cmds, m.refusalsCmd())
	}
	return m, tea.Batch(cmds...)
}

// withReactions is m with reactions added to the open room's index, deduped by event
// ID. The index is copied once per batch, and each target's list is a new slice.
func (m Model) withReactions(rs ...domain.Reaction) Model {
	var index map[domain.EventID][]domain.Reaction
	for _, r := range rs {
		list := m.timeline.reactions[r.Target]
		if index != nil {
			list = index[r.Target]
		}
		if slices.ContainsFunc(list, func(x domain.Reaction) bool { return x.ID == r.ID }) {
			continue
		}
		if index == nil {
			index = maps.Clone(m.timeline.reactions)
			if index == nil {
				index = map[domain.EventID][]domain.Reaction{}
			}
		}
		index[r.Target] = append(slices.Clip(list), r)
	}
	if index != nil {
		m.timeline.reactions = index
		m.rowInputsChanged()
	}
	return m
}

// withoutReaction is m with a reaction (an un-react) gone from the open room's index.
func (m Model) withoutReaction(target, id domain.EventID) Model {
	list := m.timeline.reactions[target]
	for i, x := range list {
		if x.ID == id {
			m.timeline.reactions = withEntry(m.timeline.reactions, target, append(list[:i:i], list[i+1:]...))
			m.rowInputsChanged()
			return m
		}
	}
	return m
}

// cappedStatic is the configured static palette (capped), else the defaults.
func cappedStatic(configured []string) []string {
	s := configured
	if len(s) == 0 {
		s = defaultReactions
	}
	if len(s) > paletteSize {
		s = s[:paletteSize]
	}
	return s
}

// spaceRoomsFor is the rooms of a room's own space (first by space_priority) — the
// middle tier of the emoji ranking.
func (m Model) spaceRoomsFor(roomID domain.RoomID) []domain.RoomID {
	names := m.homesOf(roomID)
	if len(names) == 0 {
		return nil
	}
	if tag, ok := domain.TagOf(names[0]); ok {
		var rooms []domain.RoomID
		for id, facts := range m.rail.roomFacts {
			if slices.Contains(facts.Tags, tag) {
				rooms = append(rooms, id)
			}
		}
		slices.Sort(rooms)
		return rooms
	}
	for i := range m.rooms.spaces {
		if m.rooms.spaces[i].DisplayName() == names[0] {
			return m.rooms.spaces[i].Children
		}
	}
	return nil
}

// WithKeys replaces the keymap with one built from config; config issues are reported
// on the status line and in help, never fatal.
func (m Model) WithKeys(keys config.Keys) Model {
	m.keys = newKeymap(keys)
	if n := len(m.keys.issues); n > 0 {
		m = m.say(fmt.Sprintf("%d keybinding issue(s) — press %s for details", n, m.helpKey()))
	}
	return m
}

// helpKey names a key that opens help, falling back to the config field name.
func (m Model) helpKey() string {
	if bound := m.keys.labels[scopeCommand][actHelp]; len(bound) > 0 {
		return bound[0]
	}
	return "the key bound to keys.help"
}

// WithConfigFile sets where settings are written back and applies the config's
// integrations. Without a path, changes apply but are not saved.
func (m Model) WithConfigFile(path string, cfg config.Config) Model {
	m.conf.path = path
	m.conf.writer = &configWriter{}
	// main validates first; an error here means a config built in code, so fall back to defaults whole.
	derived, err := derive(cfg)
	if err != nil {
		m = m.sayErr("config", err)
		derived, _ = derive(config.Config{})
	}
	next := m.applyIntegrations(cfg, derived)
	// Script bindings live in [[commands.script]], not [keys], so bind them onto the
	// existing keymap rather than rebuilding it.
	next.keys = next.keys.withScripts(cfg.Commands.Scripts)
	// The rail is the spaces and the tags; New had no tags to build it from.
	return next.rebuiltRail()
}

// handleConfigNews is news of a change: kithd's verdict on one held back, its save,
// the daemon's re-read.
func (m Model) handleConfigNews(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case configCheckedMsg:
		return m.handleConfigChecked(msg)
	case configSavedMsg:
		return m.handleConfigSaved(msg)
	}
	reloaded, _ := msg.(configReloadedMsg)
	return m.handleConfigReloaded(reloaded)
}

// handleConfigSaved reports a failed write; on success it asks the daemon to re-read
// the file, since the daemon makes the notification decisions.
func (m Model) handleConfigSaved(msg configSavedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m = m.sayErr("setting applied but not saved", msg.err)
		return m, nil
	}
	return m, m.reloadConfigCmd()
}

// handleConfigReloaded reports a config the daemon refused, which leaves it running the old one.
func (m Model) handleConfigReloaded(msg configReloadedMsg) (Model, tea.Cmd) {
	return m.sayFailure("saved, but kithd refused it: ", msg.err), nil
}

// WithRestart wires in restarting the daemon, for :login to turn a network on; nil
// when this kith cannot.
func (m Model) WithRestart(restart func(context.Context) error) Model {
	m.link.restart = restart
	return m
}

// WithSchedules wires in the send-later queue.
func (m Model) WithSchedules(s Schedules) Model {
	m.schedules = s
	return m
}

// WithNotifications wires in the daemon's notification controls (do-not-disturb, config reload).
func (m Model) WithNotifications(n Notifications) Model {
	m.notifications.backend = n
	return m
}

// WithRules sets the notification rules to describe (the badge) and our MXID. Delivery is the daemon's.
func (m Model) WithRules(rules []notify.Rule, me string, on bool) Model {
	m.notifications.rules = rules
	m.me = me
	m.notifications.on = on
	return m
}

// WithCache gives the model a picture cache; without one it re-renders each time.
// WithLogger sets where failures are logged; the status line shows them briefly, the
// log keeps them. nil keeps the silent default.
func (m Model) WithLogger(log *slog.Logger) Model {
	if log != nil {
		m.log = log
	}
	return m
}

func (m Model) WithCache(cache *media.Cache) Model {
	m.pics.cache = cache
	return m
}

// dropImages forgets every picture drawn for the open room.
func (m Model) dropImages() Model {
	m.pics = m.pics.withNoImages()
	m.rowInputsChanged()
	return m
}

// homesOf is a room's spaces and tags (as tag:<name>), ordered by [display] priority:
// the first is the one that picks name rules, place rules and a download's folder.
// Tags are judged as places are (domain.TagSet.Of), as the daemon judges them.
func (m Model) homesOf(roomID domain.RoomID) []string {
	return domain.Homes(m.rooms.spaceNames(roomID), m.rail.roomFacts[roomID].Tags, m.rail.homes)
}

// spans reports whether a home holds every room — a tag of everything, as All: the
// same place as everywhere, so no scope offers it and a rail row of it does not decide
// a room's name rules.
func (m Model) spans(home string) bool { return m.rail.spanning[strings.ToLower(home)] }

// placeHomes is homesOf without the homes spanning every room, for offering scopes
// and for the rail row's say in name rules.
func (m Model) placeHomes(roomID domain.RoomID) []string {
	return slices.DeleteFunc(m.homesOf(roomID), m.spans)
}

// homeEntry is a home as a place entry: a tag is one already, a space is space:<name>.
func homeEntry(home string) string {
	if _, ok := domain.TagOf(home); ok {
		return home
	}
	return domain.SpaceEntry(home)
}

// roomByID looks up a joined room by ID.
func (m Model) roomByID(id domain.RoomID) (domain.Room, bool) { return m.rooms.byID(id) }

// handleImageLoaded records a finished inline-image load (nil rows on failure).
func (m Model) handleImageLoaded(msg imageLoadedMsg) (Model, tea.Cmd) {
	// Not retried and not on the status line (nobody asked); the row keeps its label.
	m.logErr(slog.LevelWarn, "load inline image", msg.err, "event", msg.eventID)
	m.pics = m.pics.loaded(msg.eventID, msg.rows)
	m.rowInputsChanged()
	return m, nil
}

// handleResize records the new frame size.
func (m Model) handleResize(msg tea.WindowSizeMsg) (Model, tea.Cmd) {
	was := m.width
	m.width, m.height = msg.Width, msg.Height
	m.ready = true
	// A picture is rendered at a fixed cell width, so a width change drops renders to re-fit them.
	if was != 0 && was != m.width && m.pics.graphics != graphicsNone {
		return m.dropImages(), nil
	}
	return m, nil
}

// handleReactionSent reports a failed reaction, clearing the status on success.
func (m Model) handleReactionSent(msg reactionSentMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m.clearStatus(), nil
	}
	// In a bridged room a refused reaction is learned and no longer offered (noteReactionFailure).
	if next, cmd := m.noteReactionFailure(msg.key); cmd != nil {
		return next, cmd
	}
	return m.sayErr("reaction failed", msg.err), nil
}

// maxInlineImages bounds how many inline images load at once.
const maxInlineImages = 60

// loadInlineImages issues capped, de-duplicated background loads for unloaded images
// in the shown messages.
func (m Model) loadInlineImages() (Model, tea.Cmd) {
	if !m.showsPictures() {
		return m, nil
	}
	targetW, maxH := m.imageTargetWidth(), m.imageMaxHeight()
	var cmds []tea.Cmd
	// Only what is drawn: pictures in collapsed threads are not fetched.
	shown := m.shownMessages()
	for i := range shown {
		msg := shown[i]
		if msg.ID == "" || !msg.Media.IsImage() {
			continue
		}
		if m.pics.settled(msg.ID) {
			continue
		}
		if m.pics.full(maxInlineImages) {
			break
		}
		// A room's media policy may forbid automatic fetches.
		policy := m.mediaPolicy(msg)
		if !policy.Auto {
			continue
		}
		m.pics = m.pics.loading(msg.ID)
		job := mediaJob{
			roomID: msg.RoomID, eventID: msg.ID,
			name: msg.Media.Name, mime: msg.Media.Mime,
			cache: policy.Cache,
		}
		cmds = append(cmds, m.loadImageCmd(job, targetW, maxH))
	}
	return m, tea.Batch(cmds...)
}

// imageTargetWidth is an inline picture's width in cells: the body column, or the configured cap if narrower.
func (m Model) imageTargetWidth() int {
	w := m.contentWidth() - (timestampWidth + 1 + m.nameColWidth() + 1)
	if capped := m.prefs.display.Media.MaxWidth; capped > 0 && capped < w {
		w = capped
	}
	return max(w, minImageCells)
}

// imageMaxHeight is an inline picture's max rows: the configured value, else half the message area within bounds.
func (m Model) imageMaxHeight() int {
	if h := m.prefs.display.Media.MaxHeight; h > 0 {
		return h
	}
	return min(max(m.msgAreaRows()/2, minImageCells), maxAutoImageRows)
}

// handleCachedTimeline merges cached messages for the open room; the pagination
// token is left to the live fetch.
func (m Model) handleCachedTimeline(msg cachedTimelineMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "load cached timeline", msg.err)
	if msg.err != nil {
		return m, nil
	}
	room, ok := m.currentRoom()
	if !ok || msg.roomID != room.ID {
		return m, nil
	}
	m = m.setMessages(domain.MergeMessages(m.timeline.messages, msg.messages))
	m, jump := m.resolveJump(false)
	m, opening := m.resolveThread(false)
	m, naming := m.nameOpenRoomThreads()
	return m, tea.Batch(jump, opening, naming)
}

// handleTimeline merges a fetched page for the open room (by timestamp, deduped),
// updates pagination and continues a backfill.
func (m Model) handleTimeline(msg timelineMsg) (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || msg.roomID != room.ID {
		return m, nil
	}
	m.timeline.hist.loading = false
	if msg.err != nil {
		m = m.sayErr("history error", msg.err)
		return m, nil
	}
	m = m.setMessages(domain.MergeMessages(m.timeline.messages, msg.page.Messages))
	m = m.withReactions(msg.page.Reactions...)
	m.timeline.hist.token = msg.page.Next
	m.timeline.hist.atStart = msg.page.Next == ""
	m.timeline.hist.ready = true
	// During a backfill, keep pulling older pages until the start or the cap.
	if m.timeline.hist.backfilling && !m.timeline.hist.atStart && len(m.timeline.messages) < maxTimelineMessages {
		m.timeline.hist.loading = true
		m = m.doing(fmt.Sprintf("loading full history… %d messages", len(m.timeline.messages)))
		return m, m.loadTimelineCmd(room.ID, m.timeline.hist.token)
	}
	m.timeline.hist.backfilling = false
	m = m.doing(historyStatus(len(m.timeline.messages), m.timeline.hist.atStart))
	// The live page landed, so a still-missing jump target never will; resolveJump reports it.
	m, jump := m.resolveJump(true)
	m, opening := m.resolveThread(true)
	m, naming := m.nameOpenRoomThreads()
	// A room opened before its history landed marks read now (markRead dedupes).
	if m.focus == paneTimeline {
		next, cmd := m.markRead()
		return next, tea.Batch(jump, opening, cmd, naming)
	}
	return m, tea.Batch(jump, opening, naming)
}

// handleVerify applies one verification step and keeps listening. Steps for another
// transaction are ignored.
func (m Model) handleVerify(msg verifyMsg) (Model, tea.Cmd) {
	v := msg.v
	switch v.Kind {
	case domain.VerificationRequested:
		if m.verify.active && m.verify.ours && m.verify.txnID == v.TxnID {
			// Our own request echoed back; not a new one.
			break
		}
		m.verify = verifyState{active: true, txnID: v.TxnID, stage: v.Kind, from: v.From, device: v.Device}
	case domain.VerificationSAS:
		if m.verify.active && m.verify.txnID == v.TxnID {
			m.verify.stage = v.Kind
			m.verify.emojis = v.Emojis
			m.verify.decimals = v.Decimals
			m.verify.waiting = false
		}
	case domain.VerificationDone:
		if m.verify.txnID == v.TxnID {
			m.verify = verifyState{}
			m = m.say("✓ device verified")
		}
	case domain.VerificationCanceled:
		if m.verify.txnID == v.TxnID {
			m.verify = verifyState{}
			m = m.say("verification canceled: " + v.Reason)
		}
	case domain.VerificationRestored:
		// Arrives after Done dismissed the overlay; just report the import.
		m = m.say("✓ " + v.Reason)
	}
	return m, m.listenVerifyCmd()
}

// handleVerifyStarted raises the overlay for our own request, or says why it could not be made.
func (m Model) handleVerifyStarted(msg verifyStartedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		if errors.Is(msg.err, api.ErrNoEncryption) {
			return m.say("verification needs encryption, which is not set up for this session"), nil
		}
		return m.sayErr("could not ask", msg.err), nil
	}
	m.verify = verifyState{
		active: true, ours: true, txnID: msg.txnID, stage: domain.VerificationRequested,
	}
	return m, nil
}

// handleVerifyFailed clears waiting after an accept/confirm failed to reach the
// daemon, handing the keyboard back. The overlay stays: the request is still open on
// the other device.
func (m Model) handleVerifyFailed(msg verifyFailedMsg) (Model, tea.Cmd) {
	m.verify.waiting = false
	return m.sayErr("verification failed", msg.err), nil
}

// handleVerifyKey drives the overlay: [y] accepts or confirms, [n]/esc cancels. Keys
// are ignored while an accept/confirm is in flight.
func (m Model) handleVerifyKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.verify.waiting {
		return m, nil
	}
	txnID := m.verify.txnID
	switch m.keys.lookup(key.String(), scopeVerify) {
	case actConfirm:
		switch m.verify.stage {
		case domain.VerificationDone, domain.VerificationCanceled, domain.VerificationRestored:
			// Nothing left to confirm.
		case domain.VerificationRequested:
			if m.verify.ours {
				// Our own request: the other device decides.
				return m, nil
			}
			m.verify.waiting = true
			m = m.say("verifying… compare the emoji when they appear")
			return m, m.acceptVerifyCmd(txnID)
		case domain.VerificationSAS:
			m.verify.waiting = true
			m = m.say("confirming…")
			return m, m.confirmSASCmd(txnID)
		}
	case actCancel:
		m.verify = verifyState{}
		m = m.say("verification canceled")
		return m, m.cancelVerifyCmd(txnID)
	}
	return m, nil
}

func (m Model) handleIncoming(msg incomingMsg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	// Every room's messages arrive here, which keeps activity ordering current.
	m = m.noteActivity(msg.message)
	if room, ok := m.currentRoom(); ok && msg.message.RoomID == room.ID {
		scrolled := m.timeline.scroll > 0
		m = m.keepAnchored(func(m Model) Model {
			return m.setMessages(domain.MergeMessages(m.timeline.messages, []domain.Message{msg.message}))
		})
		if !scrolled && m.focus == paneTimeline {
			// Watching live at the bottom: keep it marked read.
			var readCmd tea.Cmd
			m, readCmd = m.markRead()
			cmds = append(cmds, readCmd)
		}
	}
	cmds = append(cmds, m.listenCmd())
	return m, tea.Batch(cmds...)
}

// handleSent reports a failed send; on success it marks the conversation read —
// replying implies reading, and while composing the focus-gated mark in handleIncoming
// does not fire. markRead targets the open thread when there is one.
func (m Model) handleSent(msg sentMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		// Logged here: the status line may only say "queued, retrying".
		m.logErr(slog.LevelWarn, "send failed", msg.err, "room", msg.roomID)
		return m.queueUnsent(msg)
	}
	m = m.clearStatus()
	return m.markRead()
}

// queueUnsent hands a message the homeserver refused to the daemon's send queue (the
// `/at` queue, due now), so it is retried even after this client exits. Past
// schedule_cutoff it stops retrying. See daemon.Scheduler.
func (m Model) queueUnsent(msg sentMsg) (Model, tea.Cmd) {
	// Edits and empty drafts are not queued: a late replacement is worse than a failed one.
	if msg.draft.Edits != "" || strings.TrimSpace(msg.draft.Body) == "" {
		return m.sayErr("send failed", msg.err), nil
	}
	now := time.Now()
	return m, m.queueUnsentCmd(sentMsg{roomID: msg.roomID, draft: msg.draft}, domain.ScheduledMessage{
		RoomID:     msg.roomID,
		Body:       msg.draft.Body,
		ThreadRoot: msg.draft.ThreadRoot,
		ReplyTo:    msg.draft.ReplyTo,
		Mentions:   msg.draft.Mentions,
		Emote:      msg.draft.Emote,
		// The same TxnID, so a send that actually went through is not delivered twice.
		TxnID:   msg.draft.TxnID,
		At:      now.UTC(),
		Written: now.UTC(),
	})
}

// handleQueuedUnsent reports a failed send as queued, or — with no queue — puts the
// words back as the room's draft.
func (m Model) handleQueuedUnsent(msg queuedUnsentMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m.say("send failed — queued, retrying"), nil
	}
	m.logErr(slog.LevelWarn, "queue failed send", msg.err, "room", msg.roomID)
	m = m.putBackDraft(msg.roomID, msg.draft)
	return m.say("send failed and could not be queued — the words are back in " +
		m.namedRoom(msg.roomID) + "'s draft"), nil
}

// putBackDraft returns an unsendable message to its room's draft, prepended to what
// is there now and separated by a blank line.
func (m Model) putBackDraft(roomID domain.RoomID, unsent domain.Draft) Model {
	if roomID == m.openRoom {
		ed := m.editorFor(fieldComposer)
		body := unsent.Body
		if ed.text != "" {
			body += "\n\n" + ed.text
		}
		m = m.store(fieldComposer, newEditor(body))
		m.compose.drafted = append(unsent.Mentions, m.compose.drafted...)
		if m.compose.replyTo == "" {
			m.compose.replyTo = unsent.ReplyTo
		}
		return m
	}
	held := m.drafts[roomID]
	if held.input != "" {
		held.input = unsent.Body + "\n\n" + held.input
	} else {
		held.input = unsent.Body
		held.replyTo = unsent.ReplyTo
	}
	held.caret = len(held.input)
	held.drafted = append(unsent.Mentions, held.drafted...)
	drafts := maps.Clone(m.drafts)
	if drafts == nil {
		drafts = map[domain.RoomID]draft{}
	}
	drafts[roomID] = held
	m.drafts = drafts
	return m.rebuiltRail()
}

// namedRoom names a room for a sentence, falling back to its ID.
func (m Model) namedRoom(id domain.RoomID) string {
	if room, ok := m.rooms.byID(id); ok {
		return m.roomName(room)
	}
	return string(id)
}

// filteredRooms returns the selected group's rooms in its configured order. The
// Unread group also keeps the open room listed until you move off it.
func (m Model) filteredRooms() []domain.Room {
	g, ok := m.rail.at()
	if !ok {
		return nil
	}
	pinned := g.sticky && m.openRoom != ""
	view := m.unreadView()
	out := make([]domain.Room, 0, len(m.rooms.all))
	// Indexed: domain.Room is large enough that a range copy trips gocritic.
	for i := range m.rooms.all {
		if g.admits(view, m.rooms.all[i]) || (pinned && m.rooms.all[i].ID == m.openRoom) {
			out = append(out, m.rooms.all[i])
		}
	}
	// Ordered per group, since spaces can be configured differently.
	return m.orderRooms(out, m.roomList(g.key))
}

// indexOfRoom returns the position of the room with id in rooms, or -1.
func indexOfRoom(rooms []domain.Room, id domain.RoomID) int {
	for i := range rooms {
		if rooms[i].ID == id {
			return i
		}
	}
	return -1
}

// indexOfGroup returns the position of the group with key, or 0 when it is gone.
func indexOfGroup(groups []group, key string) int {
	return max(slices.IndexFunc(groups, func(g group) bool { return g.key == key }), 0)
}

// currentRoom returns the open room, looked up by ID among the joined rooms.
func (m Model) currentRoom() (domain.Room, bool) {
	if m.openRoom == "" {
		return domain.Room{}, false
	}
	return m.rooms.byID(m.openRoom)
}

func historyStatus(count int, atStart bool) string {
	if atStart {
		return fmt.Sprintf("%d message(s) · start of room", count)
	}
	return fmt.Sprintf("%d message(s) · [pgup] older", count)
}

// handleSyncEnded records that the streams closed for good (Start returned). Unary
// calls still work, so without the badge the client would look healthy with nothing
// live arriving; the badge asks for a restart.
func (m Model) handleSyncEnded(msg syncEndedMsg) (Model, tea.Cmd) {
	m.link.detached, m.link.ended = true, true
	m.logErr(slog.LevelError, "sync stopped", msg.err)
	return m.say(syncStatus(msg.err)), nil
}

func syncStatus(err error) string {
	if err != nil {
		return "sync stopped: " + err.Error()
	}
	return "sync stopped"
}

// answered spreads a (Model, tea.Cmd) pair into the routers' three-value form:
// `return answered(m.doSomething())`.
func answered(mdl Model, cmd tea.Cmd) (Model, tea.Cmd, bool) {
	return mdl, cmd, true
}
