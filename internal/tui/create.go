package tui

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Making a room, space, other-network chat, or tag: pick one whole answer, then a
// name, and for another network's chat its members. Only what this kith's accounts
// can make is offered: Matrix rooms and spaces with a Matrix account, the kinds each
// other network's connected accounts offer (asked of the daemon), and a tag always.
// Encryption can only be chosen at creation, so it is asked here.

// chatValueSep joins account and kind in a new-chat row's value.
const chatValueSep = "|"

// chatOffersMsg is what chats the accounts can make, for :new, and the accounts with
// their defaults.
type chatOffersMsg struct {
	offers   []domain.ChatOffer
	accounts []domain.Account
	err      error
}

// openNewRoom asks the daemon what the accounts can make, then what to make.
func (m Model) openNewRoom() (Model, tea.Cmd) {
	ctx, backend := m.ctx, m.backend
	return m, func() tea.Msg {
		offers, err := backend.ChatOffers(ctx)
		accounts, aerr := backend.Accounts(ctx)
		return chatOffersMsg{offers: offers, accounts: accounts, err: errors.Join(err, aerr)}
	}
}

// handleCreating is news of :new: what can be made, or what was.
func (m Model) handleCreating(msg tea.Msg) (Model, tea.Cmd) {
	if offers, ok := msg.(chatOffersMsg); ok {
		return m.handleChatOffers(offers)
	}
	created, _ := msg.(roomCreatedMsg)
	return m.handleRoomCreated(created)
}

// handleChatOffers asks what to make, from what this kith's accounts can make; when
// the daemon cannot say, a tag is still offered. Where a network's default is among
// its accounts, only the default's offers are shown: it makes chats without asking.
func (m Model) handleChatOffers(msg chatOffersMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.logErr(slog.LevelWarn, "list the chats accounts can make", msg.err)
	}
	msg.offers = defaultsOnly(msg.offers, msg.accounts)
	var items []pickerItem
	accounts := map[domain.Protocol]map[domain.RoomOwner]bool{}
	for _, o := range msg.offers {
		network := domain.NetworkOf(string(o.On))
		if accounts[network] == nil {
			accounts[network] = map[domain.RoomOwner]bool{}
		}
		accounts[network][o.On] = true
	}
	for _, o := range msg.offers {
		detail := o.Detail
		if len(accounts[domain.NetworkOf(string(o.On))]) > 1 {
			detail = o.Account + " · " + detail
		}
		items = append(items, pickerItem{
			label:  o.Label,
			detail: detail,
			value:  string(o.On) + chatValueSep + strconv.Itoa(int(o.Kind)),
			match:  o.Label + " " + detail,
		})
	}
	// A tag needs no account: it is kith's own, over every network.
	items = append(items, pickerItem{
		label: "Tag", detail: "kith's own group, by a rule or by hand, over every network",
		value: tagNew, match: "tag kith's own group",
	})
	m.aimedAt.offers = msg.offers
	m.picker = newPicker(pickerNewRoom, items)
	return m, nil
}

// defaultsOnly drops the offers of a network's other accounts where its default
// offers something.
func defaultsOnly(offers []domain.ChatOffer, accounts []domain.Account) []domain.ChatOffer {
	byDefault := map[domain.Protocol]domain.RoomOwner{}
	for _, a := range accounts {
		if a.Default && slices.ContainsFunc(offers, func(o domain.ChatOffer) bool { return o.On == a.On }) {
			byDefault[a.Network] = a.On
		}
	}
	return slices.DeleteFunc(slices.Clone(offers), func(o domain.ChatOffer) bool {
		on, ok := byDefault[domain.NetworkOf(string(o.On))]
		return ok && o.On != on
	})
}

