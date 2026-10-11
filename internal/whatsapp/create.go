package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A group or community is created with its members (by number); WhatsApp adds the
// account itself, and the server makes a community's announcement group.

// groupNameMost is WhatsApp's maximum group name length, in characters.
const groupNameMost = 25

// CreateRoom creates a group or community on the account spec.On names.
func (a *Adapter) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	name := strings.TrimSpace(spec.Name)
	if n := utf8.RuneCountInString(name); n > groupNameMost {
		return "", fmt.Errorf("whatsapp: a group name is at most %d characters; %q has %d", groupNameMost, name, n)
	}
	account := domain.ParseID(string(spec.On)).Account
	var client *whatsmeow.Client
	for _, c := range a.connected() {
		if c.account.Digits == account {
			client = c.client
		}
	}
	if client == nil {
		return "", fmt.Errorf("whatsapp: account %s is not connected", account)
	}
	req := whatsmeow.ReqCreateGroup{Name: name}
	switch spec.Kind {
	case domain.ChatGroup:
	case domain.ChatCommunity:
		req.IsParent = true
	default:
		return "", fmt.Errorf("whatsapp: makes groups and communities, not kind %d", spec.Kind)
	}
	var unknown []string
	for _, person := range spec.Invite {
		jid, ok := personJID(person)
		if !ok {
			unknown = append(unknown, person)
			continue
		}
		req.Participants = append(req.Participants, jid)
	}
	if len(req.Participants) == 0 && spec.Kind == domain.ChatGroup {
		return "", errors.New("whatsapp: a group needs someone in it besides you")
	}
	info, err := client.CreateGroup(ctx, req)
	if err != nil {
		return "", fmt.Errorf("whatsapp: create %q: %w", name, err)
	}
	room := roomID(account, info.JID)
	if len(unknown) > 0 {
		return room, fmt.Errorf("%s could not be added: not a WhatsApp number", strings.Join(unknown, ", "))
	}
	return room, nil
}

// personJID parses a directory phone number or a WhatsApp ID into a JID.
func personJID(person string) (types.JID, bool) {
	if digits := domain.PhoneOf(person); digits != "" {
		return types.NewJID(digits, types.DefaultUserServer), true
	}
	if digits, ok := strings.CutPrefix(person, "tel:"); ok {
		return types.NewJID(digits, types.DefaultUserServer), digits != ""
	}
	if domain.NetworkOf(person) == domain.ProtocolWhatsApp {
		jid, err := types.ParseJID(domain.ParseID(person).Native)
		return jid, err == nil
	}
	return types.JID{}, false
}
