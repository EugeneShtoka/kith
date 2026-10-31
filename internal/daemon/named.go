package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A client that names a profile (Kith-Profile) reaches only what its profile holds:
// every room, space and message a request names must be the profile's, or the call is
// refused before its handler runs. What a request names is read off its fields
// (namedFields), so one check covers every call. A client naming no profile (the
// terminal client without one, the CLI, kith-mcp) is refused nothing here.

// nameKind is what a request field names.
type nameKind int

const (
	// notARoom: text, a person, an account, a tag, a device, or a cursor the backend
	// issued for the room the request names.
	notARoom nameKind = iota + 1
	// aRoom: a room or a space, which must be one the profile's rail holds.
	aRoom
	// aMessage: a message, which every room the cache holds it in must be the profile's;
	// one the cache does not hold is looked for in the request's room alone.
	aMessage
	// aJoin: a room or an address to join. One kithd holds (a room, an invitation, a
	// space) must be the profile's; one it does not (a public room, an alias) is no
	// profile's yet.
	aJoin
	// aQueued: a queued message's ID, whose room must be one the profile's queue lists
	// (ScheduledMessages): the profile's, or a room kithd no longer holds that the
	// profile's entries name by its ID or network.
	aQueued
	// narrowed: rooms a query reads, which it narrows to the profile's itself (profileSet).
	narrowed
)

