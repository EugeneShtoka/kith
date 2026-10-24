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

// Following a Signal link (signal.me): Signal is asked for the account behind the
// number or username link, and the chat with it opens, made with the first message
// as any Signal chat is.

// errNobodyThere is a number or username link Signal knows no account for.
var errNobodyThere = errors.New("signal: nobody on Signal has that number or link")

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
	case domain.SignalLinkGroup, domain.SignalLinkNone:
		return domain.LinkTarget{}, errors.New("signal: group invite links are not followed yet")
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

// JoinLink joins the group a Signal invite link names.
func (a *Adapter) JoinLink(_ context.Context, uri string) (domain.RoomID, error) {
	if _, ok := domain.ParseSignalLink(uri); !ok {
		return "", fmt.Errorf("signal: not a Signal link: %s", uri)
	}
	return "", errors.New("signal: group invite links are not followed yet")
}
