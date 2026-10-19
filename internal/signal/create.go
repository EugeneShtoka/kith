package signal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A group is made with the people asked for: Signal people by their account, anyone
// else by number (Signal is asked for the account behind it). The account is its admin.

// titleMost is Signal's longest group name, in characters.
const titleMost = 32

var errNoSuchSignalAccount = errors.New("signal: no linked account makes chats there")

// CreateRoom makes a group on the account spec.On names, and keeps its room.
func (a *Adapter) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	if spec.Kind != domain.ChatGroup {
		return "", fmt.Errorf("signal: makes groups, not kind %d", spec.Kind)
	}
	title := strings.TrimSpace(spec.Name)
	if n := utf8.RuneCountInString(title); n == 0 || n > titleMost {
		return "", fmt.Errorf("signal: a group's name is 1 to %d characters; %q has %d", titleMost, title, n)
	}
	self := ""
	for _, aci := range a.linkedAccounts() {
		if owner(aci) == spec.On {
			self = aci
		}
	}
	if self == "" {
		return "", fmt.Errorf("%w: %s", errNoSuchSignalAccount, spec.On)
	}
	h, err := a.sender(self)
	if err != nil {
		return "", err
	}
	req := &signalv1.CreateGroupRequest{Account: self, Title: title}
	var unreachable []string
	for _, person := range spec.Invite {
		switch aci, number := invitee(person); {
		case aci != "":
			req.Members = append(req.Members, aci)
		case number != "":
			req.Numbers = append(req.Numbers, number)
		default:
			unreachable = append(unreachable, person)
		}
	}
	res, err := h.client.CreateGroup(ctx, connect.NewRequest(req))
	if err != nil {
		return "", fmt.Errorf("signal: create %q: %w", title, err)
	}
	room, err := a.keepGroupMade(ctx, self, res.Msg.GetGroup())
	if err != nil {
		return "", err
	}
	unreachable = append(unreachable, res.Msg.GetUnknown()...)
	if len(unreachable) > 0 {
		return room, fmt.Errorf("%s could not be added: not on Signal", strings.Join(unreachable, ", "))
	}
	return room, nil
}

// invitee is a person asked into a group: their Signal account, else their number
// (E.164), else neither.
func invitee(person string) (aci, number string) {
	id := domain.ParseID(person)
	if id.Network == domain.ProtocolSignal && id.Account == "" && isACI(id.Native) {
		return id.Native, ""
	}
	if digits := domain.PhoneOf(person); digits != "" {
		return "", "+" + digits
	}
	if digits, ok := strings.CutPrefix(person, "tel:"); ok && digits != "" {
		return "", "+" + strings.TrimPrefix(digits, "+")
	}
	return "", ""
}

// keepGroupMade keeps the room of a group the account just made: Signal tells its
// members, not the device that made it.
func (a *Adapter) keepGroupMade(ctx context.Context, self string, g *signalv1.Group) (domain.RoomID, error) {
	b, err := a.bookOf(ctx, self)
	if err != nil {
		return "", err
	}
	a.booksMu.Lock()
	b.groups[g.GetId()] = g
	room, _ := b.chat(self, g.GetId(), true)
	members := b.members(self, g.GetId(), true)
	a.booksMu.Unlock()
	if err := a.keepChat(ctx, self, room, members); err != nil {
		return "", err
	}
	return room.ID, nil
}

// ChatOffers is a group on each connected account.
func (a *Adapter) ChatOffers(context.Context) ([]domain.ChatOffer, error) {
	var out []domain.ChatOffer
	for name, aci := range a.linkedAccounts() {
		if _, err := a.sender(aci); err != nil {
			continue
		}
		out = append(out, domain.ChatOffer{
			On: owner(aci), Account: name, Kind: domain.ChatGroup, Label: "Signal group",
			Detail: fmt.Sprintf("a group chat, at most %d characters of name", titleMost),
		})
	}
	return out, nil
}