// chooseNewRoomKind settles the kind and asks for the name. A room lands in the space
// selected in the rail; synthetic groups file it nowhere.
func (m Model) chooseNewRoomKind(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	if value == tagNew {
		m.choosing.tag = tagEditing{}
		return m.openPrompt(promptTagName), nil
	}
	on, kind, isChat := strings.Cut(value, chatValueSep)
	if !isChat {
		return m, nil
	}
	spec := domain.NewRoom{On: domain.RoomOwner(on), Kind: domain.ChatKind(atoiSafe(kind))}
	// A Matrix room lands in the space selected, when that account holds it; nesting a
	// space is left to the S key, not assumed.
	if parent := m.selectedSpaceID(); spec.Kind != domain.ChatSpace && parent != "" &&
		domain.OwnerOf(domain.RoomID(parent)) == spec.On {
		spec.Parent = parent
	}
	m.aimedAt.creating = spec
	return m.openPrompt(promptNewRoom), nil
}

func (m Model) submitNewRoom(input string) (Model, tea.Cmd) {
	spec, fileInto := m.aimedAt.creating, m.aimedAt.fileInto
	m.aimedAt.creating, m.aimedAt.fileInto = domain.NewRoom{}, ""
	spec.Name = strings.TrimSpace(input)
	if spec.Name == "" {
		// Nothing typed is a cancel.
		return m, nil
	}
	if domain.NetworkOf(string(spec.On)) != domain.ProtocolMatrix {
		return m.openNewChatMembers(spec), nil
	}
	what := "room"
	if spec.Kind == domain.ChatSpace {
		what = "space"
	}
	m = m.doing("creating " + what + " " + isolate(spec.Name) + "…")
	if fileInto != "" {
		return m, m.createSpaceForCmd(spec, fileInto)
	}
	return m, m.createRoomCmd(spec)
}

// openNewChatMembers asks who is in a new chat: the people its account knows, by name.
func (m Model) openNewChatMembers(spec domain.NewRoom) Model {
	source := strings.TrimSuffix(string(spec.On), "/")
	seen := map[string]bool{}
	var items []pickerItem
	for _, p := range m.dir.dir.Names() {
		if p.Source != source || seen[p.ID] || m.isMe(p.ID) {
			continue
		}
		seen[p.ID] = true
		items = append(items, pickerItem{label: isolate(p.Name), detail: p.ID, value: p.ID, match: p.Name + " " + p.ID})
	}
	slices.SortFunc(items, func(a, b pickerItem) int { return strings.Compare(strings.ToLower(a.match), strings.ToLower(b.match)) })
	m.aimedAt.creating = spec
	spec2 := pickerSpecs[pickerNewMembers]
	spec2.title = "Who is in " + spec.Name + "? — tick them, then enter"
	m.picker = newPickerWith(pickerNewMembers, spec2, items)
	m.picker.checked = map[string]bool{}
	return m
}

// createNewChat makes the chat with the people ticked, as many as its kind needs.
func (m Model) createNewChat(people []string) (Model, tea.Cmd) {
	spec := m.aimedAt.creating
	for _, o := range m.aimedAt.offers {
		if o.On == spec.On && o.Kind == spec.Kind && len(people) < o.MinMembers {
			if o.MinMembers == 1 {
				return m.say("a " + o.Label + " needs someone in it besides you: tick at least one"), nil
			}
			return m.say(fmt.Sprintf("a %s needs at least %d people in it besides you: tick them", o.Label, o.MinMembers)), nil
		}
	}
	m = m.closePicker()
	m.aimedAt.creating = domain.NewRoom{}
	spec.Invite = people
	m = m.doing("creating " + isolate(spec.Name) + "…")
	return m, m.createRoomAsCmd(spec, spec.Name, true)
}

// createSpaceForCmd makes a space and files room into it, reported as one creation: a
// failure to file shows as a later step's failure.
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

// handleRoomCreated reports the outcome and refreshes rooms and spaces (filing into a
// space changes the hierarchy).
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

// selectedSpaceID is the Matrix space under the rail cursor, "" on a synthetic group.
// Space groups are keyed by name, so a name shared by two spaces resolves to neither.
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

// enterPending opens a just-created room once a refresh lists it; until then the
// request stands.
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
