package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/setup"
)

// Profiles switches the [[profile]] this window runs in. Declared here because only
// daemon.Remote implements it.
type Profiles interface {
	// SwitchProfile runs every call in the named profile, returning once every stream
	// runs in it.
	SwitchProfile(name string)
}

// profileState is the [[profile]] this window runs in.
type profileState struct {
	switcher Profiles // nil: this window cannot switch
	current  string   // as the config spells it; "" is the first
}

// profileSwitchedMsg says the window now runs in name.
type profileSwitchedMsg struct{ name string }

// WithProfiles sets the switcher and the profile this window starts in ("" the first).
func (m Model) WithProfiles(p Profiles, current string) Model {
	m.profiles = profileState{switcher: p, current: current}
	return m
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
		switcher.SwitchProfile(to)
		return profileSwitchedMsg{name: to}
	}
}

// handleProfileSwitched reads the new profile's rooms; the open room stays open.
func (m Model) handleProfileSwitched(msg profileSwitchedMsg) (Model, tea.Cmd) {
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
