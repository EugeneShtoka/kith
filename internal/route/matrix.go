package route

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// What only Matrix has goes to Matrix; a call about another network's room is
// refused with api.ErrNotOnNetwork rather than sent to a homeserver that never
// heard of it. Without Matrix (not configured, or not logged in yet) its lists are
// empty and its actions are refused with api.ErrNetworkOff.

// errMatrixOff is a Matrix-only action asked of a daemon with no Matrix session.
var errMatrixOff = fmt.Errorf("%w: matrix (not logged in)", api.ErrNetworkOff)

// matrixOn reports whether Matrix is configured and logged in.
func (r *Router) matrixOn() bool { return r.matrix != nil && r.matrix.LoggedIn() }

// onMatrix runs a Matrix-only action, or refuses it without Matrix.
func onMatrix[T any](r *Router, call func(Matrix) (T, error)) (T, error) {
	if !r.matrixOn() {
		var zero T
		return zero, errMatrixOff
	}
	return call(r.matrix)
}

// doOnMatrix is onMatrix for an action that returns only an error.
func doOnMatrix(r *Router, call func(Matrix) error) error {
	_, err := onMatrix(r, func(m Matrix) (struct{}, error) { return struct{}{}, call(m) })
	return err
}

// matrixList is a Matrix-only list: empty without Matrix, as there is nothing of it.
func matrixList[T any](r *Router, read func(Matrix) ([]T, error)) ([]T, error) {
	if !r.matrixOn() {
		return nil, nil
	}
	return read(r.matrix)
}

// onMatrixRoom is doOnMatrix for an action about one Matrix room.
func onMatrixRoom(r *Router, roomID domain.RoomID, call func(Matrix) error) error {
	if err := matrixRoom(roomID); err != nil {
		return err
	}
	return doOnMatrix(r, call)
}

// AddToSpace files a Matrix room into a Matrix space. Another network's space (a
// WhatsApp community) is its admins' to fill.
func (r *Router) AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	if err := matrixRoom(domain.RoomID(spaceID)); err != nil {
		return err
	}
	return onMatrixRoom(r, roomID, func(m Matrix) error { return m.AddToSpace(ctx, spaceID, roomID) })
}

// RemoveFromSpace takes a Matrix room out of a Matrix space.
func (r *Router) RemoveFromSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	if err := matrixRoom(domain.RoomID(spaceID)); err != nil {
		return err
	}
	return onMatrixRoom(r, roomID, func(m Matrix) error { return m.RemoveFromSpace(ctx, spaceID, roomID) })
}

// CachedInvites is the pending Matrix invites.
func (r *Router) CachedInvites(ctx context.Context) ([]domain.Room, error) {
	return matrixList(r, func(m Matrix) ([]domain.Room, error) { return m.CachedInvites(ctx) })
}

// Invites streams the invite set; without Matrix configured, it carries nothing and
// closes when the router stops.
func (r *Router) Invites() <-chan []domain.Room {
	if r.matrix == nil {
		return r.noInvites
	}
	return r.matrix.Invites()
}

// JoinRoom joins a Matrix room by ID or alias.
func (r *Router) JoinRoom(ctx context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error) {
	return onMatrix(r, func(m Matrix) (domain.RoomID, error) { return m.JoinRoom(ctx, roomIDOrAlias, via) })
}

// CreateRoom makes a Matrix room or space.
func (r *Router) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	return onMatrix(r, func(m Matrix) (domain.RoomID, error) { return m.CreateRoom(ctx, spec) })
}

// InviteUser invites someone into a Matrix room.
func (r *Router) InviteUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	return onMatrixRoom(r, roomID, func(m Matrix) error { return m.InviteUser(ctx, roomID, userID) })
}

// KickUser removes someone from a Matrix room.
func (r *Router) KickUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	return onMatrixRoom(r, roomID, func(m Matrix) error { return m.KickUser(ctx, roomID, userID, reason) })
}

// BanUser bans someone from a Matrix room.
func (r *Router) BanUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	return onMatrixRoom(r, roomID, func(m Matrix) error { return m.BanUser(ctx, roomID, userID, reason) })
}

// UnbanUser lifts a ban in a Matrix room.
func (r *Router) UnbanUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	return onMatrixRoom(r, roomID, func(m Matrix) error { return m.UnbanUser(ctx, roomID, userID) })
}

