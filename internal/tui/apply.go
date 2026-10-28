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

// applyConfig makes cfg the bridge's running configuration (applySettings).
func (m Model) applyConfig(cfg config.Config, done string) (Model, tea.Cmd) {
	return m.applySettings(config.Settings{Bridge: cfg, Device: m.conf.device}, done)
}

// applyDevice makes dev this device's running settings (applySettings).
func (m Model) applyDevice(dev config.Device, done string) (Model, tea.Cmd) {
	return m.applySettings(config.Settings{Bridge: m.conf.base, Device: dev}, done)
}

// settings is what the window runs on: the bridge's config and this device's.
func (m Model) settings() config.Settings {
	return config.Settings{Bridge: m.conf.base, Device: m.conf.device}
}

// applySettings makes s the running configuration: re-derives everything the UI reads,
// keeps the rail cursor on focusGroup (empty = where it was), reports done, and writes
// each file that changed, the bridge's through kithd and the device's here. Every
// in-app setting change goes through here so nothing derived goes stale.
//
// A change to a network's accounts waits for kithd to accept it (configCheckedMsg),
// and is dropped if another change was applied meanwhile, which it would undo.
func (m Model) applySettings(s config.Settings, done string) (Model, tea.Cmd) {
	// Rules are derived only to validate them; the daemon resolves them itself.
	derived, err := derive(s, m.profiles.current)
	if err != nil {
		m = m.sayErr("could not apply", err)
		return m, nil
	}
	checker, ok := m.backend.(configChecker)
	if !ok || !networksChanged(m.conf.base, s.Bridge) {
		return m.applyDerived(s, derived, done)
	}
	app, over := m.ctx, m.conf.applied
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(app, configCheckTimeout)
		defer cancel()
		return configCheckedMsg{cfg: s, derived: derived, done: done, over: over, err: checker.CheckConfig(ctx, s.Bridge)}
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
	cfg     config.Settings
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

// applyDerived makes s, already checked and derived, the running configuration, has
// the daemon save the bridge's config if it changed, and saves the device's if it did.
func (m Model) applyDerived(s config.Settings, derived derivations, done string) (Model, tea.Cmd) {
	was := m.settings()
	next, cmd := m.runDerived(s, derived, done)
	var saves []tea.Cmd
	if !reflect.DeepEqual(was.Bridge, s.Bridge) {
		saves = append(saves, next.saveConfigFileCmd(s.Bridge))
	}
	if !reflect.DeepEqual(was.Device, s.Device) {
		saves = append(saves, next.saveDeviceCmd(s.Device))
	}
	return next, tea.Batch(append(saves, cmd)...)
}

// runDerived makes s, already checked and derived, the running configuration.
func (m Model) runDerived(s config.Settings, derived derivations, done string) (Model, tea.Cmd) {
	cfg, dev := s.Bridge, s.Device
	m.conf.applied++
	m = m.applyIntegrations(cfg, derived).runningOn(dev)
	m.prefs.display = cfg.Display
	m.keys = keymapFor(dev)
	m.theme = theme.New(derived.palette)
	m.prefs.identities = buildIdentities(cfg.Display.Identities)
	m.prefs.aliases = setup.Aliases(cfg.Display.Identities)
	m.prefs.clock = derived.clock
	m.receipts.policy = readSettingsFrom(dev.Display)
	// Invalidates the timeline's cached colors, name column and rows.
	m.conf.rev++
	m = m.applyNaming(cfg.Display, dev.Display)
	m.prefs.openInsert = dev.Display.OpenInInsertMode()
	m.pics.mode = derived.media
	m.pics.graphics = resolveGraphics(m.pics.mode)
	m.pics.blocks = resolveBlocks(derived.detail)
	m.glyphs.scope = dev.Display.Reactions.Scope
	m = m.applyMedia(cfg.Display.Media)
	// Set before the rail is rebuilt: a group's Unread filter captures unreadView.
	m.prefs.unreadLocal = derived.unreadLocal
	m = m.refreshPlaces()
	// A room set by hand, or auto turned on or off, applies to the open room now.
	m = m.resettleLayout()
	m.glyphs.skin = derived.tone
	m.glyphs.set = derived.emoji
	m.glyphs.static = toneEach(cappedStatic(dev.Display.Reactions.Static), derived.tone)
	m.glyphs.palette = m.toneAll(m.glyphs.palette)

	// Keep the rail cursor on a group by key, not position.
	focusGroup := ""
	if entry, ok := m.currentGroup(); ok {
		focusGroup = entry.key
	}
	m.rail.groups = m.railFromState()
	if at := indexOfGroup(m.rail.groups, focusGroup); at >= 0 && at < len(m.rail.groups) {
		m.rail.cursor = at
	}
	m.rail.cursor = clampIndex(m.rail.cursor, len(m.rail.groups))

	m.notifications.rules = derived.rules
	m.notifications.on = dev.Notifications.Enabled

	m = m.say(done)
	// Naming or unnaming a room renames the person it is named after.
	m, peers := m.refreshPeers()
	m, fields := m.refreshFieldMembers()
	// The folder may have moved.
	m, cards := m.loadCards(false, "")
	// A just-archived room needs its parent space resolved to be filed in the rail.
	return m, tea.Batch(m.resolveParentsCmd(), peers, fields, cards)
}

// adoptConfig runs a config changed elsewhere (another window, a hand edit, the
// daemon) without saving it back. note goes on the status line.
func (m Model) adoptConfig(cfg config.Config, note string) (Model, tea.Cmd) {
	return m.adoptSettings(config.Settings{Bridge: cfg, Device: m.conf.device}, note)
}

// adoptSettings is adoptConfig for either file.
func (m Model) adoptSettings(s config.Settings, note string) (Model, tea.Cmd) {
	derived, err := derive(s, m.profiles.current)
	if err != nil {
		return m.sayErr("the configuration changed elsewhere, and this window cannot run it", err), nil
	}
	return m.runDerived(s, derived, note)
}

// applyIntegrations sets what is read off the config directly rather than derived.
// Shared with WithConfigFile so startup and settings changes agree.
func (m Model) applyIntegrations(cfg config.Config, derived derivations) Model {
	m.conf.base = cfg
	m.prefs.phonebookDir = setup.PhonebookDir(cfg)
	m.prefs.codes.rules, m.prefs.codes.scope = derived.codeRules, derived.codeScope
	m.rail.tags = derived.tags
	m.rail.archives, m.rail.mirrored = derived.archives, derived.mirrored
	m.rail.archiveTags = derived.archiveTags
	m.rail.cfg = derived.rail
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

// runningOn makes dev the device's settings the window runs on, as read off it
// directly rather than derived.
func (m Model) runningOn(dev config.Device) Model {
	m.conf.device = dev
	m.prefs.device = dev.Display
	m.prefs.external = externalsOf(dev.Clipboard)
	return m
}

// externalsOf is a device's [clipboard] commands for copying and opening links.
func externalsOf(c config.DeviceClipboard) externalCommands {
	return externalCommands{
		clipboard: c.Command, copyDownloads: c.CopyDownloads, open: c.OpenCommandOrDefault(),
		focus: c.FocusCommand, raise: c.RaiseCommand,
	}
}

// applyNaming rebuilds the room/thread aliases and per-group room-list rules.
func (m Model) applyNaming(display config.Display, device config.DeviceDisplay) Model {
	m.prefs.roomAliases = buildRoomAliases(display.Names)
	m.prefs.threadAliases = buildThreadAliases(display.Names)
	m.rail.rules = roomListRules(device.Rooms)
	m.prefs.tracked = setup.TrackedRules(display.Tracked)
	return m
}

// derivations are the values a config means, resolved together so a config that
// cannot produce all of them is refused whole.
type derivations struct {
	rules []notify.Rule
	// unreadLocal: badges count cached unread messages rather than notifications.
	unreadLocal bool
	tone        string
	clock       domain.Clock
	media       string
	detail      string
	emoji       emojiSet
	codeRules   domain.CodeRules
	codeScope   domain.CodeScope
	palette     theme.Palette
	// tags are the [[tag]]s, and tagWarnings the cycles among them (said, not fatal).
	tags        domain.TagSet
	tagWarnings []string
	// archives is the archive tag of each followed network; mirrored lists the networks
	// whose archive kith's filing changes (fileTags).
	archives map[domain.Protocol]string
	mirrored map[domain.Protocol]string
	// archiveTags is every tag an archive is (setup.ArchiveTags).
	archiveTags []string
	// rail is the rail the profile draws: its own, or [display.rail].
	rail config.Rail
}

// derive resolves both files into what the model reads in a profile ("" the first),
// or returns the first error. The tags and the rail are the profile's.
func derive(s config.Settings, profile string) (derivations, error) {
	cfg, dev := s.Bridge, s.Device
	// A config the app applies must load, or the daemon and the next start refuse it.
	if err := setup.Validate(cfg); err != nil {
		return derivations{}, err
	}
	if err := setup.ValidateDevice(dev, cfg); err != nil {
		return derivations{}, err
	}
	rules, err := setup.NotificationRules(cfg.Notifications)
	if err != nil {
		return derivations{}, err
	}
	tone, err := setup.SkinTone(dev.Display.SkinTone)
	if err != nil {
		return derivations{}, err
	}
	clock, _ := setup.Clock(cfg.Display) // checked by Validate above
	tier, err := setup.EmojiTier(dev.Display.Emoji.Set)
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
	source, err := setup.UnreadSource(dev.Display.Unread)
	if err != nil {
		return derivations{}, err
	}
	// Validated only: roomOrder resolves the order per group on every draw.
	if bad := setup.RoomList(dev.Display.Rooms); bad != nil {
		return derivations{}, bad
	}
	detail, err := setup.MediaDetail(cfg.Display.Media.Detail)
	if err != nil {
		return derivations{}, err
	}
	palette, err := theme.Resolve(dev.Display.Theme.Preset, dev.Display.Theme.Overrides())
	if err != nil {
		return derivations{}, fmt.Errorf("display.theme: %w", err)
	}
	out, err := placesIn(setup.ProfileConfig(cfg, profile))
	if err != nil {
		return derivations{}, err
	}
	out.palette, out.rules, out.unreadLocal = palette, rules, source != config.UnreadNotifications
	out.tone, out.clock, out.media, out.detail = tone, clock, media, detail
	out.emoji, out.codeRules, out.codeScope = newEmojiSet(tier, dev.Display.Emoji.Extra), codeRules, codeScope
	return out, nil
}

// placesIn is the tags, archives and rail of a profile's view of the config
// (setup.ProfileConfig).
func placesIn(view config.Config) (derivations, error) {
	tags, tagWarnings, err := setup.Tags(view)
	if err != nil {
		return derivations{}, err
	}
	archives, err := setup.Archives(view, tags) // validated by derive: never fails here
	return derivations{
		tags:        tags,
		tagWarnings: tagWarnings,
		archives:    archives,
		mirrored:    setup.Mirrored(view, tags),
		archiveTags: setup.ArchiveTags(view, tags),
		rail:        view.Display.Rail,
	}, err
}

func (m Model) applyDisplay(display config.Display, done string) (Model, tea.Cmd) {
	// Taken before the change, which can remove the row the cursor is on.
	was := m.roomCursor()
	cfg := m.conf.base.Clone()
	cfg.Display = display
	next, cmd := m.applyConfig(cfg, done)
	moved, move := next.keepCursorNearby(was)
	return moved, tea.Batch(cmd, move)
}

// keepCursorNearby selects a neighbor when the change removed the open room from the
// list, instead of falling to row zero. Rows after a removed one shift up, so the old
// index now holds the next room.
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

func (m Model) applyNotifications(notifs config.Notifications, done string) (Model, tea.Cmd) {
	cfg := m.conf.base.Clone()
	cfg.Notifications = notifs
	return m.applyConfig(cfg, done)
}

// paletteOf resolves a device's colors, falling back to the default palette (a bad
// preset was refused at startup; derive is the erroring version).
func paletteOf(display config.DeviceDisplay) theme.Palette {
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
	m.pics.anonymize = media.Anonymizes()
	m.pics.rules = mediaRules(media)
	m.pics.speed = media.Audio.PlaySpeed()
	return m
}
