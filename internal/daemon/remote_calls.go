package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// Unary api.Backend methods (plus daemon-only extras) over the socket. On error every
// method returns zero values: the response getters are nil-safe.

// ── Room ──

func (r *Remote) Rooms(ctx context.Context) ([]domain.Room, error) {
	resp, err := call(ctx, "rooms", r.c.Rooms, &v1.RoomsRequest{})
	return pc.ProtoToRooms(resp.GetRooms()), err
}

func (r *Remote) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	resp, err := call(ctx, "refresh rooms", r.c.RefreshRooms, &v1.RefreshRoomsRequest{})
	return pc.ProtoToRooms(resp.GetRooms()), err
}

func (r *Remote) CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	resp, err := call(ctx, "canonical parent", r.c.CanonicalParent, &v1.CanonicalParentRequest{RoomId: string(roomID)})
	return domain.SpaceID(resp.GetSpaceId()), err
}

func (r *Remote) LastMessages(ctx context.Context) (map[domain.RoomID]time.Time, error) {
	resp, err := call(ctx, "last messages", r.c.LastMessages, &v1.LastMessagesRequest{})
	if err != nil {
		return nil, err
	}
	latest := make(map[domain.RoomID]time.Time, len(resp.GetAt()))
	for id, ms := range resp.GetAt() {
		if ms > 0 {
			latest[domain.RoomID(id)] = time.UnixMilli(ms)
		}
	}
	return latest, nil
}

func (r *Remote) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, private bool) error {
	_, err := call(ctx, "mark read", r.c.MarkRead,
		&v1.MarkReadRequest{RoomId: string(roomID), EventId: string(eventID), Private: private})
	return err
}

func (r *Remote) MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error {
	_, err := call(ctx, "mark unread", r.c.MarkRoomUnread, &v1.MarkRoomUnreadRequest{RoomId: string(roomID), Unread: unread})
	return err
}

// GetConfig is the daemon's configuration and its file's revision.
func (r *Remote) GetConfig(ctx context.Context) (config.Snapshot, error) {
	res, err := call(ctx, "read the config", r.c.GetConfig, &v1.GetConfigRequest{})
	if err != nil {
		return config.Snapshot{}, err
	}
	cfg, err := config.Decode(res.GetConfig())
	if err != nil {
		return config.Snapshot{}, callErr("read the config", err)
	}
	return config.Snapshot{Config: cfg, Revision: res.GetRevision()}, nil
}

// UpdateConfig has the daemon write cfg, made on revision base, and returns the new
// revision; api.ErrConfigMoved if the file changed since.
func (r *Remote) UpdateConfig(ctx context.Context, base string, cfg config.Config) (string, error) {
	res, err := call(ctx, "save the config", r.c.UpdateConfig,
		&v1.UpdateConfigRequest{BaseRevision: base, Config: config.Encode(cfg)})
	if err != nil {
		return "", err
	}
	return res.GetRevision(), nil
}

// PreviewGroupings is what copying each of an account's groupings (Telegram folders)
// into a same-named tag would change.
func (r *Remote) PreviewGroupings(ctx context.Context, network, account string) ([]domain.GroupingDiff, error) {
	res, err := call(ctx, "read the folders", r.c.PreviewGroupings, &v1.PreviewGroupingsRequest{Network: network, Account: account})
	if err != nil {
		return nil, err
	}
	out := make([]domain.GroupingDiff, 0, len(res.GetGroupings()))
	for _, g := range res.GetGroupings() {
		out = append(out, domain.GroupingDiff{
			Grouping: domain.Grouping{Name: g.GetName(), Rooms: roomIDs(g.GetRooms()), Left: g.GetLeft()},
			Exists:   g.GetExists(), Add: roomIDs(g.GetAdd()), Remove: roomIDs(g.GetRemove()),
		})
	}
	return out, nil
}

// ApplyGroupings copies an account's groupings into tags as chosen; an absent key
// keeps the tag.
func (r *Remote) ApplyGroupings(ctx context.Context, network, account string, choices map[string]domain.GroupingChoice) error {
	wire := make(map[string]v1.GroupingChoice, len(choices))
	for name, c := range choices {
		switch c {
		case domain.MergeIn:
			wire[name] = v1.GroupingChoice_GROUPING_CHOICE_MERGE_IN
		case domain.TakeNetworks:
			wire[name] = v1.GroupingChoice_GROUPING_CHOICE_TAKE_NETWORKS
		case domain.KeepTags:
		}
	}
	_, err := call(ctx, "copy the folders", r.c.ApplyGroupings, &v1.ApplyGroupingsRequest{Network: network, Account: account, Choices: wire})
	return err
}

func roomIDs(ids []string) []domain.RoomID {
	out := make([]domain.RoomID, len(ids))
	for i, id := range ids {
		out[i] = domain.RoomID(id)
	}
	return out
}

func (r *Remote) SetRoomArchived(ctx context.Context, roomID domain.RoomID, archived bool) error {
	_, err := call(ctx, "archive", r.c.SetRoomArchived, &v1.SetRoomArchivedRequest{RoomId: string(roomID), Archived: archived})
	return err
}

func (r *Remote) StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, starred bool) error {
	_, err := call(ctx, "star message", r.c.StarMessage,
		&v1.StarMessageRequest{RoomId: string(roomID), EventId: string(eventID), Starred: starred})
	return err
}

