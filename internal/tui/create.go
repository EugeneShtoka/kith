package tui

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Making a room or a space: pick one of four whole answers, then a name. Encryption
// can only be chosen at creation, so it is asked here.

// newRoomKinds are the first step's rows, safe default first.
var newRoomKinds = []struct {
	value  string
	label  string
	detail string
	spec   domain.NewRoom
}{
	{
		value: "private", label: "Private room",
		detail: "encrypted, invitation only",
		spec:   domain.NewRoom{Encrypted: true},
	},
	{
		value: "plain", label: "Private room, unencrypted",
		detail: "invitation only — for bridges and bots that cannot read encrypted rooms",
		spec:   domain.NewRoom{},
	},
	{
		value: "public", label: "Public room",
		detail: "anyone who finds it can join; not encrypted",
		spec:   domain.NewRoom{Public: true},
	},
	{
		value: "space", label: "Space",
		detail: "a container for rooms, not a conversation",
		spec:   domain.NewRoom{Space: true},
	},
}

// openNewRoom asks what to make.
func (m Model) openNewRoom() (Model, tea.Cmd) {
	items := make([]pickerItem, 0, len(newRoomKinds))
	for _, kind := range newRoomKinds {
		items = append(items, pickerItem{
			label:  kind.label,
			detail: kind.detail,
			value:  kind.value,
			match:  kind.label + " " + kind.detail,
		})
	}
	m.picker = newPicker(pickerNewRoom, items)
	return m, nil
}

// chooseNewRoomKind settles the kind and asks for the name. A room lands in the
// space selected in the rail; synthetic groups file it nowhere.
func (m Model) chooseNewRoomKind(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	for _, kind := range newRoomKinds {
		if kind.value != value {
			continue
		}
		m.aimedAt.creating = kind.spec
		if !kind.spec.Space {
			// Nesting a space is left to the S key rather than assumed.
			m.aimedAt.creating.Parent = m.selectedSpaceID()
		}
		return m.openPrompt(promptNewRoom), nil
	}
	return m, nil
}

// submitNewRoom creates it.
func (m Model) submitNewRoom(input string) (Model, tea.Cmd) {
	spec, fileInto := m.aimedAt.creating, m.aimedAt.fileInto
	m.aimedAt.creating, m.aimedAt.fileInto = domain.NewRoom{}, ""
	spec.Name = strings.TrimSpace(input)
	if spec.Name == "" {
		// Nothing typed is a cancel.
		return m, nil
	}
	what := "room"
	if spec.Space {
		what = "space"
	}
	m = m.doing("creating " + what + " " + isolate(spec.Name) + "…")
	if fileInto != "" {
		return m, m.createSpaceForCmd(spec, fileInto)
	}
	return m, m.createRoomCmd(spec)
}

// createSpaceForCmd makes a space and then files room into it, reporting both as one
// creation: a space made but not given the room says so as a later step's failure.
func (m Model) createSpaceForCmd(spec domain.NewRoom, room domain.RoomID) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		spaceID, err := backend.CreateRoom(ctx, spec)
		if err != nil {
			return roomCreatedMsg{name: spec.Name, err: err}
		}
		if err := backend.AddToSpace(ctx, domain.SpaceID(spaceID), room); err != nil && !errors.Is(err, api.ErrNoSpaceParent) {
			return roomCreatedMsg{roomID: spaceID, name: spec.Name, err: fmt.Errorf("the room could not be filed into it: %w", err)}
		}
		return roomCreatedMsg{roomID: spaceID, name: spec.Name}
	}
}

// handleRoomCreated reports the outcome and refreshes both the room list and the
// spaces (a room filed into a space changes the hierarchy).
func (m Model) handleRoomCreated(msg roomCreatedMsg) (Model, tea.Cmd) {
	switch {
	case msg.err != nil && msg.roomID == "":
		m = m.sayErr("could not create "+isolate(msg.name), msg.err)
		return m, nil
	case msg.err != nil:
		// Created, but a later step failed.
		m.logErr(slog.LevelWarn, "create room: a later step failed", msg.err, "room", msg.roomID)
		m = m.say(msg.name + " was created, but " + msg.err.Error())
	default:
		m = m.say(isolate(msg.name) + " created")
	}
	if msg.enter && msg.roomID != "" {
		m.entering = msg.roomID
	}
	return m, tea.Batch(m.refreshRoomsCmd(), m.refreshSpacesCmd())
}

// selectedSpaceID is the Matrix space the rail cursor is on, or "" on a synthetic
// group. Space groups are keyed by name, so a name shared by two spaces resolves to
// neither rather than to a guess.
func (m Model) selectedSpaceID() domain.SpaceID {
	g, ok := m.currentGroup()
	if !ok {
		return ""
	}
	var found domain.SpaceID
	for i := range m.rooms.spaces {
		if m.rooms.spaces[i].DisplayName() != g.key {
			continue
		}
		if found != "" {
			return ""
		}
		found = m.rooms.spaces[i].ID
	}
	return found
}

// enterPending opens a room created a moment ago once a refresh lists it; until
// then the request stands.
func (m Model) enterPending() (Model, tea.Cmd, bool) {
	if m.entering == "" {
		return m, nil, false
	}
	room, listed := m.rooms.byID(m.entering)
	if !listed {
		return m, nil, false
	}
	m.entering = ""
	mdl, cmd := m.selectRoom(room)
	return mdl, cmd, true
}
