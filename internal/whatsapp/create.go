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

// A group or a community is made on one account with its members at once: WhatsApp
// takes them by number, and adds the account itself. A community comes with its
// announcement group, which the server makes.

// groupNameMost is the longest group name WhatsApp takes, in characters.
const groupNameMost = 25

// CreateRoom makes a group or a community on the account spec.On names.
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

// personJID is a person's WhatsApp address: a number as the directory writes it, or a
// WhatsApp ID.
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
