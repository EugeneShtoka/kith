package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	_ "time/tzdata" // the zone a client names, where kithd's machine has no zone files

	"connectrpc.com/connect"

	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/chatexport"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat a phone exported is imported from an upload in three steps, each reading it
// again (Uploads.read): the rooms it may be, who wrote it, then the import, which
// caches only what is older than the room's oldest cached message and uses it up.

// ChatCache is the cache's half of a chat import, which kithd's backend hands over.
type ChatCache interface {
	OldestMessage(ctx context.Context, roomID domain.RoomID) (domain.Message, bool, error)
	MessagesSaying(ctx context.Context, roomID domain.RoomID, bodies []string) ([]domain.Message, error)
	SaveMessages(ctx context.Context, roomID domain.RoomID, msgs []domain.Message) error
	CountMessages(ctx context.Context, roomID domain.RoomID) (int, error)
}

// importBatch is how many messages one write caches: a long chat is not one transaction.
const importBatch = 1000

// chatExport is upload id's export, its times read in zone ("" kithd's), and the cache
// it goes into.
func (s *server) chatExport(id, zone string) (chatexport.Export, ChatCache, error) {
	src, ok := s.Backend.(interface{ ChatCache() (ChatCache, bool) })
	var cache ChatCache
	if ok {
		cache, ok = src.ChatCache()
	}
	if !ok {
		return chatexport.Export{}, nil, connect.NewError(connect.CodeUnimplemented, errors.New("daemon: this daemon imports no chats"))
	}
	loc := time.Local
	if zone != "" {
		var err error
		if loc, err = time.LoadLocation(zone); err != nil {
			return chatexport.Export{}, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon: %w", err))
		}
	}
	path, name, err := s.Uploads.read(id)
	if err != nil {
		return chatexport.Export{}, nil, err
	}
	e, err := chatexport.Read(path, name, loc)
	if err != nil {
		return chatexport.Export{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return e, cache, nil
}

// oldestAt is when room's oldest cached message was sent; zero when it has none.
func oldestAt(ctx context.Context, cache ChatCache, room domain.RoomID) (time.Time, error) {
	m, _, err := cache.OldestMessage(ctx, room)
	if err != nil {
		return time.Time{}, fmt.Errorf("daemon: %w", err)
	}
	return m.Timestamp, nil // zero when there is none
}

// PreviewChatImport reads an uploaded export and offers the rooms it may be.
func (s *server) PreviewChatImport(ctx context.Context, r *req[v1.PreviewChatImportRequest]) (*resp[v1.PreviewChatImportResponse], error) {
	e, cache, err := s.chatExport(r.Msg.GetUploadId(), r.Msg.GetZone())
	if err != nil {
		return nil, err
	}
	saying, err := cache.MessagesSaying(ctx, "", e.Sample())
	if err != nil {
		return nil, rpcErr(err)
	}
	found := map[domain.RoomID]int{}
	type said struct {
		room domain.RoomID
		body string
	}
	counted := map[said]bool{} // a body cached twice in a room counts once
	for i := range saying {
		if key := (said{saying[i].RoomID, saying[i].Body}); !counted[key] {
			counted[key] = true
			found[key.room]++
		}
	}
	rooms, err := s.Backend.Rooms(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &v1.PreviewChatImportResponse{
		Title: e.Title, Messages: pc.ClampInt32(len(e.Lines)),
		FirstMs: e.Lines[0].At.UnixMilli(), LastMs: e.Lines[len(e.Lines)-1].At.UnixMilli(),
	}
	for _, room := range e.Rooms(s.profileRooms(ctx, rooms), found) {
		before, err := oldestAt(ctx, cache, room.ID)
		if err != nil {
			return nil, rpcErr(err)
		}
		out.Rooms = append(out.Rooms, &v1.ExportRoom{
			RoomId: string(room.ID), Found: pc.ClampInt32(room.Found), Named: room.Named, Adds: pc.ClampInt32(e.Adds(before)),
		})
	}
	return connect.NewResponse(out), nil
}

// importMembers is who a name in room may be: its members, and this person's accounts
// on its network when they are not among them.
func (s *server) importMembers(ctx context.Context, room domain.RoomID) ([]domain.Member, error) {
	members, err := s.Backend.Members(ctx, room, 0)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	selves, err := s.Backend.Selves(ctx)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	for _, id := range selves {
		mine := func(m domain.Member) bool { return m.UserID == id }
		if domain.NetworkOf(id) == domain.NetworkOf(string(room)) && !slices.ContainsFunc(members, mine) {
			members = append(members, domain.Member{UserID: id})
		}
	}
	return members, nil
}

// ChatImportSenders matches an export's names to the room's members.
func (s *server) ChatImportSenders(ctx context.Context, r *req[v1.ChatImportSendersRequest]) (*resp[v1.ChatImportSendersResponse], error) {
	e, cache, err := s.chatExport(r.Msg.GetUploadId(), r.Msg.GetZone())
	if err != nil {
		return nil, err
	}
	room := roomID(r.Msg.GetRoomId())
	members, err := s.importMembers(ctx, room)
	if err != nil {
		return nil, rpcErr(err)
	}
	cached, err := cache.MessagesSaying(ctx, room, e.Sample())
	if err != nil {
		return nil, rpcErr(err)
	}
	before, err := oldestAt(ctx, cache, room)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &v1.ChatImportSendersResponse{Members: pc.MembersToProto(members), Adds: pc.ClampInt32(e.Adds(before))}
	for _, sender := range e.Senders(members, cached) {
		out.Senders = append(out.Senders, &v1.ExportSender{
			Name: sender.Name, Messages: pc.ClampInt32(sender.Messages), Chosen: sender.Chosen, Likely: pc.MembersToProto(sender.Likely),
		})
	}
	return connect.NewResponse(out), nil
}

// ApplyChatImport caches an export's messages older than the room's oldest cached one,
// then uses the upload up. One that failed part way can run again.
func (s *server) ApplyChatImport(ctx context.Context, r *req[v1.ApplyChatImportRequest]) (*resp[v1.ApplyChatImportResponse], error) {
	e, cache, err := s.chatExport(r.Msg.GetUploadId(), r.Msg.GetZone())
	if err != nil {
		return nil, err
	}
	room := roomID(r.Msg.GetRoomId())
	members, err := s.importMembers(ctx, room)
	if err != nil {
		return nil, rpcErr(err)
	}
	before, err := oldestAt(ctx, cache, room)
	if err != nil {
		return nil, rpcErr(err)
	}
	had, err := cache.CountMessages(ctx, room)
	if err != nil {
		return nil, rpcErr(err)
	}
	msgs := e.Messages(room, before, r.Msg.GetSenders(), members)
	// Newest first, so what a failure leaves cached joins the room's oldest message and
	// a run again adds the rest, which is still older than everything cached.
	for end := len(msgs); end > 0; end -= importBatch {
		if serr := cache.SaveMessages(ctx, room, msgs[max(0, end-importBatch):end]); serr != nil {
			return nil, rpcErr(serr)
		}
	}
	has, err := cache.CountMessages(ctx, room)
	if err != nil {
		return nil, rpcErr(err)
	}
	if _, _, done, err := s.Uploads.take(r.Msg.GetUploadId()); err == nil {
		done()
	}
	// What the room keeps ([storage] keep) may have trimmed some at once.
	return connect.NewResponse(&v1.ApplyChatImportResponse{Added: pc.ClampInt32(has - had)}), nil
}
