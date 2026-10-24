package matrix

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Creating rooms and spaces: the same call, with m.space in the creation content.

// megolm is the room encryption algorithm, written into initial state.
const megolm = "m.megolm.v1.aes-sha2"

// CreateRoom makes a room or a space and returns its ID. Encryption can only be set
// at creation (in initial state); spaces are never encrypted. Recording a DM and
// filing into a parent happen after, and their failure returns the ID with an error.
func (b *InProc) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	if spec.On != "" && spec.On != b.ids.owner() {
		return "", fmt.Errorf("matrix: %s is not this account", spec.On)
	}
	switch spec.Kind {
	case domain.ChatRoom:
		spec.Encrypted = true
	case domain.ChatRoomPublic:
		spec.Public = true
	case domain.ChatSpace:
		spec.Space = true
	case domain.ChatRoomUnencrypted, domain.ChatGroup, domain.ChatForum, domain.ChatChannel,
		domain.ChatPrivateChannel, domain.ChatCommunity:
	}
	req := &mautrix.ReqCreateRoom{
		Name:       spec.Name,
		Preset:     "private_chat",
		Visibility: "private",
		Invite:     b.ids.userIDs(spec.Invite),
	}
	if spec.Public {
		req.Preset, req.Visibility = "public_chat", "public"
	}
	if spec.Direct {
		// is_direct files it as a DM for the invitee; trusted_private_chat makes both
		// people equals.
		req.IsDirect = true
		req.Preset = "trusted_private_chat"
	}
	if spec.Space {
		req.CreationContent = map[string]any{"type": event.RoomTypeSpace}
	} else if spec.Encrypted {
		req.InitialState = []*event.Event{{
			Type:     event.StateEncryption,
			StateKey: new(""),
			Content: event.Content{Parsed: &event.EncryptionEventContent{
				Algorithm: megolm,
			}},
		}}
	}
	resp, err := b.client.CreateRoom(ctx, req)
	if err != nil {
		return "", fmt.Errorf("matrix: create %s: %w", spec.Name, err)
	}
	roomID := b.ids.room(resp.RoomID)
	if spec.Direct && len(spec.Invite) > 0 {
		// is_direct is only a hint to the invitee; our own m.direct is what marks it
		// a DM for our clients (see directPeers).
		if derr := b.recordDirect(ctx, spec.Invite[0], roomID); derr != nil {
			return roomID, fmt.Errorf("created it, but could not record it as a direct message: %w", derr)
		}
	}
	if spec.Parent != "" {
		if ferr := b.AddToSpace(ctx, spec.Parent, roomID); ferr != nil {
			return roomID, fmt.Errorf("created it, but could not file it into the space: %w", ferr)
		}
	}
	return roomID, nil
}

// userIDs converts invitee MXIDs, dropping empties.
func (x ids) userIDs(users []string) []id.UserID {
	out := make([]id.UserID, 0, len(users))
	for _, user := range users {
		if user != "" {
			out = append(out, x.nativeUser(user))
		}
	}
	return out
}

// recordDirect adds a room to this account's m.direct under peer: a read-modify-write
// of the whole map. A failed read aborts, since writing an empty map would un-mark
// every DM.
func (b *InProc) recordDirect(ctx context.Context, peer string, roomID domain.RoomID) error {
	direct := map[id.UserID][]id.RoomID{}
	if err := b.client.GetAccountData(ctx, "m.direct", &direct); err != nil && !errors.Is(err, mautrix.MNotFound) {
		return fmt.Errorf("read m.direct: %w", err)
	}
	user := b.ids.nativeUser(peer)
	if slices.Contains(direct[user], b.ids.nativeRoom(roomID)) {
		return nil
	}
	direct[user] = append(direct[user], b.ids.nativeRoom(roomID))
	if err := b.client.SetAccountData(ctx, "m.direct", direct); err != nil {
		return fmt.Errorf("write m.direct: %w", err)
	}
	return nil
}

// ChatOffers is the account's rooms and space, safe default first; encryption can only
// be chosen at creation, so it is a kind of its own.
func (m *Adapter) ChatOffers(context.Context) ([]domain.ChatOffer, error) {
	acc := m.accountNow()
	if acc.User == "" {
		return nil, nil
	}
	on := m.ids.owner()
	return []domain.ChatOffer{
		{On: on, Account: acc.User, Kind: domain.ChatRoom, Label: "Private room", Detail: "encrypted, invitation only"},
		{
			On: on, Account: acc.User, Kind: domain.ChatRoomUnencrypted, Label: "Private room, unencrypted",
			Detail: "invitation only — for bridges and bots that cannot read encrypted rooms",
		},
		{On: on, Account: acc.User, Kind: domain.ChatRoomPublic, Label: "Public room", Detail: "anyone who finds it can join; not encrypted"},
		{On: on, Account: acc.User, Kind: domain.ChatSpace, Label: "Space", Detail: "a container for rooms, not a conversation"},
	}, nil
}
