package tui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/theme"
)

// applyConfig makes cfg the running configuration: it re-derives everything the UI
// reads from it, keeps the rail cursor on focusGroup (empty = wherever it was), reports
// done on the status line, and writes the file. Every in-app setting change funnels
// through here so no derived structure is left stale.
//
// A change to a network's accounts is the network's to judge, and only kithd links
// the networks: it is applied once kithd has said it would run with it
// (configCheckedMsg), unless another change was applied meanwhile, which it would
// undo (it was made over the config before that one).
func (m Model) applyConfig(cfg config.Config, done string) (Model, tea.Cmd) {
	// Rules are derived only to validate them; the daemon resolves them itself.
	derived, err := derive(cfg)
	if err != nil {
		m = m.sayErr("could not apply", err)
		return m, nil
	}
	checker, ok := m.backend.(configChecker)
	if !ok || !networksChanged(m.conf.base, cfg) {
		return m.applyDerived(cfg, derived, done)
	}
	app, over := m.ctx, m.conf.applied
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(app, configCheckTimeout)
		defer cancel()
		return configCheckedMsg{cfg: cfg, derived: derived, done: done, over: over, err: checker.CheckConfig(ctx, cfg)}
	}
}

// configChecker is a daemon that says whether it would run with a config.
type configChecker interface {
	CheckConfig(ctx context.Context, cfg config.Config) error
}

// configCheckTimeout bounds kithd's answer: a network may ask its own servers.
const configCheckTimeout = 15 * time.Second

// configCheckedMsg is kithd's verdict on a change applyConfig held back.
type configCheckedMsg struct {
	cfg     config.Config
	derived derivations
	done    string
	// over is how many changes had been applied when it was made (configState.applied).
	over uint64
	err  error
}

// handleConfigChecked applies the held change, or says why kithd refused it.
func (m Model) handleConfigChecked(msg configCheckedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not apply", msg.err), nil
	}
	if msg.over != m.conf.applied {
		return m.say("not applied: another change landed while kithd checked this one — make it again"), nil
	}
	return m.applyDerived(msg.cfg, msg.derived, msg.done)
}

// networksChanged reports whether any network's section differs between two configs.
func networksChanged(was, now config.Config) bool {
	return !reflect.DeepEqual(was.Networks(), now.Networks())
}

// applyDerived makes cfg, already checked and derived, the running configuration,
// and has the daemon save it.
func (m Model) applyDerived(cfg config.Config, derived derivations, done string) (Model, tea.Cmd) {
	next, cmd := m.runDerived(cfg, derived, done)
	return next, tea.Batch(next.saveConfigFileCmd(cfg), cmd)
}

// runDerived makes cfg, already checked and derived, the running configuration.
func (m Model) runDerived(cfg config.Config, derived derivations, done string) (Model, tea.Cmd) {
	m.conf.applied++
	m = m.applyIntegrations(cfg, derived)
	m.prefs.display = cfg.Display
	m.keys = keymapFor(cfg)
	m.theme = theme.New(derived.palette)
	m.prefs.identities = buildIdentities(cfg.Display.Identities)
	m.receipts.policy = readSettingsFrom(cfg.Display)
	// Invalidates the timeline's cached colors, name column and rows.
	m.conf.rev++
	m = m.applyNaming(cfg.Display)
	m.prefs.openInsert = cfg.Display.OpenInInsertMode()
	m.pics.mode = derived.media
	m.pics.graphics = resolveGraphics(m.pics.mode)
	m.pics.blocks = resolveBlocks(derived.detail)
	m.glyphs.scope = cfg.Display.Reactions.Scope
	m = m.applyMedia(cfg.Display.Media)
	// Set before the rail is rebuilt: a group's Unread filter captures unreadView.
	m.prefs.unreadLocal = derived.unreadLocal
	m.rail.spam = setup.SpamPlaces(cfg.Spam)
	m = m.refreshPlaces()
	// A room set by hand, or auto turned on or off, applies to the open room now.
	m = m.resettleLayout()
	m.glyphs.skin = derived.tone
	m.glyphs.set = derived.emoji
	m.glyphs.static = toneEach(cappedStatic(cfg.Display.Reactions.Static), derived.tone)
	m.glyphs.palette = m.toneAll(m.glyphs.palette)

	// Keep the rail cursor on a group by key rather than by position.
	focusGroup := ""
	if entry, ok := m.currentGroup(); ok {
		focusGroup = entry.key
	}
	m.rail.groups = railGroups(m.rooms.spaces, cfg.Display.Rail, cfg.Display.Names, m.unreadView(), m.rooms.all)
	if at := indexOfGroup(m.rail.groups, focusGroup); at >= 0 && at < len(m.rail.groups) {
		m.rail.cursor = at
	}
	m.rail.cursor = clampIndex(m.rail.cursor, len(m.rail.groups))

	m.notifications.rules = derived.rules
	m.notifications.on = cfg.Notifications.Enabled

	m = m.say(done)
	// A just-archived room needs its parent space resolved to be filed in the rail.
	return m, m.resolveParentsCmd()
}

