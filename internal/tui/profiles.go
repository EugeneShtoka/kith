package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Profiles switches the [[profile]] this window runs in. Declared here because only
// daemon.Remote implements it.
type Profiles interface {
	// SwitchProfile runs every call in the named profile, returning once every stream
	// runs in it; refused (api.ErrSeatTaken) while another window runs in it.
	SwitchProfile(name string) error
}

// Presence tells the daemon what this window is doing, which a clicked link is handed
// to a window by. Declared here because only daemon.Remote implements it.
type Presence interface {
	// Present says the room open ("" none), and that the window was used just now.
	Present(ctx context.Context, open domain.RoomID) error
}

// presenceState is what this window last told the daemon: the room open, and when.
type presenceState struct {
	to   Presence // nil tells nothing
	open domain.RoomID
	at   time.Time
}

// presenceEvery is how often a window in use says so, at most; opening a room says so
// at once.
const presenceEvery = 2 * time.Second

// profileState is the [[profile]] this window runs in.
type profileState struct {
	switcher Profiles // nil: this window cannot switch
	current  string   // as the config spells it; "" is the first
	// switches counts the switches landed: a room or invitation list asked before the
	// last one is another profile's (a switch and back makes the name the same again).
	switches int
	// to is the profile a switch is under way to, until it lands; sent once it has gone
	// to the daemon, which waits for the drafts' writes (switchWhenDrafted).
	to   string
	sent bool
	// arrival is what the window has read of the profile it switched to, until it has
	// read both its rooms and its invitations.
	arrival profileArrival
}

// profileArrival is a switch's landing until the new profile's lists are read: which
// have been, and the room open as it landed (a chat a link opened then is the profile
// left's). kithd refuses every call naming a room outside the profile, so a room the
// lists read do not hold goes, as a room that is gone does.
type profileArrival struct {
	due            bool
	room           domain.RoomID
	rooms, invites bool
}

// profileSwitchedMsg says the window now runs in name, or why it does not.
type profileSwitchedMsg struct {
	name string
	err  error
}

// WithProfiles sets the switcher and the profile this window starts in ("" the first).
// Started in none (neither --profile nor the device's profile_in_use names one) while the
// config has several, the window offers them: the first runs until one is picked.
func (m Model) WithProfiles(p Profiles, current string) Model {
	m.profiles = profileState{switcher: p, current: current}
	if current == "" && p != nil && len(m.profileNames()) > 1 {
		m = m.offerProfiles("which profile? the pick is kept for the next start")
	}
	if current == "" || len(m.conf.base.Profiles) == 0 {
		return m // the first profile's, as the config was applied; or no config yet
	}
	return m.runProfile()
}

// runProfile derives the tags and the rail for the profile this window runs in: a
// profile's archive and rail can differ from another's. The new tags revision drops
// the tag memo.
func (m Model) runProfile() Model {
	derived, err := derive(m.settings(), m.profiles.current)
	if err != nil {
		return m.sayErr("profile "+m.currentProfile(), err)
	}
	return m.applyIntegrations(m.conf.base, derived).refreshPlaces().rebuiltRail()
}

// WithPresence sets where this window tells what it is doing; nil tells nothing.
func (m Model) WithPresence(p Presence) Model {
	m.presence = presenceState{to: p}
	return m
}

// armPresence tells the daemon this window is in use: at once when the open room
// changed, else on a key at most every presenceEvery. A failed telling is not news: the
// next key tells it again.
func (m Model) armPresence(msg tea.Msg) (Model, tea.Cmd) {
	if m.presence.to == nil {
		return m, nil
	}
	_, pressed := msg.(tea.KeyPressMsg)
	now := time.Now()
	if m.openRoom == m.presence.open && (!pressed || now.Sub(m.presence.at) < presenceEvery) {
		return m, nil
	}
	m.presence.open, m.presence.at = m.openRoom, now
	ctx, to, open := m.ctx, m.presence.to, m.openRoom
	return m, func() tea.Msg {
		_ = to.Present(ctx, open) // see above
		return nil
	}
}

// profileNames is the config's profiles in order; the config was checked on load.
func (m Model) profileNames() []string {
	profiles, _ := setup.Profiles(m.conf.base)
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names
}

// currentProfile is the profile this window runs in, by name.
func (m Model) currentProfile() string {
	if m.profiles.current != "" {
		return m.profiles.current
	}
	return m.profileNames()[0]
}

// openProfiles lists the profiles to switch to (`:profile`, the profile key).
func (m Model) openProfiles() (Model, tea.Cmd) {
	if names, current := m.profileNames(), m.currentProfile(); len(names) < 2 {
		return m.say("one profile, " + current + " — a [[profile]] in the config adds another"), nil
	}
	return m.offerProfiles(""), nil
}

// offerProfiles opens the profile picker, saying note.
func (m Model) offerProfiles(note string) Model {
	names, current := m.profileNames(), m.currentProfile()
	items := make([]pickerItem, 0, len(names))
	for _, name := range names {
		item := pickerItem{label: name, value: name, match: name}
		if strings.EqualFold(name, current) {
			item.detail = "current"
		}
		items = append(items, item)
	}
	m.picker = newPickerWith(pickerProfile, pickerSpecs[pickerProfile], items)
	if note != "" {
		m = m.say(note)
	}
	return m
}

