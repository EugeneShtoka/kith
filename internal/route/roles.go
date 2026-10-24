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

// JoinRoom joins a room by ID or alias, on the network its ID names; a Matrix address
// (a room ID or alias as Matrix writes it) goes to Matrix.
func (r *Router) JoinRoom(ctx context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error) {
	on := domain.RoomID(roomIDOrAlias)
	if domain.IsMatrixAddress(roomIDOrAlias) {
		on = domain.RoomID(domain.NetworkRooms(domain.ProtocolMatrix))
	}
	return onRoom(r, on, "joining", func(c api.Membership) (domain.RoomID, error) {
		return c.JoinRoom(ctx, roomIDOrAlias, via)
	})
}

// CreateRoom makes a chat, room or space on the account spec.On names; a call naming
// none is refused, never sent to some account.
func (r *Router) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	if spec.On == "" {
		return "", fmt.Errorf("%w: a new room names no account", api.ErrNotOnNetwork)
	}
	return onRoom(r, domain.RoomID(spec.On), "new chats", func(c ChatMaker) (domain.RoomID, error) {
		return c.CreateRoom(ctx, spec)
	})
}

// Accounts is every connected account of every network that lists its accounts.
func (r *Router) Accounts(ctx context.Context) ([]domain.Account, error) {
	return gather(r, func(c AccountLister) ([]domain.Account, error) { return c.Accounts(), nil })
}

// ChatOffers is every kind of chat each connected account of every network can make.
func (r *Router) ChatOffers(ctx context.Context) ([]domain.ChatOffer, error) {
	return gather(r, func(c ChatMaker) ([]domain.ChatOffer, error) { return c.ChatOffers(ctx) })
}

// ResolveLink looks up the chat a link names, on the account on names.
func (r *Router) ResolveLink(ctx context.Context, uri string, on domain.RoomOwner) (domain.LinkTarget, error) {
	return onLink(r, uri, on, func(c LinkFollower) (domain.LinkTarget, error) { return c.ResolveLink(ctx, uri, on) })
}

// JoinLink joins the chat a link names, on the account on names.
func (r *Router) JoinLink(ctx context.Context, uri string, on domain.RoomOwner) (domain.RoomID, error) {
	return onLink(r, uri, on, func(c LinkFollower) (domain.RoomID, error) { return c.JoinLink(ctx, uri, on) })
}

// onLink runs call on the account on names, which must be on the network uri is a
// link for.
func onLink[T any](r *Router, uri string, on domain.RoomOwner, call func(LinkFollower) (T, error)) (T, error) {
	var zero T
	network, ok := domain.LinkNetwork(uri)
	if !ok {
		return zero, fmt.Errorf("%w: no network's link: %s", api.ErrNotOnNetwork, uri)
	}
	if got := domain.NetworkOf(string(on)); got != network {
		return zero, fmt.Errorf("%w: a %s link on %q", api.ErrNotOnNetwork, network, on)
	}
	return onRoom(r, domain.RoomID(on), "following links", call)
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

// StartVerification asks the other devices of the account on names to verify this one.
func (r *Router) StartVerification(ctx context.Context, on domain.RoomOwner) (string, error) {
	return onRoom(r, domain.RoomID(on), "device verification", func(c api.Verification) (string, error) {
		return c.StartVerification(ctx, on)
	})
}

// onVerification runs call on Matrix, whose adapter knows the account a transaction is
// for.
func onVerification(r *Router, call func(api.Verification) error) error {
	return doOnRoom(r, domain.RoomID(domain.NetworkRooms(domain.ProtocolMatrix)), "device verification", call)
}

// AcceptVerification accepts a verification request.
func (r *Router) AcceptVerification(ctx context.Context, txnID string) error {
	return onVerification(r, func(c api.Verification) error { return c.AcceptVerification(ctx, txnID) })
}

// ConfirmSAS confirms the emoji matched.
func (r *Router) ConfirmSAS(ctx context.Context, txnID string) error {
	return onVerification(r, func(c api.Verification) error { return c.ConfirmSAS(ctx, txnID) })
}

// CancelVerification cancels a verification.
func (r *Router) CancelVerification(ctx context.Context, txnID string) error {
	return onVerification(r, func(c api.Verification) error { return c.CancelVerification(ctx, txnID) })
}

// RestoreKeyBackup imports the backed-up room keys of the account on names.
func (r *Router) RestoreKeyBackup(ctx context.Context, on domain.RoomOwner, secret string) (int, error) {
	return onRoom(r, domain.RoomID(on), "key backup", func(c api.Keys) (int, error) { return c.RestoreKeyBackup(ctx, on, secret) })
}

// ExportRoomKeys is an encrypted key export of the account on names.
func (r *Router) ExportRoomKeys(ctx context.Context, on domain.RoomOwner, passphrase string) ([]byte, error) {
	return onRoom(r, domain.RoomID(on), "key export", func(c api.Keys) ([]byte, error) { return c.ExportRoomKeys(ctx, on, passphrase) })
}

// ImportRoomKeys imports a key export into the account on names.
func (r *Router) ImportRoomKeys(ctx context.Context, on domain.RoomOwner, passphrase string, data []byte) (imported, total int, err error) {
	type counts struct{ imported, total int }
	got, err := onRoom(r, domain.RoomID(on), "key import", func(c api.Keys) (counts, error) {
		i, n, ierr := c.ImportRoomKeys(ctx, on, passphrase, data)
		return counts{i, n}, ierr
	})
	return got.imported, got.total, err
}

// BootstrapKeyBackup creates cross-signing, secret storage and a key backup for the
// account on names.
func (r *Router) BootstrapKeyBackup(ctx context.Context, on domain.RoomOwner, password string) (domain.KeyBackup, error) {
	return onRoom(r, domain.RoomID(on), "key backup", func(c api.Keys) (domain.KeyBackup, error) {
		return c.BootstrapKeyBackup(ctx, on, password)
	})
}

// Attached never fires in-process (see matrix.InProc.Attached).
func (r *Router) Attached() <-chan bool { return neverAttaches }

// neverAttaches is closed: an in-process backend is never re-subscribed.
var neverAttaches = func() chan bool {
	ch := make(chan bool)
	close(ch)
	return ch
}()
