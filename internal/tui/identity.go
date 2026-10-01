package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/theme"
)

// Setting someone's alias from the app, since identities are keyed on MXIDs the client
// rarely shows: pick a person → pick or create the identity → pick a color. Nothing is
// written until the last step.

// pendingIdentity is the identity edit in progress, carried between the pickers. It
// holds the MXID, not a config pointer, so a reload cannot redirect the edit.
type pendingIdentity struct {
	mxid string
	// name is the display name, for the prompts.
	name string
	// alias is empty while still being chosen.
	alias string
	// creating: a brand-new identity whose alias is typed.
	creating bool
}

// newIdentityValue is the "start a new person" sentinel; it collides with no MXID or alias.
const newIdentityValue = "\x00new"

// openIdentityForSender starts the flow for whoever sent the selected message.
func (m Model) openIdentityForSender() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	return m.openIdentityFor(msg.Sender, m.processedName(msg))
}

// openIdentityFor starts the flow for one MXID.
func (m Model) openIdentityFor(mxid, name string) (Model, tea.Cmd) {
	if mxid == "" {
		return m, nil
	}
	if mxid == m.me {
		// Naming yourself is allowed; just say who it is.
		m = m.say("that's you")
	}
	m.choosing.identity = pendingIdentity{mxid: mxid, name: name}
	m.picker = newPicker(pickerIdentity, m.identityItems(mxid))
	return m, nil
}

// identityItems lists the identities this person could join, plus "new person"; ones
// they are already in are marked, not hidden.
func (m Model) identityItems(mxid string) []pickerItem {
	items := []pickerItem{{
		label: "+ new person",
		value: newIdentityValue,
		match: "new",
	}}
	for _, ident := range m.prefs.display.Identities {
		label := ident.Alias
		if label == "" {
			label = "(unnamed)"
		}
		detail := fmt.Sprintf("%d account(s)", len(ident.IDs))
		if containsKey(ident.IDs, mxid) {
			detail = "already here · " + detail
		}
		items = append(items, pickerItem{
			label:  isolate(label),
			detail: detail,
			value:  ident.Alias,
			// Filter on the logical name, not the isolated display label.
			match: ident.Alias,
		})
	}
	return items
}

// chooseIdentity starts naming a new person, or adds the MXID to the chosen identity
// and moves on to the color.
func (m Model) chooseIdentity(alias string) (Model, tea.Cmd) {
	if alias == newIdentityValue {
		m.choosing.identity.creating = true
		m = m.closePicker()
		m = m.openPromptWith(promptAlias, m.choosing.identity.name)
		return m, nil
	}
	m.choosing.identity.alias = alias
	m.picker = newPicker(pickerColor, m.colorItems())
	return m, nil
}

// submitAlias takes the typed name for a new person and moves on to the color.
func (m Model) submitAlias(alias string) (Model, tea.Cmd) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		m = m.say("no name given — nothing changed")
		m.choosing.identity = pendingIdentity{}
		return m, nil
	}
	m.choosing.identity.alias = alias
	m.picker = newPicker(pickerColor, m.colorItems())
	return m, nil
}

// colorItems offers the identity palette as swatches, plus "no color".
func (m Model) colorItems() []pickerItem {
	items := []pickerItem{{
		label:  "— no color —",
		detail: "auto-assigned, avoiding the pinned hues",
		value:  "",
		match:  "none auto default",
	}}
	for _, hex := range identityColors {
		swatch := lipglossSwatch(m.theme, hex)
		items = append(items, pickerItem{
			label:  swatch + "  " + hex,
			detail: colorNames[hex],
			value:  hex,
			match:  hex + " " + colorNames[hex],
		})
	}
	return items
}

// chooseColor is the last step: it writes the config and applies it in place.
func (m Model) chooseColor(hex string) (Model, tea.Cmd) {
	pending := m.choosing.identity
	m.choosing.identity = pendingIdentity{}
	m = m.closePicker()
	if pending.mxid == "" || pending.alias == "" {
		return m, nil
	}
	display := mergeIdentity(m.prefs.display, pending.alias, pending.mxid, hex)
	return m.applyDisplay(display, fmt.Sprintf("%s is now %s", isolate(pending.name), isolate(pending.alias)))
}

// mergeIdentity returns display with mxid added to the identity named alias, creating
// it when there is none, and removed from every other identity (an MXID belongs to one
// person). Identities left empty are dropped.
func mergeIdentity(display config.Display, alias, mxid, color string) config.Display {
	identities := make([]config.Identity, 0, len(display.Identities)+1)
	found := false
	for _, ident := range display.Identities {
		ident.IDs = without(ident.IDs, mxid)
		if ident.Alias == alias {
			found = true
			ident.IDs = append(ident.IDs, mxid)
			ident.Color = color
		}
		if ident.Alias == alias || len(ident.IDs) > 0 {
			identities = append(identities, ident)
		}
	}
	if !found {
		identities = append(identities, config.Identity{Alias: alias, Color: color, IDs: []string{mxid}})
	}
	display.Identities = identities
	return display
}

// lipglossSwatch renders a solid block in the given color.
func lipglossSwatch(t theme.Theme, hex string) string {
	c, ok := theme.ParseColor(hex)
	if !ok {
		return "  "
	}
	return t.Swatch(c).Render("███")
}

// identityColors is the deliberately short list offered for a person's color.
var identityColors = []string{
	"#8ff586", "#7aa2f7", "#f7768e", "#e0af68", "#bb9af7", "#7dcfff",
	"#9ece6a", "#ff9e64", "#f7c8e0", "#73daca", "#c0caf5", "#ff757f",
}

// colorNames lets the list be filtered by typing "green".
var colorNames = map[string]string{
	"#8ff586": "green", "#7aa2f7": "blue", "#f7768e": "red", "#e0af68": "yellow",
	"#bb9af7": "purple", "#7dcfff": "cyan", "#9ece6a": "leaf green", "#ff9e64": "orange",
	"#f7c8e0": "pink", "#73daca": "teal", "#c0caf5": "lavender", "#ff757f": "salmon",
}

// openPeopleForRoom lists the room's members to act on.
func (m Model) openPeopleForRoom() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	if len(m.timeline.members) == 0 {
		m = m.say("no members loaded for this room yet")
		return m, nil
	}
	m.picker = newPicker(pickerPeople, m.peopleItems())
	return m, nil
}

// peopleItems lists the members by local name, with the MXID as detail.
func (m Model) peopleItems() []pickerItem {
	items := make([]pickerItem, 0, len(m.timeline.members))
	for _, member := range m.timeline.members {
		label := isolate(member.Name())
		if ident, ok := m.prefs.identities[member.UserID]; ok && ident.alias != "" {
			label = isolate(ident.alias) + "  (" + isolate(member.Name()) + ")"
		}
		items = append(items, pickerItem{
			label:  label,
			detail: member.UserID,
			value:  member.UserID,
			match:  member.Name() + " " + domain.ShortName(member.UserID),
		})
	}
	return items
}

// aliasSelectedPerson moves from choosing a person in the people picker to naming them.
func (m Model) aliasSelectedPerson() (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok {
		return m, nil
	}
	name := stripIsolates(item.label)
	if idx := strings.Index(name, "  ("); idx > 0 {
		name = name[:idx] // strip the "(real name)" qualifier
	}
	return m.openIdentityFor(item.value, name)
}