func (r *Remote) StarredIn(ctx context.Context, roomID domain.RoomID) ([]domain.EventID, error) {
	resp, err := call(ctx, "list starred", r.c.StarredIn, &v1.StarredInRequest{RoomId: string(roomID)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.EventID, 0, len(resp.GetEventIds()))
	for _, id := range resp.GetEventIds() {
		out = append(out, domain.EventID(id))
	}
	return out, nil
}

// TagVerdicts is every move and release, from the daemon's cache.
func (r *Remote) TagVerdicts(ctx context.Context) ([]domain.TagVerdict, error) {
	resp, err := call(ctx, "list tag verdicts", r.c.TagVerdicts, &v1.TagVerdictsRequest{})
	return pc.ProtoToVerdicts(resp.GetVerdicts()), err
}

// MarkTag records a move or a release on one room and tag.
func (r *Remote) MarkTag(ctx context.Context, verdict domain.TagVerdict) error {
	_, err := call(ctx, "mark tag", r.c.MarkTag, &v1.MarkTagRequest{Verdict: pc.VerdictToProto(verdict)})
	return err
}

// PersonVerdicts is every person's marks and releases, from the daemon's cache.
func (r *Remote) PersonVerdicts(ctx context.Context) ([]domain.PersonVerdict, error) {
	resp, err := call(ctx, "list person verdicts", r.c.PersonVerdicts, &v1.PersonVerdictsRequest{})
	return pc.ProtoToPersonVerdicts(resp.GetVerdicts()), err
}

// MarkPerson records a mark or a release on one person ID and tag.
func (r *Remote) MarkPerson(ctx context.Context, verdict domain.PersonVerdict) error {
	_, err := call(ctx, "mark person", r.c.MarkPerson, &v1.MarkPersonRequest{Verdict: pc.PersonVerdictToProto(verdict)})
	return err
}

// Ignore adds mxid to the Matrix account's m.ignored_user_list.
func (r *Remote) Ignore(ctx context.Context, mxid string) error {
	_, err := call(ctx, "ignore user", r.c.Ignore, &v1.IgnoreRequest{Mxid: mxid})
	return err
}

// Unignore drops mxid from the Matrix account's m.ignored_user_list.
func (r *Remote) Unignore(ctx context.Context, mxid string) error {
	_, err := call(ctx, "unignore user", r.c.Unignore, &v1.UnignoreRequest{Mxid: mxid})
	return err
}

// Ignored lists the MXIDs this account currently ignores on Matrix.
func (r *Remote) Ignored(ctx context.Context) ([]string, error) {
	res, err := call(ctx, "list ignored users", r.c.Ignored, &v1.IgnoredRequest{})
	if err != nil {
		return nil, err
	}
	return res.GetMxids(), nil
}

// RetagVerdicts moves one tag's verdicts to its new name, or drops them.
func (r *Remote) RetagVerdicts(ctx context.Context, from, to string) error {
	_, err := call(ctx, "move tag verdicts", r.c.RetagVerdicts, &v1.RetagVerdictsRequest{From: from, To: to})
	return err
}

// MarkRoomsRead sends only room IDs; the daemon resolves each newest event from its cache.
func (r *Remote) MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error) {
	resp, err := call(ctx, "mark rooms read", r.c.MarkRoomsRead,
		&v1.MarkRoomsReadRequest{RoomIds: pc.RoomIDsToStrings(roomIDs), Private: private})
	return domain.ReadResult{
		Marked:     int(resp.GetMarked()),
		Skipped:    int(resp.GetSkipped()),
		Failed:     int(resp.GetFailed()),
		FirstError: resp.GetFirstError(),
	}, err
}

// ── Space ──

func (r *Remote) Spaces(ctx context.Context) ([]domain.Space, error) {
	resp, err := call(ctx, "spaces", r.c.Spaces, &v1.SpacesRequest{})
	return pc.ProtoToSpaces(resp.GetSpaces()), err
}

func (r *Remote) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	resp, err := call(ctx, "refresh spaces", r.c.RefreshSpaces, &v1.RefreshSpacesRequest{})
	return pc.ProtoToSpaces(resp.GetSpaces()), err
}

func (r *Remote) AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	_, err := call(ctx, "add to space", r.c.AddToSpace, &v1.AddToSpaceRequest{SpaceId: string(spaceID), RoomId: string(roomID)})
	return err
}

func (r *Remote) RemoveFromSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	_, err := call(ctx, "remove from space", r.c.RemoveFromSpace,
		&v1.RemoveFromSpaceRequest{SpaceId: string(spaceID), RoomId: string(roomID)})
	return err
}

// ── Membership ──

func (r *Remote) CachedInvites(ctx context.Context) ([]domain.Room, error) {
	resp, err := call(ctx, "cached invites", r.c.CachedInvites, &v1.CachedInvitesRequest{})
	return pc.ProtoToRooms(resp.GetInvites()), err
}

func (r *Remote) JoinRoom(ctx context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error) {
	resp, err := call(ctx, "join room", r.c.JoinRoom, &v1.JoinRoomRequest{RoomIdOrAlias: roomIDOrAlias, Via: via})
	return domain.RoomID(resp.GetRoomId()), err
}

// DeleteChat deletes a chat that cannot be left.
func (r *Remote) DeleteChat(ctx context.Context, roomID domain.RoomID, forEveryone bool) error {
	_, err := call(ctx, "delete chat", r.c.DeleteChat, &v1.DeleteChatRequest{RoomId: string(roomID), ForEveryone: forEveryone})
	return err
}

// SignOut signs out the account whose own space it is.
func (r *Remote) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	_, err := call(ctx, "sign out", r.c.SignOut, &v1.SignOutRequest{SpaceId: string(space), Forget: forget})
	return err
}

func (r *Remote) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	_, err := call(ctx, "leave room", r.c.LeaveRoom, &v1.LeaveRoomRequest{RoomId: string(roomID)})
	return err
}

// Accounts asks the daemon for every connected account, the defaults marked.
func (r *Remote) Accounts(ctx context.Context) ([]domain.Account, error) {
	resp, err := call(ctx, "list the accounts", r.c.Accounts, &v1.AccountsRequest{})
	if err != nil {
		return nil, err
	}
	return pc.ProtoToAccounts(resp.GetAccounts()), nil
}

// ChatOffers asks the daemon what chats each account can make.
func (r *Remote) ChatOffers(ctx context.Context) ([]domain.ChatOffer, error) {
	resp, err := call(ctx, "list the chats accounts can make", r.c.ChatOffers, &v1.ChatOffersRequest{})
	if err != nil {
		return nil, err
	}
	return pc.ProtoToChatOffers(resp.GetOffers()), nil
}

