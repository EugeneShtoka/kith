package matrix

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// How each call reaches its account: one naming a room, space, event or address goes to
// the account its ID is under; one naming an account (on) to that one; a list is
// served by every logged-in account, and one the cache answers (rooms, unread, spaces)
// by every configured account, a logged-out one from its cache (kept); and a
// person-level change (ignore, a mark) is made on every logged-in account, as it
// applies to all of them.

// runnerOn is account on's runner, refusing an account not configured or logged out.
func (m *Adapter) runnerOn(on domain.RoomOwner) (*runner, error) {
	for _, r := range m.runnersNow() {
		if r.ids.owner() != on {
			continue
		}
		if !r.loggedIn.Load() {
			return nil, fmt.Errorf("matrix: %s is logged out: %w", r.account().Name, api.ErrNetworkOff)
		}
		return r, nil
	}
	return nil, fmt.Errorf("matrix: %s is no Matrix account here: %w", on, api.ErrNotOnNetwork)
}

// configured reports whether on is a configured account's, logged in or not.
func (m *Adapter) configured(on domain.RoomOwner) bool {
	return on != "" && slices.ContainsFunc(m.runnersNow(), func(r *runner) bool { return r.ids.owner() == on })
}

// runnerOf is the runner of the account id is under: a room, space or event, or an
// address, in kith's form.
func (m *Adapter) runnerOf(id string) (*runner, error) {
	on := ownerOf(id)
	if on == "" || domain.NetworkOf(id) != domain.ProtocolMatrix {
		return nil, fmt.Errorf("matrix: %s names no Matrix account: %w", id, api.ErrNotOnNetwork)
	}
	return m.runnerOn(on)
}

// onRoom runs call on the runner of the account room is under.
func onRoom[T any](m *Adapter, room domain.RoomID, call func(*runner) (T, error)) (T, error) {
	r, err := m.runnerOf(string(room))
	if err != nil {
		var zero T
		return zero, err
	}
	return call(r)
}

// byAccount splits rooms among the logged-in accounts they are under; a room of none
// is in rest.
func (m *Adapter) byAccount(rooms []domain.RoomID) (by map[*runner][]domain.RoomID, rest []domain.RoomID) {
	by = map[*runner][]domain.RoomID{}
	for _, room := range rooms {
		if r, err := m.runnerOf(string(room)); err == nil {
			by[r] = append(by[r], room)
		} else {
			rest = append(rest, room)
		}
	}
	return by, rest
}

// everyLive runs call on every logged-in account, joining what failed.
func (m *Adapter) everyLive(call func(*runner) error) error {
	var errs []error
	for _, r := range m.live() {
		errs = append(errs, call(r))
	}
	return errors.Join(errs...)
}