// namedFields classifies every string field of every request message, by full name; a
// nested message's fields count wherever it is used. TestEveryRequestFieldIsClassified
// holds it complete.
var namedFields = map[protoreflect.FullName]nameKind{
	// Rooms and spaces.
	"backend.v1.MarkReadRequest.room_id": aRoom, "backend.v1.MarkRoomsReadRequest.room_ids": aRoom,
	"backend.v1.MarkRoomUnreadRequest.room_id": aRoom, "backend.v1.SetRoomArchivedRequest.room_id": aRoom,
	"backend.v1.StarMessageRequest.room_id": aRoom, "backend.v1.StarredInRequest.room_id": aRoom,
	"backend.v1.CanonicalParentRequest.room_id": aRoom, "backend.v1.TagVerdict.room_id": aRoom,
	"backend.v1.AddToSpaceRequest.space_id": aRoom, "backend.v1.AddToSpaceRequest.room_id": aRoom,
	"backend.v1.RemoveFromSpaceRequest.space_id": aRoom, "backend.v1.RemoveFromSpaceRequest.room_id": aRoom,
	"backend.v1.FilingPlacesRequest.room_id": aRoom,
	"backend.v1.FileRoomRequest.room_id":     aRoom, "backend.v1.SpaceFiling.space_id": aRoom,
	"backend.v1.CachedTimelineRequest.room_id": aRoom, "backend.v1.TimelineRequest.room_id": aRoom,
	"backend.v1.MessageHistoryRequest.room_id": aRoom, "backend.v1.FetchEventRequest.room_id": aRoom,
	"backend.v1.FetchEventSourceRequest.room_id": aRoom, "backend.v1.SendRequest.room_id": aRoom,
	"backend.v1.Mention.room_id": aRoom, "backend.v1.RedactRequest.room_id": aRoom,
	"backend.v1.SendFileRequest.room_id": aRoom, "backend.v1.SendTypingRequest.room_id": aRoom,
	"backend.v1.ListThreadsRequest.room_id": aRoom, "backend.v1.ThreadPageRequest.room_id": aRoom,
	"backend.v1.MarkThreadReadRequest.room_id": aRoom, "backend.v1.CachedReactionsRequest.room_id": aRoom,
	"backend.v1.SendReactionRequest.room_id": aRoom, "backend.v1.VotePollRequest.room_id": aRoom,
	"backend.v1.PollVotersRequest.room_id": aRoom, "backend.v1.CreatePollRequest.room_id": aRoom,
	"backend.v1.RecordEmojiRequest.room_id": aRoom, "backend.v1.EmojiScoresRequest.room_id": aRoom,
	"backend.v1.EmojiScoresRequest.space_rooms": aRoom, "backend.v1.LoadImageRequest.room_id": aRoom,
	"backend.v1.MembersRequest.room_id": aRoom, "backend.v1.RefreshMembersRequest.room_id": aRoom,
	"backend.v1.MentionCandidatesRequest.room_id": aRoom, "backend.v1.SenderSlotsRequest.room_id": aRoom,
	"backend.v1.SaveSenderSlotsRequest.room_id": aRoom, "backend.v1.LeaveRoomRequest.room_id": aRoom,
	"backend.v1.DeleteChatRequest.room_id": aRoom, "backend.v1.SignOutRequest.space_id": aRoom,
	"backend.v1.CreateRoomRequest.parent": aRoom, "backend.v1.InviteUserRequest.room_id": aRoom,
	"backend.v1.KickUserRequest.room_id": aRoom, "backend.v1.BanUserRequest.room_id": aRoom,
	"backend.v1.UnbanUserRequest.room_id": aRoom, "backend.v1.ModelTaskRequest.room_id": aRoom,
	"backend.v1.StoredDraft.room_id": aRoom, "backend.v1.PresentRequest.open_room_id": aRoom,
	"backend.v1.RoomEncryptionRequest.room_ids": aRoom, "backend.v1.MessagesAroundRequest.room_id": aRoom,
	"backend.v1.MessagesFromRequest.before_room_id": aRoom, "backend.v1.CompleteWordRequest.room_ids": aRoom,
	"backend.v1.CompleteWordRequest.space_room_ids": aRoom, "backend.v1.RescanFiltersRequest.room_ids": aRoom,
	"backend.v1.ScheduledMessage.room_id": aRoom,

	// Messages: the one acted on, replied to, edited, a thread's root, a cursor's.
	"backend.v1.MarkReadRequest.event_id": aMessage, "backend.v1.StarMessageRequest.event_id": aMessage,
	"backend.v1.MessageHistoryRequest.event_id": aMessage, "backend.v1.FetchEventRequest.event_id": aMessage,
	"backend.v1.FetchEventSourceRequest.event_id": aMessage, "backend.v1.RedactRequest.event_id": aMessage,
	"backend.v1.Draft.reply_to": aMessage, "backend.v1.Draft.edits": aMessage, "backend.v1.Draft.thread_root": aMessage,
	"backend.v1.ThreadPageRequest.root": aMessage, "backend.v1.MarkThreadReadRequest.root": aMessage,
	"backend.v1.MarkThreadReadRequest.event_id": aMessage, "backend.v1.SendReactionRequest.target": aMessage,
	"backend.v1.VotePollRequest.event_id": aMessage, "backend.v1.PollVotersRequest.event_id": aMessage,
	"backend.v1.LoadImageRequest.event_id": aMessage, "backend.v1.ModelTaskRequest.reply_to": aMessage,
	"backend.v1.StoredDraft.reply_to": aMessage, "backend.v1.StoredDraft.editing": aMessage,
	"backend.v1.StoredDraft.thread_root": aMessage, "backend.v1.MessagesAroundRequest.event_id": aMessage,
	"backend.v1.MessagesFromRequest.before_event_id": aMessage, "backend.v1.ScheduledMessage.reply_to": aMessage,
	"backend.v1.ScheduledMessage.thread_root": aMessage,

	"backend.v1.JoinRoomRequest.room_id_or_alias": aJoin, "backend.v1.KnockRoomRequest.room_id_or_alias": aJoin,
	"backend.v1.CancelScheduledRequest.id": aQueued, "backend.v1.RetryScheduledRequest.id": aQueued,

	"backend.v1.SearchMessagesRequest.room_ids": narrowed, "backend.v1.SearchSendersRequest.room_ids": narrowed,
	"backend.v1.RoomsWithRequest.room_ids": narrowed, "backend.v1.MessagesFromRequest.room_ids": narrowed,

	// What a message says, and who it names: text, a person, an emoji, a reason.
	"backend.v1.Draft.body": notARoom, "backend.v1.Draft.txn_id": notARoom, "backend.v1.Mention.user_id": notARoom,
	"backend.v1.Mention.name": notARoom, "backend.v1.Mention.known": notARoom, "backend.v1.RedactRequest.reason": notARoom,
	"backend.v1.SendFileRequest.caption": notARoom, "backend.v1.SendFileRequest.name": notARoom,
	"backend.v1.StartUploadRequest.name": notARoom, "backend.v1.CreatePollRequest.question": notARoom,
	"backend.v1.CreatePollRequest.options": notARoom, "backend.v1.VotePollRequest.options": notARoom,
	"backend.v1.SendReactionRequest.key": notARoom, "backend.v1.RecordEmojiRequest.kind": notARoom,
	"backend.v1.RecordEmojiRequest.emoji": notARoom, "backend.v1.EmojiScoresRequest.kind": notARoom,
	"backend.v1.EmojiScoresRequest.scope": notARoom, "backend.v1.RecordReactionRefusalRequest.protocol": notARoom,
	"backend.v1.RecordReactionRefusalRequest.emoji": notARoom, "backend.v1.KickUserRequest.user_id": notARoom,
	"backend.v1.KickUserRequest.reason": notARoom, "backend.v1.BanUserRequest.user_id": notARoom,
	"backend.v1.BanUserRequest.reason": notARoom, "backend.v1.InviteUserRequest.user_id": notARoom,
	"backend.v1.UnbanUserRequest.user_id": notARoom, "backend.v1.KnockRoomRequest.reason": notARoom,
	"backend.v1.ModelTaskRequest.task": notARoom, "backend.v1.ModelTaskRequest.draft": notARoom,
	"backend.v1.ModelTaskRequest.instruction": notARoom, "backend.v1.ModelTaskRequest.span_said": notARoom,
	"backend.v1.StoredDraft.body": notARoom, "backend.v1.StoredDraft.edit_saved": notARoom,
	"backend.v1.StoredDraft.author": notARoom, "backend.v1.ScheduledMessage.body": notARoom,
	"backend.v1.ScheduledMessage.txn_id": notARoom, "backend.v1.ScheduledMessage.refused": notARoom,
	"backend.v1.ScheduledMessage.failing": notARoom, "backend.v1.CheckSpellingRequest.text": notARoom,
	"backend.v1.LearnWordRequest.word": notARoom, "backend.v1.AllowRareWordRequest.word": notARoom,
	"backend.v1.CompleteWordRequest.prefix": notARoom, "backend.v1.CompleteWordRequest.scope": notARoom,
	"backend.v1.CompleteWordRequest.sources": notARoom,
	// The schedule's own ID for a message it queues: kithd assigns it (Schedule).
	"backend.v1.ScheduledMessage.id": notARoom,
	// A page's cursor, issued by the backend for the room the request names.
	"backend.v1.TimelineRequest.from": notARoom, "backend.v1.ThreadPageRequest.from": notARoom,
	// An upload: a random ID only its uploader holds, sent into the room the request names.
	"backend.v1.PutUploadRequest.upload_id": notARoom, "backend.v1.SendFileRequest.upload_id": notARoom,
	// A sender's color slot in the room the request names.
	"backend.v1.SenderSlot.key": notARoom,
	// Search terms and people; the rooms searched are narrowed.
	"backend.v1.SearchMessagesRequest.query": notARoom, "backend.v1.SearchMessagesRequest.sender": notARoom,
	"backend.v1.SearchMessagesRequest.words": notARoom, "backend.v1.SearchMessagesRequest.senders": notARoom,
	"backend.v1.RoomsWithRequest.user_ids": notARoom, "backend.v1.MessagesFromRequest.senders": notARoom,
	"backend.v1.PersonIDs.ids": notARoom,
	// Tags, filters and people's verdicts: the config's, not a room's.
	"backend.v1.TagVerdict.tag": notARoom, "backend.v1.TagVerdict.filter": notARoom,
	"backend.v1.TagFiling.tag": notARoom, "backend.v1.RetagVerdictsRequest.from": notARoom,
	// The device's filing_spaces entries ("space:Name"), which FilingPlaces matches
	// against the profile's spaces itself.
	"backend.v1.FilingPlacesRequest.filing_spaces": notARoom,
	"backend.v1.RetagVerdictsRequest.to":           notARoom, "backend.v1.PersonVerdict.person": notARoom,
	"backend.v1.PersonVerdict.tag": notARoom, "backend.v1.PersonVerdict.filter": notARoom,
	"backend.v1.RescanPeopleRequest.people": notARoom, "backend.v1.IgnoreRequest.mxid": notARoom,
	"backend.v1.UnignoreRequest.mxid": notARoom, "backend.v1.BlockPersonRequest.user_id": notARoom,
	"backend.v1.UnblockPersonRequest.user_id": notARoom,
	// A room not made yet, and whom it invites; its parent is a room (above).
	"backend.v1.CreateRoomRequest.name": notARoom, "backend.v1.CreateRoomRequest.invite": notARoom,
	// Servers to join through, a directory's server and page.
	"backend.v1.JoinRoomRequest.via": notARoom, "backend.v1.KnockRoomRequest.via": notARoom,
	"backend.v1.PublicRoomsRequest.server": notARoom, "backend.v1.PublicRoomsRequest.since": notARoom,
	// Links: what one names is resolved by the backend; ResolveLink's answer is checked
	// (ResolveLink), and JoinLink joins whatever the link names, which only adds a room.
	"backend.v1.ResolveLinkRequest.uri": notARoom, "backend.v1.JoinLinkRequest.uri": notARoom,
	"backend.v1.FollowRequest.uri": notARoom,
	// An account (domain.RoomOwner), a network: every profile's.
	"backend.v1.PublicRoomsRequest.on": notARoom, "backend.v1.CreateRoomRequest.on": notARoom,
	"backend.v1.ResolveLinkRequest.on": notARoom, "backend.v1.JoinLinkRequest.on": notARoom,
	"backend.v1.StartVerificationRequest.on": notARoom, "backend.v1.RestoreKeyBackupRequest.on": notARoom,
	"backend.v1.ExportRoomKeysRequest.on": notARoom, "backend.v1.ImportRoomKeysRequest.on": notARoom,
	"backend.v1.BootstrapKeyBackupRequest.on": notARoom, "backend.v1.BeginLoginRequest.network": notARoom,
	"backend.v1.BeginLoginRequest.account": notARoom, "backend.v1.PreviewGroupingsRequest.network": notARoom,
	"backend.v1.PreviewGroupingsRequest.account": notARoom, "backend.v1.ApplyGroupingsRequest.network": notARoom,
	"backend.v1.ApplyGroupingsRequest.account": notARoom,
	// Secrets, logins, verifications, devices, the config, the dictionaries and DND.
	"backend.v1.RestoreKeyBackupRequest.secret": notARoom, "backend.v1.ExportRoomKeysRequest.passphrase": notARoom,
	"backend.v1.ImportRoomKeysRequest.passphrase": notARoom, "backend.v1.BootstrapKeyBackupRequest.password": notARoom,
	"backend.v1.AnswerLoginRequest.login": notARoom, "backend.v1.CancelLoginRequest.login": notARoom,
	"backend.v1.InsteadLoginRequest.login": notARoom, "backend.v1.AcceptVerificationRequest.txn_id": notARoom,
	// A login's answers by field key, and a grouping's choice by its name (maps).
	"backend.v1.AnswerLoginRequest.values": notARoom, "backend.v1.ApplyGroupingsRequest.choices": notARoom,
	"backend.v1.ConfirmSASRequest.txn_id": notARoom, "backend.v1.CancelVerificationRequest.txn_id": notARoom,
	"backend.v1.AddDeviceRequest.id": notARoom, "backend.v1.AddDeviceRequest.name": notARoom,
	"backend.v1.RemoveDeviceRequest.id": notARoom, "backend.v1.ConfirmPairingRequest.token": notARoom,
	"backend.v1.CheckConfigRequest.config": notARoom, "backend.v1.UpdateConfigRequest.base_revision": notARoom,
	"backend.v1.UpdateConfigRequest.config": notARoom, "backend.v1.InstallDictionaryRequest.tag": notARoom,
	"backend.v1.InstallFrequenciesRequest.tag": notARoom, "backend.v1.InstallModelRequest.tag": notARoom,
	"backend.v1.TempRule.name": notARoom, "backend.v1.TempRule.match": notARoom, "backend.v1.TempRule.sender": notARoom,
	"backend.v1.ClearDNDRequest.match": notARoom, "backend.v1.ClearDNDRequest.sender": notARoom,
	// The window speaking: its client ID for the seat and drafts.
	"backend.v1.ReplaceDraftRequest.client": notARoom, "backend.v1.PresentRequest.client": notARoom,
	// Streams' requests, which admitNamed does not read: none names a room.
	"backend.v1.SeatRequest.client": notARoom, "backend.v1.SeatHolder.tty": notARoom,
	"backend.v1.SeatHolder.host": notARoom, "backend.v1.MessagesRequest.since_epoch": notARoom,
	"backend.v1.FollowStreamRequest.client": notARoom, "backend.v1.StartPairingRequest.name": notARoom,
	"backend.v1.PairRequest.token": notARoom, "backend.v1.PairRequest.name": notARoom,
	"backend.v1.PairRequest.role": notARoom, "backend.v1.PairRequest.addresses": notARoom,
	"backend.v1.JoinMeshRequest.uri": notARoom, "backend.v1.JoinMeshRequest.name": notARoom,
}

