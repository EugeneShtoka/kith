package daemon

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// profileHeader names the [[profile]] a client runs, on every request; none is the
// first. kithd keeps nothing about it beyond the request.
const profileHeader = "Kith-Profile"

// profileKey carries the request's profile name to its handler.
type profileKey struct{}

// profileReach is what a call answers within a profile.
type profileReach int

const (
	// passes: the call answers about what it names, each room, space and message of
	// which a client naming a profile must hold (admitNamed); streams every room, of
	// which such a client is sent its profile's; or is about no room at all.
	passes profileReach = iota + 1
	// narrows: the call answers about many rooms; only the profile's reach the client.
	narrows
)

// reaches classifies every BackendService call by its method name. One missing is
// refused (profileGate), so a new call cannot reach a client without a decision.
var reaches = map[string]profileReach{
	// Lists, counts and searches over many rooms.
	"Rooms": narrows, "RefreshRooms": narrows, "Spaces": narrows, "RefreshSpaces": narrows, "Rail": narrows,
	"LastMessages": narrows, "TagVerdicts": narrows, "CachedUnread": narrows, "UnreadStream": narrows,
	"CachedInvites": narrows, "Invites": narrows, "SearchMessages": narrows, "SearchSenders": narrows,
	"RoomsWith": narrows, "MessagesFrom": narrows, "Drafts": narrows, "ScheduledMessages": narrows,
	"NotificationStream": narrows,

	// One room, space or message, named by the caller: a client naming a profile is
	// refused one the profile does not hold (admitNamed).
	"Present": passes, "MarkRead": passes, "MarkRoomsRead": passes, "MarkRoomUnread": passes, "SetRoomArchived": passes,
	"StarMessage": passes, "StarredIn": passes, "CanonicalParent": passes, "MarkTag": passes,
	"AddToSpace": passes, "RemoveFromSpace": passes, "FilingPlaces": passes, "FileRoom": passes, "CachedTimeline": passes, "Timeline": passes,
	"MessageHistory": passes, "FetchEvent": passes, "FetchEventSource": passes, "Send": passes,
	"Redact": passes, "SendFile": passes, "SendTyping": passes, "ListThreads": passes,
	"ThreadPage": passes, "MarkThreadRead": passes, "CachedReactions": passes, "SendReaction": passes,
	"VotePoll": passes, "PollVoters": passes, "CreatePoll": passes, "LoadImage": passes,
	"Members": passes, "RefreshMembers": passes, "MentionCandidates": passes, "SenderSlots": passes,
	"SaveSenderSlots": passes, "JoinRoom": passes, "KnockRoom": passes, "LeaveRoom": passes,
	"DeleteChat": passes, "InviteUser": passes, "KickUser": passes, "BanUser": passes,
	"UnbanUser": passes, "ModelTask": passes, "ReplaceDraft": passes, "RoomEncryption": passes,
	"MessagesAround": passes, "CompleteWord": passes, "ResolveLink": passes, "JoinLink": passes,
	"Follow": passes, "CreateRoom": passes, "Schedule": passes, "CancelScheduled": passes,
	"RetryScheduled": passes,

	// Streams of every room: a client naming a profile is sent its profile's rooms'.
	"Messages": passes, "Reactions": passes, "ActivityStream": passes,

	// About people, accounts, the daemon or the config, not rooms.
	"RetagVerdicts": passes, "PersonVerdicts": passes, "MarkPerson": passes, "Ignore": passes,
	"Unignore": passes, "Ignored": passes, "ReactionRefusals": passes, "RecordReactionRefusal": passes,
	"RecordEmoji": passes, "EmojiScores": passes, "DirectCandidates": passes, "PublicRooms": passes,
	"BlockPerson": passes, "UnblockPerson": passes, "BlockedPeople": passes, "SignOut": passes,
	"ChatOffers": passes, "Accounts": passes, "Seat": passes, "LoginNetworks": passes,
	"BeginLogin": passes, "AnswerLogin": passes, "CancelLogin": passes, "InsteadLogin": passes, "MessageCounts": passes,
	"FollowStream": passes, "Changed": passes, "Verifications": passes, "StartVerification": passes,
	"AcceptVerification": passes, "ConfirmSAS": passes, "CancelVerification": passes, "Status": passes,
	"Selves": passes, "Directory": passes, "ClearCache": passes, "RestoreKeyBackup": passes,
	"ExportRoomKeys": passes, "ImportRoomKeys": passes, "BootstrapKeyBackup": passes,
	"DetectLanguages": passes, "InstallDictionary": passes, "InstallFrequencies": passes,
	"DetectModel": passes, "InstallModel": passes, "CheckSpelling": passes, "LearnWord": passes,
	"AllowRareWord": passes, "SetDND": passes, "ClearDND": passes, "DND": passes,
	"RescanFilters": passes, "RescanPeople": passes, "ReloadConfig": passes, "CheckConfig": passes,
	"GetConfig": passes, "PreviewGroupings": passes, "ApplyGroupings": passes, "UpdateConfig": passes,
	"ConfigChanged": passes, "StartUpload": passes, "PutUpload": passes,
	"Devices": passes, "AddDevice": passes, "RemoveDevice": passes,
	"StartPairing": passes, "ConfirmPairing": passes, "Pair": passes,
	"JoinMesh": passes, "MeshStatus": passes,
}