// gather is every logged-in account's list, in config order.
func gather[T any](m *Adapter, list func(*runner) ([]T, error)) ([]T, error) {
	var out []T
	for _, r := range m.live() {
		got, err := list(r)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// kept is every configured account's list as the cache holds it, in config order: a
// logged-in account answers with live, any other with cache, read from the cache
// alone (it has no session to ask).
func kept[T any](m *Adapter, live, cache func(*runner) ([]T, error)) ([]T, error) {
	var out []T
	for _, r := range m.runnersNow() {
		read := live
		if !r.loggedIn.Load() {
			read = cache
		}
		got, err := read(r)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// Rooms is every account's cached rooms, a logged-out one's too.
func (m *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	rooms := func(r *runner) ([]domain.Room, error) { return r.Rooms(ctx) }
	return kept(m, rooms, rooms)
}

// RefreshRooms lists every logged-in account's rooms anew; a logged-out one's are the
// cached ones.
func (m *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	return kept(m,
		func(r *runner) ([]domain.Room, error) { return r.RefreshRooms(ctx) },
		func(r *runner) ([]domain.Room, error) { return r.Rooms(ctx) })
}

// Spaces is every account's cached spaces, and a space per account holding its rooms.
func (m *Adapter) Spaces(ctx context.Context) ([]domain.Space, error) {
	cachedSpaces := func(r *runner) ([]domain.Space, error) { return r.Spaces(ctx) }
	spaces, err := kept(m, cachedSpaces, cachedSpaces)
	if err != nil {
		return nil, err
	}
	return m.withAccountSpaces(ctx, spaces)
}

// RefreshSpaces lists every logged-in account's spaces anew, a logged-out one's from
// the cache, and the account spaces.
func (m *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	spaces, err := kept(m,
		func(r *runner) ([]domain.Space, error) { return r.RefreshSpaces(ctx) },
		func(r *runner) ([]domain.Space, error) { return r.Spaces(ctx) })
	if err != nil {
		return nil, err
	}
	return m.withAccountSpaces(ctx, spaces)
}

// CachedInvites is every account's cached invitations.
func (m *Adapter) CachedInvites(ctx context.Context) ([]domain.Room, error) {
	invites, err := gather(m, func(r *runner) ([]domain.Room, error) { return r.CachedInvites(ctx) })
	domain.SortRooms(invites)
	return invites, err
}

// CachedUnread is every account's cached unread state, a logged-out one's too.
func (m *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	unread := func(r *runner) ([]domain.Unread, error) { return r.CachedUnread(ctx) }
	return kept(m, unread, unread)
}

// DirectCandidates ranks people to start a DM with: those no account has one with yet.
func (m *Adapter) DirectCandidates(ctx context.Context, limit int) ([]domain.Member, error) {
	var out []domain.Member
	for i, r := range m.live() {
		got, err := r.DirectCandidates(ctx, 0)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			out = got
			continue
		}
		out = slices.DeleteFunc(out, func(c domain.Member) bool {
			return !slices.ContainsFunc(got, func(g domain.Member) bool { return g.UserID == c.UserID })
		})
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Me is every account's IDs that are this person, a logged-out one's too: its cached
// messages are still yours.
func (m *Adapter) Me() []string {
	var me []string
	for _, r := range m.runnersNow() {
		for _, id := range r.Me() {
			if !slices.Contains(me, id) {
				me = append(me, id)
			}
		}
	}
	return me
}

// Ignored is everyone any account ignores, as the homeservers have them.
func (m *Adapter) Ignored(ctx context.Context) ([]string, error) {
	users, err := gather(m, func(r *runner) ([]string, error) { return r.Ignored(ctx) })
	slices.Sort(users)
	return slices.Compact(users), err
}

// Ignore ignores user on every account: ignoring is a person's, not an account's.
func (m *Adapter) Ignore(ctx context.Context, user string) error {
	return m.everyLive(func(r *runner) error { return r.Ignore(ctx, user) })
}

// Unignore stops ignoring user on every account.
func (m *Adapter) Unignore(ctx context.Context, user string) error {
	return m.everyLive(func(r *runner) error { return r.Unignore(ctx, user) })
}

// MarkPerson applies a mark or a release on every account: it is the person's. A
// release on any account refuses a mark there (domain.ErrPersonReleased).
func (m *Adapter) MarkPerson(ctx context.Context, verdict domain.PersonVerdict) error {
	if verdict.At.IsZero() {
		verdict.At = time.Now() // one time on every account, so the newest is one verdict
	}
	return m.everyLive(func(r *runner) error { return r.MarkPerson(ctx, verdict) })
}

// RetagVerdicts moves tag from's verdicts to tag to on every account.
func (m *Adapter) RetagVerdicts(ctx context.Context, from, to string) error {
	return m.everyLive(func(r *runner) error { return r.RetagVerdicts(ctx, from, to) })
}

// RewindSync rewinds every account to a full initial sync.
func (m *Adapter) RewindSync(ctx context.Context) error {
	return m.everyLive(func(r *runner) error { return r.RewindSync(ctx) })
}

// RecountUnread counts rooms' unread again, each on its account.
func (m *Adapter) RecountUnread(ctx context.Context, rooms []domain.RoomID) {
	by, _ := m.byAccount(rooms)
	for r, mine := range by {
		r.RecountUnread(ctx, mine)
	}
}

// RoomEncryption reports which rooms are encrypted, each asked of its account. A room
// of an account that is logged out reports as encrypted, as unknown is (the router
// does the same for a network that is off); one of no account here is refused.
func (m *Adapter) RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	by, rest := m.byAccount(roomIDs)
	out := map[domain.RoomID]bool{}
	for _, room := range rest {
		if !m.configured(ownerOf(string(room))) {
			return nil, fmt.Errorf("matrix: %s names no Matrix account here: %w", room, api.ErrNotOnNetwork)
		}
		out[room] = true
	}
	for r, mine := range by {
		got, err := r.RoomEncryption(ctx, mine)
		if err != nil {
			return nil, err
		}
		maps.Copy(out, got)
	}
	return out, nil
}

// MarkRoomsRead marks rooms read, each by its account; a room of none fails.
func (m *Adapter) MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error) {
	by, rest := m.byAccount(roomIDs)
	var out domain.ReadResult
	if len(rest) > 0 {
		out.Failed, out.FirstError = len(rest), fmt.Sprintf("%s names no logged-in Matrix account", rest[0])
	}
	for _, r := range m.live() {
		mine := by[r]
		if len(mine) == 0 {
			continue
		}
		got, err := r.MarkRoomsRead(ctx, mine, private)
		if err != nil {
			return out, err
		}
		out.Marked, out.Skipped, out.Failed = out.Marked+got.Marked, out.Skipped+got.Skipped, out.Failed+got.Failed
		if out.FirstError == "" {
			out.FirstError = got.FirstError
		}
	}
	return out, nil
}

// PublicRooms lists the directory account on sees.
func (m *Adapter) PublicRooms(ctx context.Context, on domain.RoomOwner, server, since string) (domain.PublicRoomsPage, error) {
	r, err := m.runnerOn(on)
	if err != nil {
		return domain.PublicRoomsPage{}, err
	}
	return r.PublicRooms(ctx, on, server, since)
}

// StartVerification asks account on's other devices to verify this one.
func (m *Adapter) StartVerification(ctx context.Context, on domain.RoomOwner) (string, error) {
	r, err := m.runnerOn(on)
	if err != nil {
		return "", err
	}
	txn, err := r.StartVerification(ctx, on)
	if err == nil {
		m.mu.Lock()
		m.txns[txn] = r
		m.mu.Unlock()
	}
	return txn, err
}

// verifying is the runner verification txnID is on.
func (m *Adapter) verifying(txnID string) (*runner, error) {
	m.mu.Lock()
	r, ok := m.txns[txnID]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("matrix: no verification %s is in progress: %w", txnID, api.ErrNotOnNetwork)
	}
	return r, nil
}

// AcceptVerification accepts a request on the account it came to.
func (m *Adapter) AcceptVerification(ctx context.Context, txnID string) error {
	r, err := m.verifying(txnID)
	if err != nil {
		return err
	}
	return r.AcceptVerification(ctx, txnID)
}

// ConfirmSAS confirms the emojis match, on the verification's account.
func (m *Adapter) ConfirmSAS(ctx context.Context, txnID string) error {
	r, err := m.verifying(txnID)
	if err != nil {
		return err
	}
	return r.ConfirmSAS(ctx, txnID)
}

// CancelVerification cancels a verification on its account.
func (m *Adapter) CancelVerification(ctx context.Context, txnID string) error {
	r, err := m.verifying(txnID)
	if err != nil {
		return err
	}
	return r.CancelVerification(ctx, txnID)
}

// RestoreKeyBackup restores account on's room keys from its backup.
func (m *Adapter) RestoreKeyBackup(ctx context.Context, on domain.RoomOwner, secret string) (int, error) {
	r, err := m.runnerOn(on)
	if err != nil {
		return 0, err
	}
	return r.RestoreKeyBackup(ctx, on, secret)
}

// ExportRoomKeys exports account on's room keys.
func (m *Adapter) ExportRoomKeys(ctx context.Context, on domain.RoomOwner, passphrase string) ([]byte, error) {
	r, err := m.runnerOn(on)
	if err != nil {
		return nil, err
	}
	return r.ExportRoomKeys(ctx, on, passphrase)
}

// ImportRoomKeys imports room keys into account on.
func (m *Adapter) ImportRoomKeys(ctx context.Context, on domain.RoomOwner, passphrase string, data []byte) (int, int, error) {
	r, err := m.runnerOn(on)
	if err != nil {
		return 0, 0, err
	}
	return r.ImportRoomKeys(ctx, on, passphrase, data)
}

// BootstrapKeyBackup sets up account on's key backup.
func (m *Adapter) BootstrapKeyBackup(ctx context.Context, on domain.RoomOwner, password string) (domain.KeyBackup, error) {
	r, err := m.runnerOn(on)
	if err != nil {
		return domain.KeyBackup{}, err
	}
	return r.BootstrapKeyBackup(ctx, on, password)
}

// CreateRoom makes spec on the account it names.
func (m *Adapter) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	r, err := m.runnerOn(spec.On)
	if err != nil {
		return "", err
	}
	return r.CreateRoom(ctx, spec)
}

// JoinRoom joins a room by its kith ID, or an address under the account to join it as.
func (m *Adapter) JoinRoom(ctx context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error) {
	r, err := m.runnerOf(roomIDOrAlias)
	if err != nil {
		return "", err
	}
	return r.JoinRoom(ctx, roomIDOrAlias, via)
}

// KnockRoom knocks on a room by its kith ID, or an address under the account to knock as.
func (m *Adapter) KnockRoom(ctx context.Context, roomIDOrAlias, reason string, via []string) (domain.RoomID, error) {
	r, err := m.runnerOf(roomIDOrAlias)
	if err != nil {
		return "", err
	}
	return r.KnockRoom(ctx, roomIDOrAlias, reason, via)
}

// sameAccount refuses a space and room under different accounts.
func (m *Adapter) sameAccount(space domain.SpaceID, room domain.RoomID) (*runner, error) {
	if _, ok := m.accountSpace(space); ok {
		return nil, fmt.Errorf("matrix: %s is an account, not a space rooms are added to: %w", space, api.ErrNotOnNetwork)
	}
	if ownerOf(string(space)) != domain.OwnerOf(room) {
		return nil, fmt.Errorf("matrix: %s and %s are another account's each: %w", space, room, api.ErrNotOnNetwork)
	}
	return m.runnerOf(string(space))
}

// AddToSpace adds a room to a space of the same account.
func (m *Adapter) AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	r, err := m.sameAccount(spaceID, roomID)
	if err != nil {
		return err
	}
	return r.AddToSpace(ctx, spaceID, roomID)
}