// LeaveRoom leaves a Matrix room or rejects an invite.
func (r *Router) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	return onMatrixRoom(r, roomID, func(m Matrix) error { return m.LeaveRoom(ctx, roomID) })
}

// ListThreads is a Matrix room's threads.
func (r *Router) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	if err := matrixRoom(roomID); err != nil {
		return nil, err
	}
	return onMatrix(r, func(m Matrix) ([]domain.Thread, error) { return m.ListThreads(ctx, roomID) })
}

// ThreadPage fetches one page of a Matrix thread.
func (r *Router) ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int) (domain.TimelinePage, error) {
	if err := matrixRoom(roomID); err != nil {
		return domain.TimelinePage{}, err
	}
	return onMatrix(r, func(m Matrix) (domain.TimelinePage, error) { return m.ThreadPage(ctx, roomID, root, from, limit) })
}

// MarkThreadRead sends a threaded receipt.
func (r *Router) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, private bool) error {
	return onMatrixRoom(r, roomID, func(m Matrix) error { return m.MarkThreadRead(ctx, roomID, root, eventID, private) })
}

// ThreadParticipant reports whether we took part in a Matrix thread; no other
// network has threads, so elsewhere (or without Matrix) we did not.
func (r *Router) ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool {
	if matrixRoom(roomID) != nil || !r.matrixOn() {
		return false
	}
	return r.matrix.ThreadParticipant(ctx, roomID, root)
}

// Verifications streams device verifications; without Matrix configured, it carries
// nothing and closes when the router stops.
func (r *Router) Verifications() <-chan domain.Verification {
	if r.matrix == nil {
		return r.noVerifications
	}
	return r.matrix.Verifications()
}

// StartVerification asks this account's other devices to verify this one.
func (r *Router) StartVerification(ctx context.Context) (string, error) {
	return onMatrix(r, func(m Matrix) (string, error) { return m.StartVerification(ctx) })
}

// AcceptVerification accepts a verification request.
func (r *Router) AcceptVerification(ctx context.Context, txnID string) error {
	return doOnMatrix(r, func(m Matrix) error { return m.AcceptVerification(ctx, txnID) })
}

// ConfirmSAS confirms the emoji matched.
func (r *Router) ConfirmSAS(ctx context.Context, txnID string) error {
	return doOnMatrix(r, func(m Matrix) error { return m.ConfirmSAS(ctx, txnID) })
}

// CancelVerification cancels a verification.
func (r *Router) CancelVerification(ctx context.Context, txnID string) error {
	return doOnMatrix(r, func(m Matrix) error { return m.CancelVerification(ctx, txnID) })
}

// RestoreKeyBackup imports the backed-up room keys.
func (r *Router) RestoreKeyBackup(ctx context.Context, secret string) (int, error) {
	return onMatrix(r, func(m Matrix) (int, error) { return m.RestoreKeyBackup(ctx, secret) })
}

// ExportRoomKeys is an encrypted key export.
func (r *Router) ExportRoomKeys(ctx context.Context, passphrase string) ([]byte, error) {
	return onMatrix(r, func(m Matrix) ([]byte, error) { return m.ExportRoomKeys(ctx, passphrase) })
}

// ImportRoomKeys imports a key export.
func (r *Router) ImportRoomKeys(ctx context.Context, passphrase string, data []byte) (imported, total int, err error) {
	if !r.matrixOn() {
		return 0, 0, errMatrixOff
	}
	return r.matrix.ImportRoomKeys(ctx, passphrase, data)
}

// BootstrapKeyBackup creates cross-signing, secret storage and a key backup.
func (r *Router) BootstrapKeyBackup(ctx context.Context, password string) (domain.KeyBackup, error) {
	return onMatrix(r, func(m Matrix) (domain.KeyBackup, error) { return m.BootstrapKeyBackup(ctx, password) })
}

// Attached never fires in-process (see matrix.InProc.Attached).
func (r *Router) Attached() <-chan bool { return neverAttaches }

// neverAttaches is closed: an in-process backend is never re-subscribed.
var neverAttaches = func() chan bool {
	ch := make(chan bool)
	close(ch)
	return ch
}()