// ResolveLink asks the daemon which chat a link names.
func (r *Remote) ResolveLink(ctx context.Context, uri string, on domain.RoomOwner) (domain.LinkTarget, error) {
	resp, err := call(ctx, "look up a link", r.c.ResolveLink, &v1.ResolveLinkRequest{Uri: uri, On: string(on)})
	if err != nil {
		return domain.LinkTarget{}, err
	}
	return pc.ProtoToLinkTarget(resp), nil
}

// JoinLink asks the daemon to join the chat a link names.
func (r *Remote) JoinLink(ctx context.Context, uri string, on domain.RoomOwner) (domain.RoomID, error) {
	resp, err := call(ctx, "join by a link", r.c.JoinLink, &v1.JoinLinkRequest{Uri: uri, On: string(on)})
	return domain.RoomID(resp.GetRoomId()), err
}

// CreateRoom keeps the in-process contract: a partial failure returns the new room's
// ID with an error.
func (r *Remote) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	resp, err := call(ctx, "create room", r.c.CreateRoom, &v1.CreateRoomRequest{
		Name:      spec.Name,
		Space:     spec.Space,
		Encrypted: spec.Encrypted,
		Public:    spec.Public,
		Parent:    string(spec.Parent),
		Invite:    spec.Invite,
		Direct:    spec.Direct,
		On:        string(spec.On),
		Kind:      v1.ChatKind(spec.Kind), //nolint:gosec // a ChatKind is a small enum
	})
	if err != nil {
		return "", err
	}
	if warning := resp.GetWarning(); warning != "" {
		return domain.RoomID(resp.GetRoomId()), errors.New(warning)
	}
	return domain.RoomID(resp.GetRoomId()), nil
}

func (r *Remote) InviteUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	_, err := call(ctx, "invite", r.c.InviteUser, &v1.InviteUserRequest{RoomId: string(roomID), UserId: userID})
	return err
}

func (r *Remote) KickUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	_, err := call(ctx, "remove", r.c.KickUser, &v1.KickUserRequest{RoomId: string(roomID), UserId: userID, Reason: reason})
	return err
}

func (r *Remote) BanUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	_, err := call(ctx, "ban", r.c.BanUser, &v1.BanUserRequest{RoomId: string(roomID), UserId: userID, Reason: reason})
	return err
}

func (r *Remote) UnbanUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	_, err := call(ctx, "unban", r.c.UnbanUser, &v1.UnbanUserRequest{RoomId: string(roomID), UserId: userID})
	return err
}

// ── Timeline ──

func (r *Remote) CachedTimeline(ctx context.Context, roomID domain.RoomID) ([]domain.Message, error) {
	resp, err := call(ctx, "cached timeline", r.c.CachedTimeline, &v1.CachedTimelineRequest{RoomId: string(roomID)})
	return pc.ProtoToMessages(resp.GetMessages()), err
}

func (r *Remote) Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	resp, err := call(ctx, "timeline", r.c.Timeline,
		&v1.TimelineRequest{RoomId: string(roomID), From: from, Limit: int64(limit)})
	return pc.ProtoToTimelinePage(resp.GetPage()), err
}

func (r *Remote) FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	resp, err := call(ctx, "fetch event", r.c.FetchEvent, &v1.FetchEventRequest{RoomId: string(roomID), EventId: string(eventID)})
	return pc.ProtoToMessage(resp.GetMessage()), err
}

func (r *Remote) FetchEventSource(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	resp, err := call(ctx, "fetch event source", r.c.FetchEventSource,
		&v1.FetchEventSourceRequest{RoomId: string(roomID), EventId: string(eventID)})
	return resp.GetSource(), err
}

// PublicRooms fetches one page of the Matrix public rooms directory.
func (r *Remote) PublicRooms(ctx context.Context, on domain.RoomOwner, server, since string) (domain.PublicRoomsPage, error) {
	resp, err := call(ctx, "list public rooms", r.c.PublicRooms,
		&v1.PublicRoomsRequest{Server: server, Since: since, On: string(on)})
	return domain.PublicRoomsPage{
		Rooms:         pc.ProtoToPublicRooms(resp.GetRooms()),
		NextSince:     resp.GetNextSince(),
		TotalEstimate: int(resp.GetTotalEstimate()),
	}, err
}

// KnockRoom asks to join a knock-only Matrix room.
func (r *Remote) KnockRoom(ctx context.Context, roomIDOrAlias, reason string, via []string) (domain.RoomID, error) {
	resp, err := call(ctx, "knock on room", r.c.KnockRoom,
		&v1.KnockRoomRequest{RoomIdOrAlias: roomIDOrAlias, Reason: reason, Via: via})
	return domain.RoomID(resp.GetRoomId()), err
}

// BlockPerson adds userID to its network's server-side block list.
func (r *Remote) BlockPerson(ctx context.Context, userID string) error {
	_, err := call(ctx, "block user", r.c.BlockPerson, &v1.BlockPersonRequest{UserId: userID})
	return err
}

// UnblockPerson drops userID from its network's block list.
func (r *Remote) UnblockPerson(ctx context.Context, userID string) error {
	_, err := call(ctx, "unblock user", r.c.UnblockPerson, &v1.UnblockPersonRequest{UserId: userID})
	return err
}

// BlockedPeople lists each network's blocked people, from the cache mirror.
func (r *Remote) BlockedPeople(ctx context.Context) (map[domain.Protocol][]string, error) {
	resp, err := call(ctx, "list blocked people", r.c.BlockedPeople, &v1.BlockedPeopleRequest{})
	if err != nil {
		return nil, err
	}
	out := map[domain.Protocol][]string{}
	for _, list := range resp.GetLists() {
		out[domain.Protocol(list.GetNetwork())] = list.GetUserIds()
	}
	return out, nil
}

func (r *Remote) MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	resp, err := call(ctx, "message history", r.c.MessageHistory,
		&v1.MessageHistoryRequest{RoomId: string(roomID), EventId: string(eventID)})
	return pc.ProtoToRevisions(resp.GetRevisions()), pc.ProtoToDeletion(resp.GetDeletion()), err
}