// RemoveFromSpace takes a room out of a space of the same account.
func (m *Adapter) RemoveFromSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	r, err := m.sameAccount(spaceID, roomID)
	if err != nil {
		return err
	}
	return r.RemoveFromSpace(ctx, spaceID, roomID)
}

// LeaveRoom leaves a room or space on its account; an account's space is signed out of
// instead (SignOut).
func (m *Adapter) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	if _, ok := m.accountSpace(domain.SpaceID(roomID)); ok {
		return fmt.Errorf("matrix: %s is an account: sign out of it instead: %w", roomID, api.ErrNotOnNetwork)
	}
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.LeaveRoom(ctx, roomID) })
	return err
}

// MarkTag applies a tag verdict on the room's account.
func (m *Adapter) MarkTag(ctx context.Context, verdict domain.TagVerdict) error {
	_, err := onRoom(m, verdict.Room, func(r *runner) (struct{}, error) { return struct{}{}, r.MarkTag(ctx, verdict) })
	return err
}

// ThreadParticipant reports whether we took part in a thread; false off any account.
func (m *Adapter) ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool {
	took, _ := onRoom(m, roomID, func(r *runner) (bool, error) { return r.ThreadParticipant(ctx, roomID, root), nil })
	return took
}

// The calls naming a room, each on the room's account.

