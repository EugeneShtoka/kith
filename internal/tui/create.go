package tui

import (
	"log/slog"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Making a room or a space, or a chat on another network: pick one whole answer, then
// a name, and for another network's chat who is in it. Only what an account this kith
// has can make is offered: Matrix's rooms and space when there is a Matrix account,
// and each Telegram, WhatsApp and Slack account's own kinds (newChatKinds). Encryption
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

// newChatKinds are what each network other than Matrix makes, in the order offered.
var newChatKinds = map[domain.Protocol][]struct {
	kind   domain.ChatKind
	label  string
	detail string
}{
	domain.ProtocolTelegram: {
		{domain.ChatGroup, "Telegram group", "a group chat"},
		{domain.ChatForum, "Telegram forum", "a group with topics"},
		{domain.ChatChannel, "Telegram channel", "only admins post"},
	},
	domain.ProtocolWhatsApp: {
		{domain.ChatGroup, "WhatsApp group", "a group chat, at most 25 characters of name"},
		{domain.ChatCommunity, "WhatsApp community", "groups under one community, with its announcements"},
	},
	domain.ProtocolSlack: {
		{domain.ChatChannel, "Slack channel", "anyone in the workspace can join"},
		{domain.ChatPrivateChannel, "Slack private channel", "invitation only"},
	},
}

// chatValueSep joins an account and a kind in a new-chat row's value; no Matrix kind
// has it.
const chatValueSep = "|"

// openNewRoom asks what to make, of what this kith's accounts can make.
func (m Model) openNewRoom() (Model, tea.Cmd) {
	var items []pickerItem
	if m.hasMatrix() {
		for i := range newRoomKinds {
			kind := &newRoomKinds[i]
			items = append(items, pickerItem{
				label:  kind.label,
				detail: kind.detail,
				value:  kind.value,
				match:  kind.label + " " + kind.detail,
			})
		}
	}
	accounts := m.chatAccounts()
	for _, network := range []domain.Protocol{domain.ProtocolTelegram, domain.ProtocolWhatsApp, domain.ProtocolSlack} {
		for _, account := range accounts[network] {
			for _, kind := range newChatKinds[network] {
				detail := kind.detail
				if len(accounts[network]) > 1 {
					detail = m.accountLabel(network, account) + " · " + detail
				}
				items = append(items, pickerItem{
					label:  kind.label,
					detail: detail,
					value:  string(domain.AccountRooms(network, account)) + chatValueSep + strconv.Itoa(int(kind.kind)),
					match:  kind.label + " " + detail,
				})
			}
		}
	}
	if len(items) == 0 {
		return m.say("no account here makes rooms or chats"), nil
	}
	m.picker = newPicker(pickerNewRoom, items)
	return m, nil
}

// hasMatrix reports whether this kith has a Matrix account: one of the IDs that are
// this person is a Matrix user's, or a Matrix room is listed.
func (m Model) hasMatrix() bool {
	if slices.ContainsFunc(m.selves, domain.IsMatrixUserID) {
		return true
	}
	return slices.ContainsFunc(m.rooms.all, func(r domain.Room) bool { return domain.IsMatrixRoomID(string(r.ID)) })
}

// chatAccounts is each network's accounts the room list has rooms from, in order.
func (m Model) chatAccounts() map[domain.Protocol][]string {
	out := map[domain.Protocol][]string{}
	for i := range m.rooms.all {
		id := domain.ParseID(string(m.rooms.all[i].ID))
		if _, makes := newChatKinds[id.Network]; makes && id.Account != "" && !slices.Contains(out[id.Network], id.Account) {
			out[id.Network] = append(out[id.Network], id.Account)
		}
	}
	for _, accounts := range out {
		slices.Sort(accounts)
	}
	return out
}

// accountLabel tells one of a network's accounts from its others: a WhatsApp number,
// a Slack workspace's name, a Telegram account's ID.
func (m Model) accountLabel(network domain.Protocol, account string) string {
	if network == domain.ProtocolWhatsApp {
		return "+" + account
	}
	if network == domain.ProtocolSlack {
		for i := range m.rooms.spaces {
			if id := domain.ParseID(string(m.rooms.spaces[i].ID)); id.Network == network && id.Account == account && id.Native == account {
				return m.rooms.spaces[i].DisplayName()
			}
		}
	}
	return account
}

// chooseNewRoomKind settles the kind and asks for the name. A room lands in the
// space selected in the rail; synthetic groups file it nowhere.
func (m Model) chooseNewRoomKind(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	if on, kind, isChat := strings.Cut(value, chatValueSep); isChat {
		m.aimedAt.creating = domain.NewRoom{On: domain.RoomOwner(on), Kind: domain.ChatKind(atoiSafe(kind))}
		return m.openPrompt(promptNewRoom), nil
	}
	for i := range newRoomKinds {
		kind := &newRoomKinds[i]
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
	spec := m.aimedAt.creating
	m.aimedAt.creating = domain.NewRoom{}
	spec.Name = strings.TrimSpace(input)
	if spec.Name == "" {
		// Nothing typed is a cancel.
		return m, nil
	}
	if spec.On != "" {
		return m.openNewChatMembers(spec), nil
	}
	what := "room"
	if spec.Space {
		what = "space"
	}
	m = m.doing("creating " + what + " " + isolate(spec.Name) + "…")
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

// createNewChat makes the chat with the people ticked; a WhatsApp group needs one.
func (m Model) createNewChat(people []string) (Model, tea.Cmd) {
	spec := m.aimedAt.creating
	if domain.NetworkOf(string(spec.On)) == domain.ProtocolWhatsApp && spec.Kind == domain.ChatGroup && len(people) == 0 {
		return m.say("a WhatsApp group needs someone in it besides you: tick at least one"), nil
	}
	m = m.closePicker()
	m.aimedAt.creating = domain.NewRoom{}
	spec.Invite = people
	m = m.doing("creating " + isolate(spec.Name) + "…")
	return m, m.createRoomAsCmd(spec, spec.Name, true)
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