// adoptConfig makes a configuration changed elsewhere (another window, a hand edit,
// the daemon itself) the running one, without saving it back: it is the file already.
// note is what the status line says.
func (m Model) adoptConfig(cfg config.Config, note string) (Model, tea.Cmd) {
	derived, err := derive(cfg)
	if err != nil {
		return m.sayErr("the configuration changed elsewhere, and this window cannot run it", err), nil
	}
	return m.runDerived(cfg, derived, note)
}

// applyIntegrations sets what is read off the config directly rather than derived.
// Shared with WithConfigFile so startup and settings changes use one policy.
func (m Model) applyIntegrations(cfg config.Config, derived derivations) Model {
	m.conf.base = cfg
	m.prefs.external.clipboard = cfg.Clipboard.Command
	m.prefs.external.copyDownloads = cfg.Clipboard.CopyDownloads
	m.prefs.external.open = cfg.Clipboard.OpenCommandOrDefault()
	m.prefs.external.focus = cfg.Clipboard.FocusCommand
	m.prefs.codes.rules, m.prefs.codes.scope = derived.codeRules, derived.codeScope
	m.rail.tags = derived.tags
	m.rail.archives, m.rail.mirrored = derived.archives, derived.mirrored
	m.rail.tagsRev++
	if m.rail.tagMemo == nil {
		m.rail.tagMemo = &tagMemo{}
	}
	if len(derived.tagWarnings) > 0 {
		said := strings.Join(derived.tagWarnings, "; ")
		if m.log != nil {
			m.log.Warn("[[tag]]: " + said)
		}
		m = m.say(said)
	}
	return m
}

// applyNaming rebuilds the room/thread aliases and per-group room-list rules.
func (m Model) applyNaming(display config.Display) Model {
	m.prefs.roomAliases = buildRoomAliases(display.Names)
	m.prefs.threadAliases = buildThreadAliases(display.Names)
	m.rail.rules = roomListRules(display.Rooms)
	m.prefs.tracked = setup.TrackedRules(display.Tracked)
	return m
}

// derivations are the values a config means, resolved together because a config that
// cannot produce all of them is refused whole.
type derivations struct {
	rules []notify.Rule
	// unreadLocal: badges count cached unread messages rather than notifications.
	unreadLocal bool
	tone        string
	media       string
	detail      string
	emoji       emojiSet
	codeRules   domain.CodeRules
	codeScope   domain.CodeScope
	palette     theme.Palette
	// tags are the [[tag]]s, and tagWarnings the cycles among them (said, not fatal).
	tags        domain.TagSet
	tagWarnings []string
	// archives is the tag each followed network's archive is; mirrored the networks
	// whose archive kith's filing changes (fileTags).
	archives map[domain.Protocol]string
	mirrored map[domain.Protocol]string
}