func (m *Adapter) BanUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.BanUser(ctx, roomID, userID, reason) })
	return err
}

func (m *Adapter) UnbanUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.UnbanUser(ctx, roomID, userID) })
	return err
}

func (m *Adapter) KickUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.KickUser(ctx, roomID, userID, reason) })
	return err
}

func (m *Adapter) InviteUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.InviteUser(ctx, roomID, userID) })
	return err
}

func (m *Adapter) CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	return onRoom(m, roomID, func(r *runner) (domain.SpaceID, error) { return r.CanonicalParent(ctx, roomID) })
}

func (m *Adapter) CreatePoll(
	ctx context.Context, roomID domain.RoomID, question string, options []string, multiple bool,
) (domain.EventID, error) {
	return onRoom(m, roomID, func(r *runner) (domain.EventID, error) {
		return r.CreatePoll(ctx, roomID, question, options, multiple)
	})
}

func (m *Adapter) VotePoll(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, options []string) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.VotePoll(ctx, roomID, eventID, options) })
	return err
}

func (m *Adapter) PollVoters(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.PollVoters, error) {
	return onRoom(m, roomID, func(r *runner) (domain.PollVoters, error) { return r.PollVoters(ctx, roomID, eventID) })
}

func (m *Adapter) FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	return onRoom(m, roomID, func(r *runner) (domain.Message, error) { return r.FetchEvent(ctx, roomID, eventID) })
}