// profileGate is the server's interceptor: it refuses a profile the config does not
// have, a call reaches does not classify, and a room, space or message a call names
// that the profile named does not hold; and carries the name to the handler.
type profileGate struct{ server *server }

func (g profileGate) admit(ctx context.Context, procedure string, header string) (context.Context, error) {
	if reaches[path.Base(procedure)] == 0 {
		return ctx, connect.NewError(connect.CodeUnimplemented,
			fmt.Errorf("daemon: %s is not classified for profiles", path.Base(procedure)))
	}
	p, err := g.server.Notifications.Profile(header)
	if err != nil {
		return ctx, rpcErr(err)
	}
	// The cache reads whom the profile hides by its name as the config spells it.
	return domain.WithProfile(context.WithValue(ctx, profileKey{}, header), p.Name), nil
}

func (g profileGate) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := g.admit(ctx, req.Spec().Procedure, req.Header().Get(profileHeader))
		if err != nil {
			return nil, err
		}
		if err := g.server.admitNamed(ctx, req.Any()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is unused: the gate is server-only.
func (g profileGate) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (g profileGate) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := g.admit(ctx, conn.Spec().Procedure, conn.RequestHeader().Get(profileHeader))
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// profileName is the client's interceptor: it names the profile on every request.
type profileName struct{ name func() string }

func (p profileName) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if name := p.name(); name != "" {
			req.Header().Set(profileHeader, name)
		}
		return next(ctx, req)
	}
}

func (p profileName) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		name, asked := ctx.Value(seatProfileKey{}).(string)
		if !asked {
			name = p.name()
		}
		if name != "" {
			conn.RequestHeader().Set(profileHeader, name)
		}
		return conn
	}
}

// WrapStreamingHandler is unused: the interceptor is client-only.
func (p profileName) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// Profile is the [[profile]] named, in any case; "" is the first.
func (n *Notifications) Profile(name string) (domain.Profile, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if strings.TrimSpace(name) == "" {
		return n.profiles[0], nil
	}
	for _, p := range n.profiles {
		if strings.EqualFold(p.Name, strings.TrimSpace(name)) {
			return p, nil
		}
	}
	return domain.Profile{}, fmt.Errorf("%w: %q", api.ErrUnknownProfile, name)
}

// requestProfile is the name of the [[profile]] the request runs in, as the config
// spells it; profileGate admitted it.
func (s *server) requestProfile(ctx context.Context) string {
	name, _ := ctx.Value(profileKey{}).(string)
	p, err := s.Notifications.Profile(name)
	if err != nil {
		return name // a reload took it away since: its own seat, which nothing else names
	}
	return p.Name
}

