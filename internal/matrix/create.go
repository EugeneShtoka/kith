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
	req := &mautrix.ReqCreateRoom{
		Name:       spec.Name,
		Preset:     "private_chat",
		Visibility: "private",
		Invite:     userIDs(spec.Invite),
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
	roomID := domain.RoomID(resp.RoomID)
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
func userIDs(users []string) []id.UserID {
	out := make([]id.UserID, 0, len(users))
	for _, user := range users {
		if user != "" {
			out = append(out, id.UserID(user))
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
	user := id.UserID(peer)
	if slices.Contains(direct[user], id.RoomID(roomID)) {
		return nil
	}
	direct[user] = append(direct[user], id.RoomID(roomID))
	if err := b.client.SetAccountData(ctx, "m.direct", direct); err != nil {
		return fmt.Errorf("write m.direct: %w", err)
	}
	return nil
}
