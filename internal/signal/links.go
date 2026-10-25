package signal

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Following a Signal link: for signal.me, Signal is asked for the account behind the
// number or username link, and the chat with it opens, made with the first message
// as any Signal chat is; for signal.group, Signal is asked what group the invite is
// to, and one the account is not in is joined (or asked to be) once agreed to.

// errNobodyThere is a number or username link Signal knows no account for.
var errNobodyThere = errors.New("signal: nobody on Signal has that number or link")

// errJoinRequested: the group's admins approve who joins, and the account asked to.
var errJoinRequested = errors.New("signal: asked to join; an admin of the group has to approve it")

// linkAccount is the account links are looked up on: the first connected, by name,
// so the answer does not change between calls.
func (a *Adapter) linkAccount() (string, *helper, error) {
	linked := a.linkedAccounts()
	names := make([]string, 0, len(linked))
	for name := range linked {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if h, err := a.sender(linked[name]); err == nil {
			return linked[name], h, nil
		}
	}
	return "", nil, fmt.Errorf("signal: no account to look a link up on: %w", api.ErrNetworkOff)
}

// ResolveLink looks up the person a Signal link names: the chat to write in.
func (a *Adapter) ResolveLink(ctx context.Context, uri string) (domain.LinkTarget, error) {
	link, ok := domain.ParseSignalLink(uri)
	if !ok {
		return domain.LinkTarget{}, fmt.Errorf("signal: not a Signal link: %s", uri)
	}
	req := &signalv1.FindPersonRequest{}
	switch link.Kind {
	case domain.SignalLinkPhone:
		req.By = &signalv1.FindPersonRequest_Number{Number: "+" + link.Phone}
	case domain.SignalLinkUsername:
		req.By = &signalv1.FindPersonRequest_UsernameLink{UsernameLink: link.Username}
	case domain.SignalLinkGroup:
		return a.inviteTarget(ctx, link.Invite)
	case domain.SignalLinkNone:
		return domain.LinkTarget{}, fmt.Errorf("signal: not a Signal link: %s", uri)
	}
	self, h, err := a.linkAccount()
	if err != nil {
		return domain.LinkTarget{}, err
	}
	req.Account = self
	res, err := h.client.FindPerson(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.LinkTarget{}, fmt.Errorf("signal: look the link up: %w", err)
	}
	aci := res.Msg.GetAci()
	if aci == "" {
		return domain.LinkTarget{}, errNobodyThere
	}
	return a.personTarget(ctx, self, aci, link.Phone, res.Msg.GetUsername())
}

// personTarget is the chat with aci, named as the book knows them, else by the number
// or username the link found them by, which the book keeps for when the chat is made.
func (a *Adapter) personTarget(ctx context.Context, self, aci, digits, username string) (domain.LinkTarget, error) {
	b, err := a.bookOf(ctx, self)
	if err != nil {
		return domain.LinkTarget{}, err
	}
	who := person(aci)
	a.booksMu.Lock()
	defer a.booksMu.Unlock()
	if digits != "" && b.numbers[who] == "" {
		b.numbers[who] = digits
	}
	if username != "" && b.people[who] == "" {
		b.people[who] = username
	}
	if room, known := b.rooms[roomID(self, aci, false)]; known {
		return domain.LinkTarget{Room: room}, nil
	}
	name := b.nameOf(who)
	if aci == self {
		name = noteToSelf
	}
	return domain.LinkTarget{Room: domain.Room{
		ID: roomID(self, aci, false), Name: name, IsDirect: true,
		Membership: domain.MembershipJoin, Network: domain.ProtocolSignal,
	}}, nil
}

// inviteTarget is the group an invite link is to: one the account is in, or one to join.
func (a *Adapter) inviteTarget(ctx context.Context, invite []byte) (domain.LinkTarget, error) {
	self, h, err := a.linkAccount()
	if err != nil {
		return domain.LinkTarget{}, err
	}
	res, err := h.client.GroupByInvite(ctx, connect.NewRequest(&signalv1.GroupByInviteRequest{Account: self, Invite: invite}))
	if err != nil {
		return domain.LinkTarget{}, fmt.Errorf("signal: look the invite up: %w", err)
	}
	g := res.Msg
	if g.GetRequested() {
		return domain.LinkTarget{}, errJoinRequested
	}
	room := domain.Room{
		ID: roomID(self, g.GetId(), true), Name: g.GetTitle(),
		Membership: domain.MembershipJoin, Network: domain.ProtocolSignal,
	}
	if g.GetMember() {
		b, bookErr := a.bookOf(ctx, self)
		if bookErr != nil {
			return domain.LinkTarget{}, bookErr
		}
		a.booksMu.Lock()
		if known, ok := b.rooms[room.ID]; ok {
			room = known
		}
		a.booksMu.Unlock()
		return domain.LinkTarget{Room: room}, nil
	}
	about := "group"
	if n := g.GetMembers(); n > 0 {
		about = fmt.Sprintf("group, %d members", n)
	}
	if g.GetApproval() {
		about += "; an admin approves who joins"
	}
	return domain.LinkTarget{Room: room, Join: true, About: about}, nil
}

// JoinLink joins the group a Signal invite link names, and returns its room.
func (a *Adapter) JoinLink(ctx context.Context, uri string) (domain.RoomID, error) {
	link, ok := domain.ParseSignalLink(uri)
	if !ok || link.Kind != domain.SignalLinkGroup {
		return "", fmt.Errorf("signal: not a group invite: %s", uri)
	}
	self, h, err := a.linkAccount()
	if err != nil {
		return "", err
	}
	res, err := h.client.JoinGroup(ctx, connect.NewRequest(&signalv1.JoinGroupRequest{Account: self, Invite: link.Invite}))
	if err != nil {
		return "", fmt.Errorf("signal: join by the invite: %w", err)
	}
	if res.Msg.GetRequested() {
		return "", errJoinRequested
	}
	if res.Msg.GetGroup().GetId() == "" {
		return "", errors.New("signal: Signal named no group joined")
	}
	return a.keepGroupMade(ctx, self, res.Msg.GetGroup())
}