// naming is one value a request names, and what it is.
type naming struct {
	kind nameKind
	id   string
}

// namesIn is every room, message, address and queued ID m names, in field order; a
// string or map field namedFields does not classify (as no room, for a map) fails it.
func namesIn(m protoreflect.Message) ([]naming, error) {
	var out []naming
	var err error
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case f.IsMap():
			if namedFields[f.FullName()] != notARoom {
				err = fmt.Errorf("daemon: %s is a map, which no profile check reads", f.FullName())
			}
		case f.Kind() == protoreflect.StringKind:
			kind := namedFields[f.FullName()]
			switch kind {
			case 0:
				err = fmt.Errorf("daemon: %s is not classified for profiles", f.FullName())
			case notARoom, narrowed:
			default:
				out = append(out, valuesOf(f, v, kind)...)
			}
		case f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind:
			var nested []protoreflect.Message
			if f.IsList() {
				for i := range v.List().Len() {
					nested = append(nested, v.List().Get(i).Message())
				}
			} else {
				nested = append(nested, v.Message())
			}
			for _, n := range nested {
				var found []naming
				if found, err = namesIn(n); err != nil {
					return false
				}
				out = append(out, found...)
			}
		}
		return err == nil
	})
	return out, err
}

func valuesOf(f protoreflect.FieldDescriptor, v protoreflect.Value, kind nameKind) []naming {
	if !f.IsList() {
		return []naming{{kind, v.String()}}
	}
	out := make([]naming, 0, v.List().Len())
	for i := range v.List().Len() {
		if id := v.List().Get(i).String(); id != "" {
			out = append(out, naming{kind, id})
		}
	}
	return out
}