func (r *Remote) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	_, err := call(ctx, "send", r.c.Send, &v1.SendRequest{RoomId: string(roomID), Draft: pc.DraftToProto(draft)})
	return err
}

func (r *Remote) SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error {
	_, err := call(ctx, "send typing", r.c.SendTyping,
		&v1.SendTypingRequest{RoomId: string(roomID), Typing: typing, TimeoutMs: timeout.Milliseconds()})
	return err
}

func (r *Remote) Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error {
	_, err := call(ctx, "redact", r.c.Redact,
		&v1.RedactRequest{RoomId: string(roomID), EventId: string(eventID), Reason: reason})
	return err
}

// uploadChunk is how much of a file one PutUpload carries, all either side holds of it
// in memory at once.
const uploadChunk = 1 << 20

// chunkTries bounds the attempts at one chunk, and chunkPause is the wait before the
// next, longer each time.
const (
	chunkTries = 4
	chunkPause = 250 * time.Millisecond
)

// SendFile sends the bytes of the file at path, on this machine, which kithd may not
// share: StartUpload, then PutUpload chunk by chunk, each retried where it was cut off,
// then SendFile naming the upload.
func (r *Remote) SendFile(ctx context.Context, roomID domain.RoomID, path, name, caption string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("send file: %w", err)
	}
	defer func() { _ = f.Close() }() // read only
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("send file: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("send file: %s is a directory", filepath.Base(path))
	}
	size := info.Size()
	start, err := call(ctx, "upload", r.c.StartUpload, &v1.StartUploadRequest{Name: filepath.Base(path), Size: size})
	if err != nil {
		return err
	}
	id := start.GetUploadId()
	chunk := make([]byte, min(size, uploadChunk))
	for offset := int64(0); offset < size; {
		n := min(size-offset, uploadChunk)
		if _, rerr := f.ReadAt(chunk[:n], offset); rerr != nil {
			return fmt.Errorf("send file: read %s: %w", filepath.Base(path), rerr)
		}
		if perr := r.putChunk(ctx, id, offset, chunk[:n]); perr != nil {
			return perr
		}
		offset += n
	}
	_, err = call(ctx, "send file", r.c.SendFile,
		&v1.SendFileRequest{RoomId: string(roomID), UploadId: id, Name: name, Caption: caption})
	return err
}

// putChunk writes one chunk, again after a failure that may pass (a connection cut
// before or after kithd wrote it: writing it twice is harmless), not after a refusal.
func (r *Remote) putChunk(ctx context.Context, id string, offset int64, data []byte) error {
	var err error
	for try := range chunkTries {
		if try > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			case <-time.After(time.Duration(try) * chunkPause):
			}
		}
		_, err = call(ctx, "upload", r.c.PutUpload, &v1.PutUploadRequest{UploadId: id, Offset: offset, Data: data})
		if err == nil || ctx.Err() != nil || refused(err) {
			return err
		}
	}
	return err
}

// refused reports whether kithd turned a call down, which asking again will not change.
func refused(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodeFailedPrecondition,
		connect.CodeResourceExhausted, connect.CodeUnimplemented, connect.CodeCanceled, connect.CodeDeadlineExceeded:
		return true
	default:
		return false
	}
}

// ── Unread & reactions ──

func (r *Remote) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	resp, err := call(ctx, "cached unread", r.c.CachedUnread, &v1.CachedUnreadRequest{})
	return pc.ProtoToUnreads(resp.GetUnread()), err
}

func (r *Remote) CachedReactions(ctx context.Context, roomID domain.RoomID) ([]domain.Reaction, error) {
	resp, err := call(ctx, "cached reactions", r.c.CachedReactions, &v1.CachedReactionsRequest{RoomId: string(roomID)})
	return pc.ProtoToReactions(resp.GetReactions()), err
}

func (r *Remote) PollVoters(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.PollVoters, error) {
	resp, err := call(ctx, "poll voters", r.c.PollVoters, &v1.PollVotersRequest{RoomId: string(roomID), EventId: string(eventID)})
	return pc.ProtoToPollVoters(resp), err
}

func (r *Remote) VotePoll(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, options []string) error {
	_, err := call(ctx, "vote", r.c.VotePoll,
		&v1.VotePollRequest{RoomId: string(roomID), EventId: string(eventID), Options: options})
	return err
}

func (r *Remote) CreatePoll(ctx context.Context, roomID domain.RoomID, question string, options []string, multiple bool) (domain.EventID, error) {
	resp, err := call(ctx, "create poll", r.c.CreatePoll,
		&v1.CreatePollRequest{RoomId: string(roomID), Question: question, Options: options, Multiple: multiple})
	return domain.EventID(resp.GetEventId()), err
}

func (r *Remote) SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	_, err := call(ctx, "send reaction", r.c.SendReaction,
		&v1.SendReactionRequest{RoomId: string(roomID), Target: string(target), Key: key})
	return err
}

func (r *Remote) ReactionRefusals(ctx context.Context) ([]domain.ReactionRefusal, error) {
	resp, err := call(ctx, "reaction refusals", r.c.ReactionRefusals, &v1.ReactionRefusalsRequest{})
	if err != nil {
		return nil, err
	}
	return pc.ProtoToReactionRefusals(resp.GetRefusals()), nil
}

func (r *Remote) RecordReactionRefusal(ctx context.Context, protocol, emoji string) error {
	_, err := call(ctx, "record reaction refusal", r.c.RecordReactionRefusal,
		&v1.RecordReactionRefusalRequest{Protocol: protocol, Emoji: emoji})
	return err
}

// ── Search ──

func (r *Remote) SearchMessages(ctx context.Context, search domain.SearchRequest) ([]domain.SearchHit, error) {
	resp, err := call(ctx, "search messages", r.c.SearchMessages, pc.SearchRequestToProto(search))
	return pc.ProtoToSearchHits(resp.GetHits()), err
}