// derive resolves a config into what the model reads, or returns the first error.
func derive(cfg config.Config) (derivations, error) {
	// Everything the loader checks: a change the app applies must be a config that
	// loads, or the daemon and the next start refuse what was saved.
	if err := setup.Validate(cfg); err != nil {
		return derivations{}, err
	}
	rules, err := setup.NotificationRules(cfg.Notifications)
	if err != nil {
		return derivations{}, err
	}
	tone, err := setup.SkinTone(cfg.Display.SkinTone)
	if err != nil {
		return derivations{}, err
	}
	tier, err := setup.EmojiTier(cfg.Display.Emoji.Set)
	if err != nil {
		return derivations{}, err
	}
	codeRules, codeScope, err := setup.Codes(cfg.Codes)
	if err != nil {
		return derivations{}, err
	}
	media, err := setup.MediaMode(cfg.Display.Media.Mode)
	if err != nil {
		return derivations{}, err
	}
	source, err := setup.UnreadSource(cfg.Display.Unread)
	if err != nil {
		return derivations{}, err
	}
	// Validated only: roomOrder resolves the order per group on every draw.
	if bad := setup.RoomList(cfg.Display.Rooms); bad != nil {
		return derivations{}, bad
	}
	detail, err := setup.MediaDetail(cfg.Display.Media.Detail)
	if err != nil {
		return derivations{}, err
	}
	palette, err := theme.Resolve(cfg.Display.Theme.Preset, cfg.Display.Theme.Overrides())
	if err != nil {
		return derivations{}, fmt.Errorf("display.theme: %w", err)
	}
	tags, tagWarnings, err := setup.Tags(cfg)
	if err != nil {
		return derivations{}, err
	}
	archives, err := setup.Archives(cfg, tags) // validated above: never fails here
	return derivations{
		tags:        tags,
		tagWarnings: tagWarnings,
		archives:    archives,
		mirrored:    setup.Mirrored(cfg, tags),
		palette:     palette,
		rules:       rules,
		unreadLocal: source != config.UnreadNotifications,
		tone:        tone,
		media:       media,
		detail:      detail,
		emoji:       newEmojiSet(tier, cfg.Display.Emoji.Extra),
		codeRules:   codeRules,
		codeScope:   codeScope,
	}, err
}

// applyDisplay changes the display settings and applies them.
func (m Model) applyDisplay(display config.Display, done string) (Model, tea.Cmd) {
	// Taken before the change, which can remove the row the cursor is on.
	was := m.roomCursor()
	cfg := m.conf.base.Clone()
	cfg.Display = display
	next, cmd := m.applyConfig(cfg, done)
	moved, move := next.keepCursorNearby(was)
	return moved, tea.Batch(cmd, move)
}

// keepCursorNearby selects a neighbor when the change took the open room out of the
// room list; otherwise the cursor would fall to row zero. Rows after a removed one
// shift up, so whatever now sits at the old index is the next room.
func (m Model) keepCursorNearby(was int) (Model, tea.Cmd) {
	if m.openRoom == "" {
		return m, nil
	}
	rows := m.roomRows()
	for i := range rows {
		if rows[i].room.ID == m.openRoom {
			return m, nil
		}
	}
	// Keep the action's status message ("archived Alpha") over the room's "loading…".
	said, until := m.st.event, m.st.until

	row, found := selectableNear(rows, was)
	next, cmd := m, tea.Cmd(nil)
	if !found {
		next, cmd = m.clearRoom()
	} else {
		next, cmd = m.selectRow(row)
	}
	next.st.event, next.st.until = said, until
	return next, cmd
}

// selectableNear is the first selectable row at or after from, and failing that the
// last one before it.
func selectableNear(rows []roomRow, from int) (roomRow, bool) {
	for i := max(from, 0); i < len(rows); i++ {
		if rows[i].selectable() {
			return rows[i], true
		}
	}
	for i := min(from, len(rows)) - 1; i >= 0; i-- {
		if rows[i].selectable() {
			return rows[i], true
		}
	}
	return roomRow{}, false
}

// applyNotifications changes the notification settings and applies them.
func (m Model) applyNotifications(notifs config.Notifications, done string) (Model, tea.Cmd) {
	cfg := m.conf.base.Clone()
	cfg.Notifications = notifs
	return m.applyConfig(cfg, done)
}

// paletteOf resolves a display config's colors, falling back to the default palette
// (a bad preset was already refused at startup; derive is the erroring version).
func paletteOf(display config.Display) theme.Palette {
	palette, err := theme.Resolve(display.Theme.Preset, display.Theme.Overrides())
	if err != nil {
		return theme.Default()
	}
	return palette
}

// applyMedia sets the attachment fetch/keep policy, per-place rules and voice speed.
func (m Model) applyMedia(media config.Media) Model {
	m.pics.auto = media.AutoLoad()
	m.pics.caching = media.Caching()
	m.pics.rules = mediaRules(media)
	m.pics.speed = media.Audio.PlaySpeed()
	return m
}
