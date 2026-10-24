package route

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Roles only some networks have (Matrix today): membership, filing rooms into spaces,
// device verification and key backup. A room call goes to the room's network; others
// go to the network with the role. Without it, lists are empty and actions refuse
// with api.ErrNetworkOff.

// AddToSpace files a room into a space of the same network; another network's space
// (a WhatsApp community) is its admins' to fill.
func (r *Router) AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	if err := sameNetwork(spaceID, roomID); err != nil {
		return err
	}
	return doOnRoom(r, roomID, "filing into spaces", func(c SpaceEditor) error { return c.AddToSpace(ctx, spaceID, roomID) })
}

// RemoveFromSpace takes a room out of a space of the same network.
func (r *Router) RemoveFromSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	if err := sameNetwork(spaceID, roomID); err != nil {
		return err
	}
	return doOnRoom(r, roomID, "filing into spaces", func(c SpaceEditor) error { return c.RemoveFromSpace(ctx, spaceID, roomID) })
}

// sameNetwork refuses filing a room into another network's space.
func sameNetwork(spaceID domain.SpaceID, roomID domain.RoomID) error {
	space, room := domain.NetworkOf(string(spaceID)), domain.NetworkOf(string(roomID))
	if space != room {
		return fmt.Errorf("%w: a %s room into a %s space (%s)", api.ErrNotOnNetwork, room, space, spaceID)
	}
	return nil
}

// CachedInvites is every network's pending invites.
func (r *Router) CachedInvites(ctx context.Context) ([]domain.Room, error) {
	return gather(r, func(c api.Membership) ([]domain.Room, error) { return c.CachedInvites(ctx) })
}

// JoinRoom joins a room by ID or alias, on the network its ID names.
func (r *Router) JoinRoom(ctx context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error) {
	return onRoom(r, domain.RoomID(roomIDOrAlias), "joining", func(c api.Membership) (domain.RoomID, error) {
		return c.JoinRoom(ctx, roomIDOrAlias, via)
	})
}

// CreateRoom makes a room or space on Matrix, or a chat on the account spec.On names.
func (r *Router) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	if spec.On != "" && spec.On != domain.MatrixRooms {
		return onRoom(r, domain.RoomID(spec.On), "new chats", func(c ChatMaker) (domain.RoomID, error) {
			return c.CreateRoom(ctx, spec)
		})
	}
	c, err := sole[api.Membership](r, "new rooms")
	if err != nil {
		return "", err
	}
	return c.CreateRoom(ctx, spec)
}

// ChatOffers is every kind of chat each connected account of every network can make.
func (r *Router) ChatOffers(ctx context.Context) ([]domain.ChatOffer, error) {
	return gather(r, func(c ChatMaker) ([]domain.ChatOffer, error) { return c.ChatOffers(ctx) })
}

// ResolveLink looks up the chat a link names, on the network the link is for.
func (r *Router) ResolveLink(ctx context.Context, uri string) (domain.LinkTarget, error) {
	return onLink(r, uri, func(c LinkFollower) (domain.LinkTarget, error) { return c.ResolveLink(ctx, uri) })
}

// JoinLink joins the chat a link names, on the network the link is for.
func (r *Router) JoinLink(ctx context.Context, uri string) (domain.RoomID, error) {
	return onLink(r, uri, func(c LinkFollower) (domain.RoomID, error) { return c.JoinLink(ctx, uri) })
}

// onLink runs call on the network uri is a link for.
func onLink[T any](r *Router, uri string, call func(LinkFollower) (T, error)) (T, error) {
	network, ok := domain.LinkNetwork(uri)
	if !ok {
		var zero T
		return zero, fmt.Errorf("%w: no network's link: %s", api.ErrNotOnNetwork, uri)
	}
	return onRoom(r, domain.RoomID(domain.NetworkRooms(network)), "following links", call)
}

// InviteUser invites someone into a room.
func (r *Router) InviteUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	return doOnRoom(r, roomID, "invites", func(c api.Membership) error { return c.InviteUser(ctx, roomID, userID) })
}