// namedProfile reports whether the request's client named a profile (Kith-Profile).
func namedProfile(ctx context.Context) bool {
	name, _ := ctx.Value(profileKey{}).(string)
	return strings.TrimSpace(name) != ""
}

// admitNamed refuses a request naming a room, a space or a message its client's
// profile does not hold; a client naming no profile is refused nothing.
func (s *server) admitNamed(ctx context.Context, request any) error {
	m, ok := request.(proto.Message)
	if !ok || !namedProfile(ctx) {
		return nil
	}
	names, err := namesIn(m.ProtoReflect())
	if err != nil {
		return connect.NewError(connect.CodeUnimplemented, err)
	}
	if len(names) == 0 {
		return nil
	}
	r := s.reachOf(ctx)
	for _, n := range names {
		if err := r.admit(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

// reach is what the request's profile holds, read as one request asks.
type reach struct {
	s       *server
	profile string
	// within tests a room's facts; nil holds every room.
	within func(domain.RoomFacts) bool
	// every is kithd's rooms, invitations and spaces, read once when first needed.
	every *kithdRooms
}

func (s *server) reachOf(ctx context.Context) *reach {
	return &reach{s: s, profile: s.requestProfile(ctx), within: s.Notifications.within(ctx)}
}

// admit refuses one thing named that the profile does not hold.
func (r *reach) admit(ctx context.Context, n naming) error {
	switch n.kind {
	case aRoom:
		_, held, err := r.place(ctx, domain.RoomID(n.id))
		if err != nil || !held {
			return readOr(err, notInProfile("room", n.id, r.profile))
		}
	case aJoin:
		known, held, err := r.placeAddress(ctx, n.id)
		if err != nil || (known && !held) {
			return readOr(err, notInProfile("room", n.id, r.profile))
		}
	case aMessage:
		rooms, err := r.s.MessageLog.RoomsOf(ctx, domain.EventID(n.id))
		if err != nil {
			return rpcErr(fmt.Errorf("find the room of %s: %w", n.id, err))
		}
		for _, room := range rooms {
			if _, held, err := r.place(ctx, room); err != nil || !held {
				return readOr(err, notInProfile("message", n.id, r.profile))
			}
		}
	case aQueued:
		queued := r.s.Scheduler.List()
		at := slices.IndexFunc(queued, func(m domain.ScheduledMessage) bool { return m.ID == n.id })
		if at < 0 {
			return nil // nothing queued under it: the call reaches nothing
		}
		// Judged as ScheduledMessages lists it, so a message the queue shows can be acted on.
		if holds := r.s.profileHolds(ctx); holds != nil && !holds(queued[at].RoomID) {
			return notInProfile("queued message", n.id, r.profile)
		}
	case notARoom, narrowed: // namesIn names neither
	}
	return nil
}

// readOr is err, a read that failed, as the call's failure; else refused.
func readOr(err, refused error) error {
	if err != nil {
		return rpcErr(err)
	}
	return refused
}

// place says whether kithd holds id, as a room, an invitation or a space, and whether
// the profile does, as its rail judges it: a room or an invitation by its facts, a space
// by whether it reaches one of the profile's rooms.
func (r *reach) place(ctx context.Context, id domain.RoomID) (known, held bool, err error) {
	if facts, ok := r.s.Notifications.scope.Placed(ctx, id); ok {
		return true, r.holds(facts), nil
	}
	every, err := r.rooms(ctx)
	if err != nil {
		return false, false, err
	}
	if at := slices.IndexFunc(every.rooms, func(room domain.Room) bool { return room.ID == id }); at >= 0 {
		return true, r.holds(r.s.Notifications.scope.FactsOf(ctx, every.rooms[at])), nil
	}
	isSpace := func(sp domain.Space) bool { return domain.RoomID(sp.ID) == id }
	if !slices.ContainsFunc(every.spaces, isSpace) {
		return false, false, nil
	}
	return true, slices.ContainsFunc(r.s.profileSpaces(ctx, every.spaces), isSpace), nil
}

// placeAddress is place for an address to join: kithd's ID of a room, or the network's
// own (a Matrix room ID as Matrix writes it), which names each account's room of it.
func (r *reach) placeAddress(ctx context.Context, address string) (known, held bool, err error) {
	if known, held, err = r.place(ctx, domain.RoomID(address)); err != nil || known {
		return known, held, err
	}
	every, err := r.rooms(ctx)
	if err != nil {
		return false, false, err
	}
	held = true
	for _, id := range slices.Concat(idsOfRooms(every.rooms), idsOfSpaces(every.spaces)) {
		if domain.ParseID(string(id)).Native != address {
			continue
		}
		_, in, err := r.place(ctx, id)
		if err != nil {
			return false, false, err
		}
		known, held = true, held && in
	}
	return known, known && held, nil
}

func (r *reach) holds(facts domain.RoomFacts) bool { return r.within == nil || r.within(facts) }

func (r *reach) rooms(ctx context.Context) (kithdRooms, error) {
	if r.every == nil {
		every, err := r.s.allRooms(ctx)
		if err != nil {
			return kithdRooms{}, err
		}
		r.every = &every
	}
	return *r.every, nil
}

func idsOfRooms(rooms []domain.Room) []domain.RoomID {
	out := make([]domain.RoomID, len(rooms))
	for i := range rooms {
		out[i] = rooms[i].ID
	}
	return out
}

func idsOfSpaces(spaces []domain.Space) []domain.RoomID {
	out := make([]domain.RoomID, len(spaces))
	for i := range spaces {
		out[i] = domain.RoomID(spaces[i].ID)
	}
	return out
}

// streamHolds is the test a stream of every room keeps a room's values by for a client
// naming a profile, read per value so a reload reaches it; nil keeps every room, for a
// client naming none or a profile holding every room.
func (s *server) streamHolds(ctx context.Context) func(domain.RoomID) bool {
	if !namedProfile(ctx) {
		return nil
	}
	return s.profileHolds(ctx)
}