func (r *Remote) CompleteWord(ctx context.Context, req domain.CompleteRequest) ([]domain.WordCandidate, error) {
	resp, err := call(ctx, "complete word", r.c.CompleteWord, pc.CompleteRequestToProto(req))
	return pc.ProtoToWordCandidates(resp.GetCandidates()), err
}

func (r *Remote) ModelTask(ctx context.Context, req domain.ModelRequest) (domain.ModelResult, error) {
	resp, err := call(ctx, "model task", r.c.ModelTask, pc.ModelRequestToProto(req))
	return pc.ProtoToModelResult(resp), err
}

// ReplaceDraft names this window once it has the seat; its writes are then refused
// if another window takes it.
func (r *Remote) ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (bool, error) {
	request := &v1.ReplaceDraftRequest{Draft: pc.StoredDraftToProto(draft), Over: pc.StoredDraftToProto(over)}
	if r.seated.Load() {
		request.Client = r.client
	}
	resp, err := call(ctx, "replace draft", r.c.ReplaceDraft, request)
	return resp.GetSaved(), err
}

func (r *Remote) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	resp, err := call(ctx, "drafts", r.c.Drafts, &v1.DraftsRequest{})
	return pc.ProtoToStoredDrafts(resp.GetDrafts()), err
}

func (r *Remote) RoomsWith(ctx context.Context, userIDs []string, rooms domain.RoomSet, limit int) ([]domain.Room, error) {
	resp, err := call(ctx, "rooms with", r.c.RoomsWith, &v1.RoomsWithRequest{
		UserIds: userIDs, RoomIds: pc.RoomIDsToStrings(rooms.IDs), AllRooms: rooms.All, Limit: int64(limit),
	})
	return pc.ProtoToRooms(resp.GetRooms()), err
}

func (r *Remote) MessagesAround(ctx context.Context, roomID domain.RoomID, event domain.EventID, before, after int) ([]domain.Message, error) {
	resp, err := call(ctx, "messages around", r.c.MessagesAround, &v1.MessagesAroundRequest{
		RoomId: string(roomID), EventId: string(event), Before: int64(before), After: int64(after),
	})
	return pc.ProtoToMessages(resp.GetMessages()), err
}

func (r *Remote) MessagesFrom(ctx context.Context, said domain.SaidRequest) ([]domain.Message, error) {
	resp, err := call(ctx, "messages from", r.c.MessagesFrom, pc.SaidRequestToProto(said))
	return pc.ProtoToMessages(resp.GetMessages()), err
}

func (r *Remote) MessageCounts(ctx context.Context, people [][]string) ([]int, error) {
	req := &v1.MessageCountsRequest{People: make([]*v1.PersonIDs, len(people))}
	for i, ids := range people {
		req.People[i] = &v1.PersonIDs{Ids: ids}
	}
	resp, err := call(ctx, "count messages", r.c.MessageCounts, req)
	if err != nil {
		return nil, err
	}
	counts := make([]int, len(people))
	for i, n := range resp.GetCounts() {
		if i < len(counts) {
			counts[i] = int(n)
		}
	}
	return counts, nil
}

// RoomEncryption reports rooms the daemon did not name as unencrypted; the daemon
// already reports unknowns as encrypted.
func (r *Remote) RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	resp, err := call(ctx, "room encryption", r.c.RoomEncryption,
		&v1.RoomEncryptionRequest{RoomIds: pc.RoomIDsToStrings(roomIDs)})
	if err != nil {
		return nil, err
	}
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, id := range roomIDs {
		out[id] = false
	}
	for _, id := range resp.GetEncryptedRoomIds() {
		out[domain.RoomID(id)] = true
	}
	return out, nil
}

// ── Threads ──

func (r *Remote) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	resp, err := call(ctx, "list threads", r.c.ListThreads, &v1.ListThreadsRequest{RoomId: string(roomID)})
	return pc.ProtoToThreads(resp.GetThreads()), err
}

func (r *Remote) ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int) (domain.TimelinePage, error) {
	resp, err := call(ctx, "thread page", r.c.ThreadPage, &v1.ThreadPageRequest{
		RoomId: string(roomID), Root: string(root), From: from, Limit: int64(limit),
	})
	return pc.ProtoToTimelinePage(resp.GetPage()), err
}

func (r *Remote) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, private bool) error {
	_, err := call(ctx, "mark thread read", r.c.MarkThreadRead, &v1.MarkThreadReadRequest{
		RoomId: string(roomID), Root: string(root), EventId: string(eventID), Private: private,
	})
	return err
}

// ── Members, emoji, media ──

func (r *Remote) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	resp, err := call(ctx, "members", r.c.Members, &v1.MembersRequest{RoomId: string(roomID), Limit: int64(limit)})
	return pc.ProtoToMembers(resp.GetMembers()), err
}

func (r *Remote) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	resp, err := call(ctx, "refresh members", r.c.RefreshMembers, &v1.RefreshMembersRequest{RoomId: string(roomID)})
	return pc.ProtoToMembers(resp.GetMembers()), err
}

func (r *Remote) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	resp, err := call(ctx, "mention candidates", r.c.MentionCandidates,
		&v1.MentionCandidatesRequest{RoomId: string(roomID), Limit: int64(limit)})
	return pc.ProtoToMembers(resp.GetMembers()), err
}

func (r *Remote) SearchSenders(ctx context.Context, rooms domain.RoomSet, limit int) ([]domain.Member, error) {
	resp, err := call(ctx, "search senders", r.c.SearchSenders, &v1.SearchSendersRequest{
		RoomIds: pc.RoomIDsToStrings(rooms.IDs), AllRooms: rooms.All, Limit: int64(limit),
	})
	return pc.ProtoToMembers(resp.GetMembers()), err
}

func (r *Remote) DirectCandidates(ctx context.Context, limit int) ([]domain.Member, error) {
	resp, err := call(ctx, "direct candidates", r.c.DirectCandidates, &v1.DirectCandidatesRequest{Limit: int64(limit)})
	return pc.ProtoToMembers(resp.GetMembers()), err
}

