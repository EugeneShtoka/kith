//go:build signal

package meow

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
)

// CreateGroup makes a group from a connected account, as Signal's apps do: the account
// its admin, members added by members, no invite link, its name and picture changed by
// admins. A number is asked of Signal for the account behind it; one with none is left
// out and named back.
func (n *Network) CreateGroup(ctx context.Context, req *signalv1.CreateGroupRequest) (*signalv1.CreateGroupResponse, error) {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return nil, err
	}
	members := []*signalmeow.GroupMember{{ACI: a.cli.Store.ACI, Role: signalmeow.GroupMember_ADMINISTRATOR}}
	for _, m := range req.GetMembers() {
		aci, perr := uuid.Parse(m)
		if perr != nil {
			return nil, fmt.Errorf("signal: %q is no ACI: %w", m, perr)
		}
		members = append(members, &signalmeow.GroupMember{ACI: aci, Role: signalmeow.GroupMember_DEFAULT})
	}
	found, unknown, err := a.lookUp(ctx, req.GetNumbers())
	if err != nil {
		return nil, err
	}
	for _, aci := range found {
		members = append(members, &signalmeow.GroupMember{ACI: aci, Role: signalmeow.GroupMember_DEFAULT})
	}
	group := &signalmeow.Group{
		Title:   req.GetTitle(),
		Members: members,
		AccessControl: &signalmeow.GroupAccessControl{
			Members:           signalmeow.AccessControl_MEMBER,
			AddFromInviteLink: signalmeow.AccessControl_UNSATISFIABLE,
			Attributes:        signalmeow.AccessControl_ADMINISTRATOR,
		},
	}
	if _, err = signalmeow.PrepareGroupCreation(group); err != nil {
		return nil, fmt.Errorf("signal: prepare the group: %w", err)
	}
	made, err := a.cli.CreateGroup(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("signal: create the group: %w", err)
	}
	out := &signalv1.Group{Id: string(made.GroupIdentifier), Title: made.Title, Timer: made.DisappearingMessagesDuration}
	for _, m := range made.Members {
		out.Members = append(out.Members, m.ACI.String())
	}
	return &signalv1.CreateGroupResponse{Group: out, Unknown: unknown}, nil
}

// lookUp asks Signal for the account behind each number (E.164); the numbers it knows
// none for come back as unknown.
func (a *account) lookUp(ctx context.Context, numbers []string) (found []uuid.UUID, unknown []string, err error) {
	if len(numbers) == 0 {
		return nil, nil, nil
	}
	e164s := make([]uint64, 0, len(numbers))
	for _, number := range numbers {
		e164, perr := strconv.ParseUint(strings.TrimPrefix(number, "+"), 10, 64)
		if perr != nil {
			return nil, nil, fmt.Errorf("signal: %q is no phone number: %w", number, perr)
		}
		e164s = append(e164s, e164)
	}
	res, err := a.cli.LookupPhone(ctx, e164s...)
	if err != nil {
		return nil, nil, fmt.Errorf("signal: look the numbers up: %w", err)
	}
	for i, number := range numbers {
		if entry, ok := res[e164s[i]]; ok && entry.ACI != uuid.Nil {
			found = append(found, entry.ACI)
		} else {
			unknown = append(unknown, number)
		}
	}
	return found, unknown, nil
}
