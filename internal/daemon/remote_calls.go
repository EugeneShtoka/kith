package daemon

import (
	"context"
	"errors"
	"time"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// Unary api.Backend methods (plus daemon-only extras) over the socket. On error
// every method returns zero values: the response getters are nil-safe.

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

// GetConfig is the configuration the daemon runs with, and its file's revision.
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
// revision; api.ErrConfigMoved when the file changed since.
func (r *Remote) UpdateConfig(ctx context.Context, base string, cfg config.Config) (string, error) {
	res, err := call(ctx, "save the config", r.c.UpdateConfig,
		&v1.UpdateConfigRequest{BaseRevision: base, Config: config.Encode(cfg)})
	if err != nil {
		return "", err
	}
	return res.GetRevision(), nil
}

// PreviewGroupings is what copying each of an account's groupings (Telegram's folders)
// into the tag of its name would change, among that account's rooms.
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

// ApplyGroupings copies an account's groupings into tags as chosen (by grouping name;
// absent keeps the tag).
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

// roomIDs is IDs as rooms.
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

func (r *Remote) SpamRooms(ctx context.Context) ([]domain.SpamVerdict, error) {
	resp, err := call(ctx, "list spam rooms", r.c.SpamRooms, &v1.SpamRoomsRequest{})
	return pc.ProtoToSpamList(resp.GetRooms()), err
}

func (r *Remote) MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error {
	_, err := call(ctx, "mark spam", r.c.MarkSpam,
		&v1.MarkSpamRequest{Verdict: pc.SpamToProto([]domain.SpamVerdict{verdict})[0]})
	return err
}

// MarkRoomsRead sends only room IDs: each room's newest event is resolved from
// the daemon's cache.
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

func (r *Remote) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	_, err := call(ctx, "leave room", r.c.LeaveRoom, &v1.LeaveRoomRequest{RoomId: string(roomID)})
	return err
}

// CreateRoom keeps the in-process contract: a partial failure returns the new
// room's ID together with an error.
func (r *Remote) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	resp, err := call(ctx, "create room", r.c.CreateRoom, &v1.CreateRoomRequest{
		Name:      spec.Name,
		Space:     spec.Space,
		Encrypted: spec.Encrypted,
		Public:    spec.Public,
		Parent:    string(spec.Parent),
		Invite:    spec.Invite,
		Direct:    spec.Direct,
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

// SendFile passes the path, not the bytes: client and daemon are the same user on
// the same machine. The caller makes it absolute.
func (r *Remote) SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error {
	_, err := call(ctx, "send file", r.c.SendFile, &v1.SendFileRequest{RoomId: string(roomID), Path: path, Caption: caption})
	return err
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

func (r *Remote) VotePoll(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, options []string) error {
	_, err := call(ctx, "vote", r.c.VotePoll,
		&v1.VotePollRequest{RoomId: string(roomID), EventId: string(eventID), Options: options})
	return err
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

// ReplaceDraft names this window once it has the seat: from then on its writes are
// refused if another window takes it.
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

// RoomEncryption reports rooms the daemon did not name as unencrypted: the
// daemon already reported its unknowns as encrypted.
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

// LoadImage returns an attachment's bytes, from the daemon's media cache or
// downloaded (and decrypted) by it.
func (r *Remote) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	resp, err := call(ctx, "load image", r.c.LoadImage, &v1.LoadImageRequest{RoomId: string(roomID), EventId: string(eventID)})
	return resp.GetData(), err
}

// ── Verification ──

func (r *Remote) StartVerification(ctx context.Context) (string, error) {
	resp, err := call(ctx, "start verification", r.c.StartVerification, &v1.StartVerificationRequest{})
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

// Status reports what the daemon says about itself; a dial failure means no
// daemon is listening (see Ensure).
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

// Selves is every ID that is this person (api.Identity).
func (r *Remote) Selves(ctx context.Context) ([]string, error) {
	resp, err := call(ctx, "who am I", r.c.Selves, &v1.SelvesRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetIds(), nil
}

func (r *Remote) PhoneBook(ctx context.Context) (domain.PhoneBook, error) {
	resp, err := call(ctx, "read the phone book", r.c.PhoneBook, &v1.PhoneBookRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetNames(), nil
}

func (r *Remote) ClearCache(ctx context.Context) error {
	_, err := call(ctx, "clear cache", r.c.ClearCache, &v1.ClearCacheRequest{})
	return err
}

// RestoreKeyBackup sends the secret in the request body over the 0600 socket,
// never on argv.
func (r *Remote) RestoreKeyBackup(ctx context.Context, secret string) (int, error) {
	resp, err := call(ctx, "restore keys", r.c.RestoreKeyBackup, &v1.RestoreKeyBackupRequest{Secret: secret})
	return int(resp.GetKeys()), err
}

func (r *Remote) ExportRoomKeys(ctx context.Context, passphrase string) ([]byte, error) {
	resp, err := call(ctx, "export room keys", r.c.ExportRoomKeys, &v1.ExportRoomKeysRequest{Passphrase: passphrase})
	return resp.GetData(), err
}

func (r *Remote) ImportRoomKeys(ctx context.Context, passphrase string, data []byte) (int, int, error) {
	resp, err := call(ctx, "import room keys", r.c.ImportRoomKeys, &v1.ImportRoomKeysRequest{Passphrase: passphrase, Data: data})
	return int(resp.GetImported()), int(resp.GetTotal()), err
}

func (r *Remote) BootstrapKeyBackup(ctx context.Context, password string) (domain.KeyBackup, error) {
	resp, err := call(ctx, "bootstrap key backup", r.c.BootstrapKeyBackup, &v1.BootstrapKeyBackupRequest{Password: password})
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

// ReloadConfig asks the daemon to re-read its config, returning the parse error
// if it will not load.
func (r *Remote) ReloadConfig(ctx context.Context) error {
	_, err := call(ctx, "reload config", r.c.ReloadConfig, &v1.ReloadConfigRequest{})
	return err
}

// CheckConfig asks the daemon whether it would run with cfg, before it is written:
// the error is what to fix.
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

// Follow hands a URI to whichever client is attached, reporting whether one was.
func (r *Remote) Follow(ctx context.Context, uri string) (bool, error) {
	resp, err := call(ctx, "follow", r.c.Follow, &v1.FollowRequest{Uri: uri})
	return resp.GetDelivered(), err
}
