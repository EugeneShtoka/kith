package route

import (
	"context"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// What only Matrix has goes to Matrix; a call about another network's room is
// refused with api.ErrNotOnNetwork rather than sent to a homeserver that never
// heard of it.

// Spaces is Matrix's cached space hierarchy.
func (r *Router) Spaces(ctx context.Context) ([]domain.Space, error) { return r.matrix.Spaces(ctx) }

// RefreshSpaces refetches the space hierarchy.
func (r *Router) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	return r.matrix.RefreshSpaces(ctx)
}

// AddToSpace files a Matrix room into a space.
func (r *Router) AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return r.matrix.AddToSpace(ctx, spaceID, roomID)
}

// RemoveFromSpace takes a Matrix room out of a space.
func (r *Router) RemoveFromSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return r.matrix.RemoveFromSpace(ctx, spaceID, roomID)
}

// CachedInvites is the pending Matrix invites.
func (r *Router) CachedInvites(ctx context.Context) ([]domain.Room, error) {
	return r.matrix.CachedInvites(ctx)
}

// Invites streams the invite set.
func (r *Router) Invites() <-chan []domain.Room { return r.matrix.Invites() }

// JoinRoom joins a Matrix room by ID or alias.
func (r *Router) JoinRoom(ctx context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error) {
	return r.matrix.JoinRoom(ctx, roomIDOrAlias, via)
}

// CreateRoom makes a Matrix room or space.
func (r *Router) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	return r.matrix.CreateRoom(ctx, spec)
}

// InviteUser invites someone into a Matrix room.
func (r *Router) InviteUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return r.matrix.InviteUser(ctx, roomID, userID)
}

// KickUser removes someone from a Matrix room.
func (r *Router) KickUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return r.matrix.KickUser(ctx, roomID, userID, reason)
}

// BanUser bans someone from a Matrix room.
func (r *Router) BanUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return r.matrix.BanUser(ctx, roomID, userID, reason)
}

// UnbanUser lifts a ban in a Matrix room.
func (r *Router) UnbanUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return r.matrix.UnbanUser(ctx, roomID, userID)
}

// LeaveRoom leaves a Matrix room or rejects an invite.
func (r *Router) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return r.matrix.LeaveRoom(ctx, roomID)
}

// ListThreads is a Matrix room's threads.
func (r *Router) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	if err := matrixRoom(roomID); err != nil {
		return nil, err
	}
	return r.matrix.ListThreads(ctx, roomID)
}

// ThreadPage fetches one page of a Matrix thread.
func (r *Router) ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int) (domain.TimelinePage, error) {
	if err := matrixRoom(roomID); err != nil {
		return domain.TimelinePage{}, err
	}
	return r.matrix.ThreadPage(ctx, roomID, root, from, limit)
}

// MarkThreadRead sends a threaded receipt.
func (r *Router) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, private bool) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return r.matrix.MarkThreadRead(ctx, roomID, root, eventID, private)
}

// ThreadParticipant reports whether we took part in a Matrix thread; no other
// network has threads, so elsewhere we did not.
func (r *Router) ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool {
	if matrixRoom(roomID) != nil {
		return false
	}
	return r.matrix.ThreadParticipant(ctx, roomID, root)
}

// Verifications streams device verifications.
func (r *Router) Verifications() <-chan domain.Verification { return r.matrix.Verifications() }

// StartVerification asks this account's other devices to verify this one.
func (r *Router) StartVerification(ctx context.Context) (string, error) {
	return r.matrix.StartVerification(ctx)
}

// AcceptVerification accepts a verification request.
func (r *Router) AcceptVerification(ctx context.Context, txnID string) error {
	return r.matrix.AcceptVerification(ctx, txnID)
}

// ConfirmSAS confirms the emoji matched.
func (r *Router) ConfirmSAS(ctx context.Context, txnID string) error {
	return r.matrix.ConfirmSAS(ctx, txnID)
}

// CancelVerification cancels a verification.
func (r *Router) CancelVerification(ctx context.Context, txnID string) error {
	return r.matrix.CancelVerification(ctx, txnID)
}

// RestoreKeyBackup imports the backed-up room keys.
func (r *Router) RestoreKeyBackup(ctx context.Context, secret string) (int, error) {
	return r.matrix.RestoreKeyBackup(ctx, secret)
}

// ExportRoomKeys is an encrypted key export.
func (r *Router) ExportRoomKeys(ctx context.Context, passphrase string) ([]byte, error) {
	return r.matrix.ExportRoomKeys(ctx, passphrase)
}

// ImportRoomKeys imports a key export.
func (r *Router) ImportRoomKeys(ctx context.Context, passphrase string, data []byte) (imported, total int, err error) {
	return r.matrix.ImportRoomKeys(ctx, passphrase, data)
}

// BootstrapKeyBackup creates cross-signing, secret storage and a key backup.
func (r *Router) BootstrapKeyBackup(ctx context.Context, password string) (domain.KeyBackup, error) {
	return r.matrix.BootstrapKeyBackup(ctx, password)
}

// Attached never fires in-process (see matrix.InProc.Attached).
func (r *Router) Attached() <-chan bool { return r.matrix.Attached() }