// within is the request's profile as a room test, read now (a stream asks per value, so
// a reload reaches it); nil when the profile holds every room. A profile a reload took
// away holds none.
func (n *Notifications) within(ctx context.Context) func(domain.RoomFacts) bool {
	name, _ := ctx.Value(profileKey{}).(string)
	p, err := n.Profile(name)
	switch {
	case err != nil:
		return func(domain.RoomFacts) bool { return false }
	case p.Everyone():
		return nil
	}
	return p.Holds
}

// profileRooms keeps the rooms the request's profile holds.
func (s *server) profileRooms(ctx context.Context, rooms []domain.Room) []domain.Room {
	within := s.Notifications.within(ctx)
	if within == nil {
		return rooms
	}
	return slices.DeleteFunc(slices.Clone(rooms), func(r domain.Room) bool {
		return !within(s.Notifications.scope.FactsOf(ctx, r))
	})
}

// profileHolds is the request's profile as a test of room IDs; nil holds every room.
func (s *server) profileHolds(ctx context.Context) func(domain.RoomID) bool {
	within := s.Notifications.within(ctx)
	if within == nil {
		return nil
	}
	return func(id domain.RoomID) bool { return within(s.Notifications.scope.Facts(ctx, id)) }
}

// profileKeeps keeps the items whose room the request's profile holds.
func profileKeeps[T any](holds func(domain.RoomID) bool, items []T, room func(T) domain.RoomID) []T {
	if holds == nil {
		return items
	}
	return slices.DeleteFunc(slices.Clone(items), func(item T) bool { return !holds(room(item)) })
}

// profileSet narrows the rooms a query reads to the request's profile: every room
// becomes the profile's rooms, a list keeps those the profile holds.
func (s *server) profileSet(ctx context.Context, set domain.RoomSet) (domain.RoomSet, error) {
	holds := s.profileHolds(ctx)
	if holds == nil {
		return set, nil
	}
	if !set.All {
		return domain.TheseRooms(profileKeeps(holds, set.IDs, func(id domain.RoomID) domain.RoomID { return id })), nil
	}
	rooms, err := s.Backend.Rooms(ctx)
	if err != nil {
		return domain.RoomSet{}, fmt.Errorf("read the profile's rooms: %w", err)
	}
	held := s.profileRooms(ctx, rooms)
	ids := make([]domain.RoomID, 0, len(held))
	for i := range held {
		ids = append(ids, held[i].ID)
	}
	return domain.TheseRooms(ids), nil
}

// profileSpaces keeps the spaces holding a room of the request's profile, directly or
// through a space they hold, each with only such children.
func (s *server) profileSpaces(ctx context.Context, spaces []domain.Space) []domain.Space {
	holds := s.profileHolds(ctx)
	if holds == nil {
		return spaces
	}
	isSpace := make(map[domain.RoomID]bool, len(spaces))
	for i := range spaces {
		isSpace[domain.RoomID(spaces[i].ID)] = true
	}
	// A space is kept once a child is a held room or a kept space; repeated until none
	// changes, so a cycle of spaces holding nothing keeps none.
	kept := make(map[domain.RoomID]bool, len(spaces))
	keeps := func(child domain.RoomID) bool {
		if isSpace[child] {
			return kept[child]
		}
		return holds(child)
	}
	for changed := true; changed; {
		changed = false
		for i := range spaces {
			id := domain.RoomID(spaces[i].ID)
			if !kept[id] && slices.ContainsFunc(spaces[i].Children, keeps) {
				kept[id], changed = true, true
			}
		}
	}
	out := make([]domain.Space, 0, len(spaces))
	for i := range spaces {
		if !kept[domain.RoomID(spaces[i].ID)] {
			continue
		}
		sp := spaces[i]
		sp.Children = slices.DeleteFunc(slices.Clone(sp.Children), func(c domain.RoomID) bool { return !keeps(c) })
		out = append(out, sp)
	}
	return out
}
