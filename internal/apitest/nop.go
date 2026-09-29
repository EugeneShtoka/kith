// Package apitest provides a do-nothing api.Backend for tests to embed and
// override selectively.
package apitest

import (
	"context"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Nop is a do-nothing api.Backend; its zero value is usable. Set a channel field
// to feed the matching stream. Methods return zero values and nil errors, except:
// MarkRoomsRead skips every room, JoinRoom echoes its argument, RoomEncryption
// calls every room encrypted, and ModelTask refuses as unconfigured.
type Nop struct {
	Msgs    chan domain.Message
	Verifs  chan domain.Verification
	Unreads chan domain.Unread
	Reacts  chan domain.ReactionUpdate
	Invs    chan []domain.Room
	Acts    chan domain.Activity
	Attach  chan bool
}

func (Nop) Rooms(context.Context) ([]domain.Room, error)                        { return nil, nil }
func (Nop) RefreshRooms(context.Context) ([]domain.Room, error)                 { return nil, nil }
func (Nop) MarkRead(context.Context, domain.RoomID, domain.EventID, bool) error { return nil }
func (Nop) MarkRoomsRead(_ context.Context, roomIDs []domain.RoomID, _ bool) (domain.ReadResult, error) {
	return domain.ReadResult{Skipped: len(roomIDs)}, nil
}
func (Nop) SendTyping(context.Context, domain.RoomID, bool, time.Duration) error       { return nil }
func (Nop) MarkRoomUnread(context.Context, domain.RoomID, bool) error                  { return nil }
func (Nop) StarMessage(context.Context, domain.RoomID, domain.EventID, bool) error     { return nil }
func (Nop) StarredIn(context.Context, domain.RoomID) ([]domain.EventID, error)         { return nil, nil }
func (Nop) SpamRooms(context.Context) ([]domain.SpamVerdict, error)                    { return nil, nil }
func (Nop) MarkSpam(context.Context, domain.SpamVerdict) error                         { return nil }
func (Nop) AddToSpace(_ context.Context, _ domain.SpaceID, _ domain.RoomID) error      { return nil }
func (Nop) CreateRoom(context.Context, domain.NewRoom) (domain.RoomID, error)          { return "", nil }
func (Nop) InviteUser(context.Context, domain.RoomID, string) error                    { return nil }
func (Nop) KickUser(context.Context, domain.RoomID, string, string) error              { return nil }
func (Nop) BanUser(context.Context, domain.RoomID, string, string) error               { return nil }
func (Nop) UnbanUser(context.Context, domain.RoomID, string) error                     { return nil }
func (Nop) RemoveFromSpace(_ context.Context, _ domain.SpaceID, _ domain.RoomID) error { return nil }
func (Nop) CanonicalParent(_ context.Context, _ domain.RoomID) (domain.SpaceID, error) {
	return "", nil
}
func (Nop) MessageHistory(context.Context, domain.RoomID, domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	return nil, domain.Deletion{}, nil
}
func (Nop) EmojiScores(context.Context, domain.EmojiKind, domain.RoomID, []domain.RoomID, string) (map[string]int, error) {
	return nil, nil
}
func (Nop) ReactionRefusals(context.Context) ([]domain.ReactionRefusal, error)      { return nil, nil }
func (Nop) RecordReactionRefusal(context.Context, string, string) error             { return nil }
func (Nop) Spaces(context.Context) ([]domain.Space, error)                          { return nil, nil }
func (Nop) RefreshSpaces(context.Context) ([]domain.Space, error)                   { return nil, nil }
func (Nop) CachedTimeline(context.Context, domain.RoomID) ([]domain.Message, error) { return nil, nil }
func (Nop) Timeline(context.Context, domain.RoomID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, nil
}
func (Nop) FetchEvent(context.Context, domain.RoomID, domain.EventID) (domain.Message, error) {
	return domain.Message{}, nil
}
func (Nop) Send(context.Context, domain.RoomID, domain.Draft) error       { return nil }
func (Nop) SendFile(context.Context, domain.RoomID, string, string) error { return nil }
func (Nop) CachedUnread(context.Context) ([]domain.Unread, error)         { return nil, nil }
func (n Nop) Unread() <-chan domain.Unread                                { return n.Unreads }
func (Nop) CachedReactions(context.Context, domain.RoomID) ([]domain.Reaction, error) {
	return nil, nil
}
func (n Nop) Reactions() <-chan domain.ReactionUpdate                                  { return n.Reacts }
func (Nop) SendReaction(context.Context, domain.RoomID, domain.EventID, string) error  { return nil }
func (Nop) RecordEmoji(context.Context, domain.EmojiKind, domain.RoomID, string) error { return nil }
func (Nop) LoadImage(context.Context, domain.RoomID, domain.EventID) ([]byte, error)   { return nil, nil }
func (Nop) Start(context.Context) error                                                { return nil }
func (Nop) Stop()                                                                      {}
func (n Nop) Messages() <-chan domain.Message                                          { return n.Msgs }
func (n Nop) Activity() <-chan domain.Activity                                         { return n.Acts }
func (n Nop) Attached() <-chan bool                                                    { return n.Attach }
func (n Nop) Verifications() <-chan domain.Verification                                { return n.Verifs }
func (Nop) StartVerification(context.Context) (string, error)                          { return "", nil }
func (Nop) AcceptVerification(context.Context, string) error                           { return nil }
func (Nop) ConfirmSAS(context.Context, string) error                                   { return nil }
func (Nop) CancelVerification(context.Context, string) error                           { return nil }

var _ api.Backend = Nop{}

func (Nop) CachedInvites(context.Context) ([]domain.Room, error) { return nil, nil }
func (n Nop) Invites() <-chan []domain.Room                      { return n.Invs }
func (Nop) JoinRoom(_ context.Context, roomIDOrAlias string, _ []string) (domain.RoomID, error) {
	return domain.RoomID(roomIDOrAlias), nil
}
func (Nop) LeaveRoom(context.Context, domain.RoomID) error                      { return nil }
func (Nop) ListThreads(context.Context, domain.RoomID) ([]domain.Thread, error) { return nil, nil }
func (Nop) ThreadPage(context.Context, domain.RoomID, domain.EventID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, nil
}
func (Nop) MarkThreadRead(context.Context, domain.RoomID, domain.EventID, domain.EventID, bool) error {
	return nil
}
func (Nop) SearchMessages(context.Context, domain.SearchRequest) ([]domain.SearchHit, error) {
	return nil, nil
}
func (Nop) CompleteWord(context.Context, domain.CompleteRequest) ([]domain.WordCandidate, error) {
	return nil, nil
}
func (Nop) ReplaceDraft(context.Context, domain.StoredDraft, domain.StoredDraft) (bool, error) {
	return true, nil
}
func (Nop) Drafts(context.Context) ([]domain.StoredDraft, error) { return nil, nil }
func (Nop) RoomEncryption(_ context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, roomID := range roomIDs {
		out[roomID] = true
	}
	return out, nil
}
func (Nop) RoomsWith(context.Context, []string, domain.RoomSet, int) ([]domain.Room, error) {
	return nil, nil
}
func (Nop) MessagesAround(context.Context, domain.RoomID, domain.EventID, int, int) ([]domain.Message, error) {
	return nil, nil
}
func (Nop) ModelTask(context.Context, domain.ModelRequest) (domain.ModelResult, error) {
	return domain.ModelResult{Refusal: domain.ModelRefusedOff}, nil
}
func (Nop) Members(context.Context, domain.RoomID, int) ([]domain.Member, error)   { return nil, nil }
func (Nop) RefreshMembers(context.Context, domain.RoomID) ([]domain.Member, error) { return nil, nil }
func (Nop) MentionCandidates(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return nil, nil
}
func (Nop) DirectCandidates(context.Context, int) ([]domain.Member, error)   { return nil, nil }
func (Nop) ExportRoomKeys(context.Context, string) ([]byte, error)           { return nil, nil }
func (Nop) ImportRoomKeys(context.Context, string, []byte) (int, int, error) { return 0, 0, nil }
func (Nop) SearchSenders(context.Context, domain.RoomSet, int) ([]domain.Member, error) {
	return nil, nil
}
func (Nop) Redact(context.Context, domain.RoomID, domain.EventID, string) error  { return nil }
func (Nop) LastMessages(context.Context) (map[domain.RoomID]time.Time, error)    { return nil, nil }
func (Nop) SenderSlots(context.Context, domain.RoomID) (map[string]int, error)   { return nil, nil }
func (Nop) SaveSenderSlots(context.Context, domain.RoomID, map[string]int) error { return nil }
func (Nop) ClearCache(context.Context) error                                     { return nil }
func (Nop) RestoreKeyBackup(context.Context, string) (int, error)                { return 0, nil }
func (Nop) BootstrapKeyBackup(context.Context, string) (domain.KeyBackup, error) {
	return domain.KeyBackup{}, nil
}
func (Nop) DetectLanguages(context.Context) (domain.SpellSuggestion, error) {
	return domain.SpellSuggestion{}, nil
}
func (Nop) InstallDictionary(context.Context, string) error  { return nil }
func (Nop) InstallFrequencies(context.Context, string) error { return nil }
func (Nop) DetectModel(context.Context) (domain.ModelSuggestion, error) {
	return domain.ModelSuggestion{}, nil
}
func (Nop) InstallModel(context.Context, string) error                          { return nil }
func (Nop) CheckSpelling(context.Context, string) ([]domain.Misspelling, error) { return nil, nil }
func (Nop) LearnWord(context.Context, string, bool) error                       { return nil }
func (Nop) AllowRareWord(context.Context, string) error                         { return nil }