func (r *Remote) SenderSlots(ctx context.Context, roomID domain.RoomID) (map[string]int, error) {
	resp, err := call(ctx, "sender slots", r.c.SenderSlots, &v1.SenderSlotsRequest{RoomId: string(roomID)})
	return pc.ProtoToSenderSlots(resp.GetSlots()), err
}

func (r *Remote) SaveSenderSlots(ctx context.Context, roomID domain.RoomID, slots map[string]int) error {
	_, err := call(ctx, "save sender slots", r.c.SaveSenderSlots,
		&v1.SaveSenderSlotsRequest{RoomId: string(roomID), Slots: pc.SenderSlotsToProto(slots)})
	return err
}

func (r *Remote) RecordEmoji(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, emoji string) error {
	_, err := call(ctx, "record emoji", r.c.RecordEmoji,
		&v1.RecordEmojiRequest{Kind: string(kind), RoomId: string(roomID), Emoji: emoji})
	return err
}

func (r *Remote) EmojiScores(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, spaceRooms []domain.RoomID, scope string) (map[string]int, error) {
	resp, err := call(ctx, "emoji scores", r.c.EmojiScores, &v1.EmojiScoresRequest{
		Kind: string(kind), RoomId: string(roomID), SpaceRooms: pc.RoomIDsToStrings(spaceRooms), Scope: scope,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(resp.GetScores()))
	for emoji, score := range resp.GetScores() {
		out[emoji] = int(score)
	}
	return out, nil
}

// LoadImage returns an attachment's bytes from the daemon's media cache or download.
func (r *Remote) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	resp, err := call(ctx, "load image", r.c.LoadImage, &v1.LoadImageRequest{RoomId: string(roomID), EventId: string(eventID)})
	return resp.GetData(), err
}

// ── Verification ──

func (r *Remote) StartVerification(ctx context.Context, on domain.RoomOwner) (string, error) {
	resp, err := call(ctx, "start verification", r.c.StartVerification, &v1.StartVerificationRequest{On: string(on)})
	return resp.GetTxnId(), err
}

func (r *Remote) AcceptVerification(ctx context.Context, txnID string) error {
	_, err := call(ctx, "accept verification", r.c.AcceptVerification, &v1.AcceptVerificationRequest{TxnId: txnID})
	return err
}

func (r *Remote) ConfirmSAS(ctx context.Context, txnID string) error {
	_, err := call(ctx, "confirm sas", r.c.ConfirmSAS, &v1.ConfirmSASRequest{TxnId: txnID})
	return err
}

func (r *Remote) CancelVerification(ctx context.Context, txnID string) error {
	_, err := call(ctx, "cancel verification", r.c.CancelVerification, &v1.CancelVerificationRequest{TxnId: txnID})
	return err
}

// ── Maintenance ──

// Status reports the daemon's state; a dial failure means no daemon is listening.
func (r *Remote) Status(ctx context.Context) (ready bool, syncedAt time.Time, lastErr string, err error) {
	resp, err := call(ctx, "status", r.c.Status, &v1.StatusRequest{})
	if err != nil {
		return false, time.Time{}, "", err
	}
	if ts := resp.GetSyncedAt(); ts != nil {
		syncedAt = ts.AsTime().Local()
	}
	return resp.GetReady(), syncedAt, resp.GetLastError(), nil
}