func (m *Adapter) FetchEventSource(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	return onRoom(m, roomID, func(r *runner) ([]byte, error) { return r.FetchEventSource(ctx, roomID, eventID) })
}

func (m *Adapter) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	return onRoom(m, roomID, func(r *runner) ([]domain.Thread, error) { return r.ListThreads(ctx, roomID) })
}

func (m *Adapter) ThreadPage(
	ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int,
) (domain.TimelinePage, error) {
	return onRoom(m, roomID, func(r *runner) (domain.TimelinePage, error) { return r.ThreadPage(ctx, roomID, root, from, limit) })
}

func (m *Adapter) Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	return onRoom(m, roomID, func(r *runner) (domain.TimelinePage, error) { return r.Timeline(ctx, roomID, from, limit) })
}

func (m *Adapter) MessageHistory(
	ctx context.Context, roomID domain.RoomID, eventID domain.EventID,
) ([]domain.Revision, domain.Deletion, error) {
	r, err := m.runnerOf(string(roomID))
	if err != nil {
		return nil, domain.Deletion{}, err
	}
	return r.MessageHistory(ctx, roomID, eventID)
}

func (m *Adapter) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	return onRoom(m, roomID, func(r *runner) ([]byte, error) { return r.LoadImage(ctx, roomID, eventID) })
}

func (m *Adapter) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.MarkRead(ctx, roomID, eventID, private) })
	return err
}

func (m *Adapter) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, private bool) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) {
		return struct{}{}, r.MarkThreadRead(ctx, roomID, root, eventID, private)
	})
	return err
}

func (m *Adapter) MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.MarkRoomUnread(ctx, roomID, unread) })
	return err
}

func (m *Adapter) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	return onRoom(m, roomID, func(r *runner) ([]domain.Member, error) { return r.Members(ctx, roomID, limit) })
}

func (m *Adapter) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	return onRoom(m, roomID, func(r *runner) ([]domain.Member, error) { return r.RefreshMembers(ctx, roomID) })
}

func (m *Adapter) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	return onRoom(m, roomID, func(r *runner) ([]domain.Member, error) { return r.MentionCandidates(ctx, roomID, limit) })
}

func (m *Adapter) Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.Redact(ctx, roomID, eventID, reason) })
	return err
}

func (m *Adapter) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.Send(ctx, roomID, draft) })
	return err
}

func (m *Adapter) SendFile(ctx context.Context, roomID domain.RoomID, path, name, caption string) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.SendFile(ctx, roomID, path, name, caption) })
	return err
}

func (m *Adapter) SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.SendReaction(ctx, roomID, target, key) })
	return err
}

func (m *Adapter) SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.SendTyping(ctx, roomID, typing, timeout) })
	return err
}

func (m *Adapter) StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, on bool) error {
	_, err := onRoom(m, roomID, func(r *runner) (struct{}, error) { return struct{}{}, r.StarMessage(ctx, roomID, eventID, on) })
	return err
}