// switchProfile runs this window in the named profile (`:profile <name>`, or picked),
// reading its rooms afresh once the daemon's streams run in it.
func (m Model) switchProfile(name string) (Model, tea.Cmd) {
	m = m.closePicker()
	names := m.profileNames()
	i := slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, strings.TrimSpace(name)) })
	switch {
	case i < 0:
		return m.say("no profile " + name + " — the config has " + strings.Join(names, ", ")), nil
	case strings.EqualFold(names[i], m.currentProfile()):
		return m.say("already in " + names[i]).rememberProfile(names[i])
	case m.profiles.switcher == nil:
		return m.say("this window cannot switch profiles"), nil
	case m.profiles.to != "":
		// One at a time: two under way could land in either order, the window
		// believing one profile while the daemon runs it in the other.
		return m.say("still switching to " + m.profiles.to), nil
	}
	m.profiles.to = names[i]
	m = m.doing("switching to " + names[i] + "…")
	// The writes that failed go once more, while the profile still holds their rooms.
	m, retries := m.saveRetries()
	m, send := m.switchWhenDrafted()
	return m, batched(append(retries, send))
}

// switchWhenDrafted sends the switch under way once no draft is being written: kithd
// takes a room's draft only in a profile holding the room, so what this window holds is
// written before it leaves the profile: the open room's composer here, the rooms left
// already on the wire (every Update writes them, armDraftSave). A write that failed is
// not asked again here (it is retried as any failed write is), or a daemon that is down
// would hold the switch.
func (m Model) switchWhenDrafted() (Model, tea.Cmd) {
	if m.profiles.to == "" || m.profiles.sent {
		return m, nil
	}
	// One on the wire is waited for: asking again would mark it owed, and a write kithd
	// refuses (a room the window still shows after landing) would go round for ever.
	room := m.openRoom
	if room != "" && !m.draftSync.inflight[room] && !m.draftSync.retry[room] &&
		m.draftSync.differs(room, m.composerDraft()) {
		var write tea.Cmd
		m, write = m.requestSave(room)
		return m, write
	}
	if len(m.draftSync.inflight) > 0 {
		return m, nil
	}
	to, switcher := m.profiles.to, m.profiles.switcher
	m.profiles.sent = true
	return m, func() tea.Msg {
		return profileSwitchedMsg{name: to, err: switcher.SwitchProfile(to)}
	}
}

// handleProfileSwitched reads the new profile's rooms; the open room stays open until
// they are read, and goes then if the profile does not hold it (profileListRead). A
// switch refused leaves the window where it was.
func (m Model) handleProfileSwitched(msg profileSwitchedMsg) (Model, tea.Cmd) {
	m.profiles.to, m.profiles.sent = "", false
	if msg.err != nil {
		return m.doing("").sayErr("cannot switch to "+msg.name, msg.err), nil
	}
	m.profiles.current = msg.name
	m.profiles.switches++
	m.profiles.arrival = profileArrival{due: true, room: m.openRoom}
	// Chats links opened were the profile left's: gone now, the open one with the list.
	m.rooms = m.rooms.keepingFresh(m.openRoom)
	m = m.runProfile().doing("").say("profile " + msg.name)
	m, remember := m.rememberProfile(msg.name)
	return m, tea.Batch(m.loadRoomsCmd(), m.loadSpacesCmd(), m.loadUnreadCmd(),
		m.loadInvitesCmd(), m.lastMessagesCmd(), m.loadRailCmd(), remember)
}

// profileListRead notes one of the new profile's lists read (rooms or invitations): was,
// named name, is the room open before it. Left then (keepCursorNearby, handleInvites)
// because no list read holds it, it is said to be out of the profile; the invitations
// from before the switch count only once the new profile's are read.
func (m Model) profileListRead(was domain.RoomID, name string, rooms, invites bool) Model {
	a := m.profiles.arrival
	if !a.due {
		return m
	}
	a.rooms, a.invites = a.rooms || rooms, a.invites || invites
	is := func(r domain.Room) bool { return r.ID == was }
	held := (a.rooms && slices.ContainsFunc(m.rooms.joined, is)) ||
		(a.invites && slices.ContainsFunc(m.rooms.invites, is))
	if was != "" && m.openRoom != was && !held {
		m = m.say("profile " + m.currentProfile() + " — " + name + " is not in it")
	}
	if a.rooms && a.invites {
		a = profileArrival{}
	}
	m.profiles.arrival = a
	return m
}

// rememberProfile keeps name as the profile this device runs in (device.toml's
// profile_in_use), so the next start runs in it.
func (m Model) rememberProfile(name string) (Model, tea.Cmd) {
	if m.conf.device.ProfileInUse == name {
		return m, nil
	}
	dev := m.conf.device.Clone()
	dev.ProfileInUse = name
	m.conf.device = dev
	return m, m.saveDeviceCmd(dev)
}

// profileBadge names the profile on the status line, once there is more than one.
func (m Model) profileBadge() string {
	if len(m.profileNames()) < 2 {
		return ""
	}
	return m.theme.Muted.Bold(true).Render(" " + drawSentence(m.currentProfile()) + " ")
}
