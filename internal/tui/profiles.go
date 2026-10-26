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
}

// profileSwitchedMsg says the window now runs in name, or why it does not.
type profileSwitchedMsg struct {
	name string
	err  error
}

// WithProfiles sets the switcher and the profile this window starts in ("" the first).
func (m Model) WithProfiles(p Profiles, current string) Model {
	m.profiles = profileState{switcher: p, current: current}
	return m
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
	names, current := m.profileNames(), m.currentProfile()
	if len(names) < 2 {
		return m.say("one profile, " + current + " — a [[profile]] in the config adds another"), nil
	}
	items := make([]pickerItem, 0, len(names))
	for _, name := range names {
		item := pickerItem{label: name, value: name, match: name}
		if strings.EqualFold(name, current) {
			item.detail = "current"
		}
		items = append(items, item)
	}
	m.picker = newPickerWith(pickerProfile, pickerSpecs[pickerProfile], items)
	return m, nil
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
		return m.say("already in " + names[i]), nil
	case m.profiles.switcher == nil:
		return m.say("this window cannot switch profiles"), nil
	}
	to, switcher := names[i], m.profiles.switcher
	return m.doing("switching to " + to + "…"), func() tea.Msg {
		return profileSwitchedMsg{name: to, err: switcher.SwitchProfile(to)}
	}
}

// handleProfileSwitched reads the new profile's rooms; the open room stays open. A
// switch refused leaves the window where it was.
func (m Model) handleProfileSwitched(msg profileSwitchedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.doing("").sayErr("cannot switch to "+msg.name, msg.err), nil
	}
	m.profiles.current = msg.name
	m = m.doing("").say("profile " + msg.name)
	return m, tea.Batch(m.loadRoomsCmd(), m.loadSpacesCmd(), m.loadUnreadCmd(),
		m.loadInvitesCmd(), m.lastMessagesCmd())
}

// profileBadge names the profile on the status line, once there is more than one.
func (m Model) profileBadge() string {
	if len(m.profileNames()) < 2 {
		return ""
	}
	return m.theme.Muted.Bold(true).Render(" " + drawSentence(m.currentProfile()) + " ")
}