// Networks is each network account the daemon serves, logged in or not.
func (r *Remote) Networks(ctx context.Context) ([]NetworkStatus, error) {
	resp, err := call(ctx, "status", r.c.Status, &v1.StatusRequest{})
	if err != nil {
		return nil, err
	}
	rows := make([]NetworkStatus, 0, len(resp.GetNetworks()))
	for _, n := range resp.GetNetworks() {
		row := NetworkStatus{Network: n.GetNetwork(), Account: n.GetAccount(), Phase: protoToPhase(n.GetPhase()), Detail: n.GetDetail()}
		if ts := n.GetOnlineAt(); ts != nil {
			row.At = ts.AsTime().Local()
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// AccountStates is Networks as the networks report them (api.AccountStates).
func (r *Remote) AccountStates(ctx context.Context) ([]domain.AccountStatus, error) {
	rows, err := r.Networks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AccountStatus, 0, len(rows))
	for _, n := range rows {
		out = append(out, domain.AccountStatus{Network: domain.Protocol(n.Network), Account: n.Account, Phase: n.Phase, Detail: n.Detail})
	}
	return out, nil
}

// Selves is every ID that is this person (api.Identity).
func (r *Remote) Selves(ctx context.Context) ([]string, error) {
	resp, err := call(ctx, "who am I", r.c.Selves, &v1.SelvesRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetIds(), nil
}

func (r *Remote) Directory(ctx context.Context) (domain.Directory, error) {
	resp, err := call(ctx, "read the directory", r.c.Directory, &v1.DirectoryRequest{})
	if err != nil {
		return domain.Directory{}, err
	}
	return pc.ProtoToDirectory(resp), nil
}

func (r *Remote) ClearCache(ctx context.Context) error {
	_, err := call(ctx, "clear cache", r.c.ClearCache, &v1.ClearCacheRequest{})
	return err
}

// RestoreKeyBackup sends the secret in the request body over the 0600 socket, never
// on argv.
func (r *Remote) RestoreKeyBackup(ctx context.Context, on domain.RoomOwner, secret string) (int, error) {
	resp, err := call(ctx, "restore keys", r.c.RestoreKeyBackup, &v1.RestoreKeyBackupRequest{Secret: secret, On: string(on)})
	return int(resp.GetKeys()), err
}

func (r *Remote) ExportRoomKeys(ctx context.Context, on domain.RoomOwner, passphrase string) ([]byte, error) {
	resp, err := call(ctx, "export room keys", r.c.ExportRoomKeys, &v1.ExportRoomKeysRequest{Passphrase: passphrase, On: string(on)})
	return resp.GetData(), err
}

func (r *Remote) ImportRoomKeys(ctx context.Context, on domain.RoomOwner, passphrase string, data []byte) (int, int, error) {
	resp, err := call(ctx, "import room keys", r.c.ImportRoomKeys, &v1.ImportRoomKeysRequest{Passphrase: passphrase, Data: data, On: string(on)})
	return int(resp.GetImported()), int(resp.GetTotal()), err
}

func (r *Remote) BootstrapKeyBackup(ctx context.Context, on domain.RoomOwner, password string) (domain.KeyBackup, error) {
	resp, err := call(ctx, "bootstrap key backup", r.c.BootstrapKeyBackup, &v1.BootstrapKeyBackupRequest{Password: password, On: string(on)})
	return pc.ProtoToKeyBackup(resp), err
}

func (r *Remote) DetectLanguages(ctx context.Context) (domain.SpellSuggestion, error) {
	resp, err := call(ctx, "detect languages", r.c.DetectLanguages, &v1.DetectLanguagesRequest{})
	return pc.ProtoToSpellSuggestion(resp), err
}

func (r *Remote) InstallDictionary(ctx context.Context, tag string) error {
	_, err := call(ctx, "install dictionary", r.c.InstallDictionary, &v1.InstallDictionaryRequest{Tag: tag})
	return err
}

func (r *Remote) InstallFrequencies(ctx context.Context, tag string) error {
	_, err := call(ctx, "install frequencies", r.c.InstallFrequencies, &v1.InstallFrequenciesRequest{Tag: tag})
	return err
}

func (r *Remote) DetectModel(ctx context.Context) (domain.ModelSuggestion, error) {
	resp, err := call(ctx, "detect model", r.c.DetectModel, &v1.DetectModelRequest{})
	return pc.ProtoToModelSuggestion(resp), err
}

func (r *Remote) InstallModel(ctx context.Context, tag string) error {
	_, err := call(ctx, "install model", r.c.InstallModel, &v1.InstallModelRequest{Tag: tag})
	return err
}

// CheckSpelling carries api.ErrSpellUnavailable when there is no engine.
func (r *Remote) CheckSpelling(ctx context.Context, text string) ([]domain.Misspelling, error) {
	resp, err := call(ctx, "check spelling", r.c.CheckSpelling, &v1.CheckSpellingRequest{Text: text})
	if err != nil {
		return nil, err
	}
	return pc.ProtoToMisspellings(resp.GetMisspellings()), nil
}

func (r *Remote) AllowRareWord(ctx context.Context, word string) error {
	_, err := call(ctx, "allow rare word", r.c.AllowRareWord, &v1.AllowRareWordRequest{Word: word})
	return err
}

func (r *Remote) LearnWord(ctx context.Context, word string, forever bool) error {
	_, err := call(ctx, "learn word", r.c.LearnWord, &v1.LearnWordRequest{Word: word, Forever: forever})
	return err
}

// ── Notifications (daemon-only) ──

// SetDND puts one temporary rule in force and returns every rule in force.
func (r *Remote) SetDND(ctx context.Context, rule notify.Rule) (notify.Temps, error) {
	resp, err := call(ctx, "set do not disturb", r.c.SetDND, &v1.SetDNDRequest{Rule: pc.TempRuleToProto(rule)})
	return pc.TempRulesFromProto(resp.GetRules()), err
}

// ClearDND lifts the rule for this place and person, returning what is left.
func (r *Remote) ClearDND(ctx context.Context, match, sender string) (notify.Temps, error) {
	resp, err := call(ctx, "clear do not disturb", r.c.ClearDND, &v1.ClearDNDRequest{Match: match, Sender: sender})
	return pc.TempRulesFromProto(resp.GetRules()), err
}

// ClearAllDND lifts every temporary rule.
func (r *Remote) ClearAllDND(ctx context.Context) (notify.Temps, error) {
	resp, err := call(ctx, "clear do not disturb", r.c.ClearDND, &v1.ClearDNDRequest{All: true})
	return pc.TempRulesFromProto(resp.GetRules()), err
}

// DND reports the temporary rules in force.
func (r *Remote) DND(ctx context.Context) (notify.Temps, error) {
	resp, err := call(ctx, "read do not disturb", r.c.DND, &v1.DNDRequest{})
	return pc.TempRulesFromProto(resp.GetRules()), err
}

// RescanFilters judges cached messages since since by the filters; with apply it
// moves the rooms (only those in rooms, when given).
func (r *Remote) RescanFilters(ctx context.Context, since time.Time, apply bool, rooms []domain.RoomID) ([]domain.MoveCandidate, error) {
	req := &v1.RescanFiltersRequest{Apply: apply}
	if !since.IsZero() {
		req.SinceUnixMs = since.UnixMilli()
	}
	for _, id := range rooms {
		req.RoomIds = append(req.RoomIds, string(id))
	}
	resp, err := call(ctx, "rescan filters", r.c.RescanFilters, req)
	return pc.ProtoToCandidates(resp.GetRooms()), err
}

// RescanPeople judges the senders of cached messages since since by the filters'
// sender moves; with apply it marks them (only those in people, when given).
func (r *Remote) RescanPeople(ctx context.Context, since time.Time, apply bool, people []string) ([]domain.PersonCandidate, error) {
	req := &v1.RescanPeopleRequest{Apply: apply, People: people}
	if !since.IsZero() {
		req.SinceUnixMs = since.UnixMilli()
	}
	resp, err := call(ctx, "rescan people", r.c.RescanPeople, req)
	return pc.ProtoToPersonCandidates(resp.GetPeople()), err
}

// ReloadConfig asks the daemon to re-read its config, returning the parse error.
func (r *Remote) ReloadConfig(ctx context.Context) error {
	_, err := call(ctx, "reload config", r.c.ReloadConfig, &v1.ReloadConfigRequest{})
	return err
}

// CheckConfig asks the daemon whether it would run with cfg, before it is written.
func (r *Remote) CheckConfig(ctx context.Context, cfg config.Config) error {
	_, err := call(ctx, "check config", r.c.CheckConfig, &v1.CheckConfigRequest{Config: config.Encode(cfg)})
	return err
}

// ── Scheduled messages (daemon-only) ──

// Schedule queues a message for later, returning the handle that cancels it.
func (r *Remote) Schedule(ctx context.Context, msg domain.ScheduledMessage) (string, error) {
	resp, err := call(ctx, "schedule message", r.c.Schedule,
		&v1.ScheduleRequest{Message: pc.ScheduledToProto([]domain.ScheduledMessage{msg})[0]})
	return resp.GetId(), err
}

// ScheduledMessages is the whole queue, soonest first.
func (r *Remote) ScheduledMessages(ctx context.Context) ([]domain.ScheduledMessage, error) {
	resp, err := call(ctx, "list scheduled messages", r.c.ScheduledMessages, &v1.ScheduledMessagesRequest{})
	if err != nil {
		return nil, err
	}
	return pc.ProtoToScheduledList(resp.GetMessages()), nil
}

// CancelScheduled drops one pending message.
func (r *Remote) CancelScheduled(ctx context.Context, id string) error {
	_, err := call(ctx, "cancel scheduled message", r.c.CancelScheduled, &v1.CancelScheduledRequest{Id: id})
	return err
}

// RetryScheduled sends a refused or failing queued message now.
func (r *Remote) RetryScheduled(ctx context.Context, id string) error {
	_, err := call(ctx, "retry scheduled message", r.c.RetryScheduled, &v1.RetryScheduledRequest{Id: id})
	return err
}

// Follow hands a URI to whichever client is attached, reporting whether one was.
func (r *Remote) Follow(ctx context.Context, uri string) (bool, error) {
	resp, err := call(ctx, "follow", r.c.Follow, &v1.FollowRequest{Uri: uri})
	return resp.GetDelivered(), err
}

// Present tells the daemon the room this window has open ("" none) and that it was
// used just now, which Follow picks a window by.
func (r *Remote) Present(ctx context.Context, open domain.RoomID) error {
	_, err := call(ctx, "present", r.c.Present, &v1.PresentRequest{Client: r.client, OpenRoomId: string(open)})
	return err
}

// Devices is kithd's own device ID and the devices paired with it.
func (r *Remote) Devices(ctx context.Context) (self string, devices []domain.Device, err error) {
	resp, err := call(ctx, "list the paired devices", r.c.Devices, &v1.DevicesRequest{})
	if err != nil {
		return "", nil, err
	}
	return resp.GetSelf(), pc.ProtoToDevices(resp.GetDevices()), nil
}

// AddDevice pairs the device with the pasted ID, listing it as name.
func (r *Remote) AddDevice(ctx context.Context, id, name string) (domain.Device, error) {
	resp, err := call(ctx, "pair the device", r.c.AddDevice, &v1.AddDeviceRequest{Id: id, Name: name})
	return pc.ProtoToDevice(resp.GetDevice()), err
}

// RemoveDevice unpairs the device with the ID, cutting its live connections.
func (r *Remote) RemoveDevice(ctx context.Context, id string) error {
	_, err := call(ctx, "unpair the device", r.c.RemoveDevice, &v1.RemoveDeviceRequest{Id: id})
	return err
}

// StartPairing opens a pairing for a device listed as name ("" leaves it to the
// device): ticket is called with what the device needs, pending with the device that
// answers; it returns how the pairing ended. Canceling ctx ends it unconfirmed.
func (r *Remote) StartPairing(ctx context.Context, name string,
	ticket func(domain.PairingTicket), pending func(domain.PendingDevice),
) (domain.PairingOutcome, error) {
	st, err := r.c.StartPairing(ctx, connect.NewRequest(&v1.StartPairingRequest{Name: name}))
	if err != nil {
		return 0, callErr("start a pairing", err)
	}
	defer func() { _ = st.Close() }() // the outcome is read, or the stream failed
	for st.Receive() {
		switch ev := st.Msg().GetEvent().(type) {
		case *v1.StartPairingResponse_Ticket:
			ticket(pc.ProtoToPairingTicket(ev.Ticket))
		case *v1.StartPairingResponse_Pending:
			pending(pc.ProtoToPendingDevice(ev.Pending))
		case *v1.StartPairingResponse_Outcome:
			return pairingOutcome("start a pairing", ev.Outcome)
		}
	}
	return 0, callErr("start a pairing", cmpErr(st.Err(), errPairingCut))
}

// ConfirmPairing pairs the device pending under token, or refuses it.
func (r *Remote) ConfirmPairing(ctx context.Context, token string, accept bool) (domain.Device, error) {
	resp, err := call(ctx, "confirm the pairing", r.c.ConfirmPairing, &v1.ConfirmPairingRequest{Token: token, Accept: accept})
	return pc.ProtoToDevice(resp.GetDevice()), err
}

// Pair answers the pairing under token as a device listed as name: digits is called
// with the six kithd shows; it returns how the pairing ended.
func (r *Remote) Pair(ctx context.Context, token, name string, digits func(string)) (domain.PairingOutcome, error) {
	st, err := r.c.Pair(ctx, connect.NewRequest(&v1.PairRequest{Token: token, Name: name}))
	if err != nil {
		return 0, callErr("pair", err)
	}
	defer func() { _ = st.Close() }() // the outcome is read, or the stream failed
	for st.Receive() {
		switch ev := st.Msg().GetEvent().(type) {
		case *v1.PairResponse_Digits:
			digits(ev.Digits)
		case *v1.PairResponse_Outcome:
			return pairingOutcome("pair", ev.Outcome)
		}
	}
	return 0, callErr("pair", cmpErr(st.Err(), errPairingCut))
}

// errPairingCut is a pairing stream that ended without an outcome.
var errPairingCut = errors.New("the bridge ended the pairing without saying how")

// pairingOutcome reads an outcome frame; one this client does not know is an error,
// never read as confirmed.
func pairingOutcome(op string, pb v1.PairingOutcome) (domain.PairingOutcome, error) {
	o, ok := pc.ProtoToPairingOutcome(pb)
	if !ok {
		return 0, callErr(op, fmt.Errorf("the pairing ended in an outcome this client does not know (%v)", pb))
	}
	return o, nil
}