// KickUser removes someone from a room.
func (r *Router) KickUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	return doOnRoom(r, roomID, "moderation", func(c api.Membership) error { return c.KickUser(ctx, roomID, userID, reason) })
}

// BanUser bans someone from a room.
func (r *Router) BanUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	return doOnRoom(r, roomID, "moderation", func(c api.Membership) error { return c.BanUser(ctx, roomID, userID, reason) })
}

// UnbanUser lifts a ban in a room.
func (r *Router) UnbanUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	return doOnRoom(r, roomID, "moderation", func(c api.Membership) error { return c.UnbanUser(ctx, roomID, userID) })
}

// LeaveRoom leaves a room or rejects an invite.
func (r *Router) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	return doOnRoom(r, roomID, "leaving", func(c Leaver) error { return c.LeaveRoom(ctx, roomID) })
}

// DeleteChat deletes a chat that cannot be left.
func (r *Router) DeleteChat(ctx context.Context, roomID domain.RoomID, forEveryone bool) error {
	return doOnRoom(r, roomID, "deleting chats", func(c ChatDeleter) error { return c.DeleteChat(ctx, roomID, forEveryone) })
}

// SignOut signs out the account whose own space it is.
func (r *Router) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	return doOnRoom(r, domain.RoomID(space), "signing out", func(c SignOuter) error { return c.SignOut(ctx, space, forget) })
}

// StartVerification asks this account's other devices to verify this one.
func (r *Router) StartVerification(ctx context.Context) (string, error) {
	c, err := sole[api.Verification](r, "device verification")
	if err != nil {
		return "", err
	}
	return c.StartVerification(ctx)
}

// AcceptVerification accepts a verification request.
func (r *Router) AcceptVerification(ctx context.Context, txnID string) error {
	c, err := sole[api.Verification](r, "device verification")
	if err != nil {
		return err
	}
	return c.AcceptVerification(ctx, txnID)
}

// ConfirmSAS confirms the emoji matched.
func (r *Router) ConfirmSAS(ctx context.Context, txnID string) error {
	c, err := sole[api.Verification](r, "device verification")
	if err != nil {
		return err
	}
	return c.ConfirmSAS(ctx, txnID)
}

// CancelVerification cancels a verification.
func (r *Router) CancelVerification(ctx context.Context, txnID string) error {
	c, err := sole[api.Verification](r, "device verification")
	if err != nil {
		return err
	}
	return c.CancelVerification(ctx, txnID)
}

// RestoreKeyBackup imports the backed-up room keys.
func (r *Router) RestoreKeyBackup(ctx context.Context, secret string) (int, error) {
	c, err := sole[api.Keys](r, "key backup")
	if err != nil {
		return 0, err
	}
	return c.RestoreKeyBackup(ctx, secret)
}

// ExportRoomKeys is an encrypted key export.
func (r *Router) ExportRoomKeys(ctx context.Context, passphrase string) ([]byte, error) {
	c, err := sole[api.Keys](r, "key export")
	if err != nil {
		return nil, err
	}
	return c.ExportRoomKeys(ctx, passphrase)
}

// ImportRoomKeys imports a key export.
func (r *Router) ImportRoomKeys(ctx context.Context, passphrase string, data []byte) (imported, total int, err error) {
	c, err := sole[api.Keys](r, "key import")
	if err != nil {
		return 0, 0, err
	}
	return c.ImportRoomKeys(ctx, passphrase, data)
}

// BootstrapKeyBackup creates cross-signing, secret storage and a key backup.
func (r *Router) BootstrapKeyBackup(ctx context.Context, password string) (domain.KeyBackup, error) {
	c, err := sole[api.Keys](r, "key backup")
	if err != nil {
		return domain.KeyBackup{}, err
	}
	return c.BootstrapKeyBackup(ctx, password)
}

// Attached never fires in-process (see matrix.InProc.Attached).
func (r *Router) Attached() <-chan bool { return neverAttaches }

// neverAttaches is closed: an in-process backend is never re-subscribed.
var neverAttaches = func() chan bool {
	ch := make(chan bool)
	close(ch)
	return ch
}()
