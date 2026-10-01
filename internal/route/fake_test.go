package route

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// fake is one network's adapter: it records which rooms each call was about, answers
// lists with what it is given, and can be told to fail.
type fake struct {
	name    string
	account string
	me      []string
	rooms   []domain.Room
	fail    error // every call fails with this, when set
	// failMarking refuses MarkRoomsRead outright (nothing attempted).
	failMarking error
	// failEach and skipEach make MarkRoomsRead attempt every room but report the
	// first failEach as failed and the next skipEach as skipped.
	failEach, skipEach int
	// encrypted is what RoomEncryption answers per room (absent: false).
	encrypted map[domain.RoomID]bool

	mu      sync.Mutex
	touched []domain.RoomID // rooms any per-room call was about, in order
	marked  []domain.RoomID
	asked   []domain.RoomID // rooms RoomEncryption was asked about
	rewinds int

	messages  chan domain.Message
	activity  chan domain.Activity
	unread    chan domain.Unread
	reactions chan domain.ReactionUpdate
	stopped   bool
	// start blocks until ctx ends, then returns startErr; started is closed as it begins.
	startErr error
	started  chan struct{}
}

func newFake(name string) *fake {
	return &fake{
		name: name, account: "@" + name + ":x", me: []string{"@" + name + ":x"},
		messages: make(chan domain.Message, 8), activity: make(chan domain.Activity, 8),
		unread: make(chan domain.Unread, 8), reactions: make(chan domain.ReactionUpdate, 8),
		started: make(chan struct{}),
	}
}

// errFake is what a fake told to fail answers.
var errFake = errors.New("fake: refused")

func (f *fake) touch(room domain.RoomID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, room)
	return f.fail
}

func (f *fake) seen() []domain.RoomID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.RoomID(nil), f.touched...)
}

func (f *fake) Start(ctx context.Context) error {
	close(f.started)
	<-ctx.Done()
	return f.startErr
}

func (f *fake) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

func (f *fake) Messages() <-chan domain.Message         { return f.messages }
func (f *fake) Activity() <-chan domain.Activity        { return f.activity }
func (f *fake) Unread() <-chan domain.Unread            { return f.unread }
func (f *fake) Reactions() <-chan domain.ReactionUpdate { return f.reactions }
func (f *fake) Account() string                         { return f.account }
func (f *fake) Me() []string                            { return f.me }

func (f *fake) RewindSync(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rewinds++
	return f.fail
}

func (f *fake) Rooms(context.Context) ([]domain.Room, error)        { return f.rooms, f.fail }
func (f *fake) RefreshRooms(context.Context) ([]domain.Room, error) { return f.rooms, f.fail }
func (f *fake) CachedUnread(context.Context) ([]domain.Unread, error) {
	out := make([]domain.Unread, 0, len(f.rooms))
	for i := range f.rooms {
		out = append(out, domain.Unread{RoomID: f.rooms[i].ID})
	}
	return out, f.fail
}

func (f *fake) MarkRead(_ context.Context, room domain.RoomID, _ domain.EventID, _ bool) error {
	return f.touch(room)
}

func (f *fake) MarkRoomsRead(_ context.Context, rooms []domain.RoomID, _ bool) (domain.ReadResult, error) {
	if f.failMarking != nil {
		return domain.ReadResult{}, f.failMarking
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, rooms...)
	failed := min(f.failEach, len(rooms))
	skipped := min(f.skipEach, len(rooms)-failed)
	got := domain.ReadResult{Marked: len(rooms) - failed - skipped, Skipped: skipped, Failed: failed}
	if failed > 0 {
		got.FirstError = f.name + ": that room refused"
	}
	return got, nil
}

func (f *fake) MarkRoomUnread(_ context.Context, room domain.RoomID, _ bool) error {
	return f.touch(room)
}

func (f *fake) StarMessage(_ context.Context, room domain.RoomID, _ domain.EventID, _ bool) error {
	return f.touch(room)
}

func (f *fake) MarkSpam(_ context.Context, verdict domain.SpamVerdict) error {
	return f.touch(verdict.Room)
}

func (f *fake) CanonicalParent(_ context.Context, room domain.RoomID) (domain.SpaceID, error) {
	return "", f.touch(room)
}

func (f *fake) MessageHistory(_ context.Context, room domain.RoomID, _ domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	return []domain.Revision{{ID: domain.EventID(f.name)}}, domain.Deletion{}, f.touch(room)
}

func (f *fake) Timeline(_ context.Context, room domain.RoomID, _ string, _ int) (domain.TimelinePage, error) {
	return domain.TimelinePage{Next: f.name}, f.touch(room)
}

func (f *fake) FetchEvent(_ context.Context, room domain.RoomID, _ domain.EventID) (domain.Message, error) {
	return domain.Message{RoomID: room}, f.touch(room)
}

func (f *fake) Redact(_ context.Context, room domain.RoomID, _ domain.EventID, _ string) error {
	return f.touch(room)
}

func (f *fake) Send(_ context.Context, room domain.RoomID, _ domain.Draft) error {
	return f.touch(room)
}

func (f *fake) SendTyping(_ context.Context, room domain.RoomID, _ bool, _ time.Duration) error {
	return f.touch(room)
}

func (f *fake) SendFile(_ context.Context, room domain.RoomID, _, _ string) error {
	return f.touch(room)
}

func (f *fake) SendReaction(_ context.Context, room domain.RoomID, _ domain.EventID, _ string) error {
	return f.touch(room)
}

func (f *fake) LoadImage(_ context.Context, room domain.RoomID, _ domain.EventID) ([]byte, error) {
	return []byte(f.name), f.touch(room)
}

func (f *fake) Members(_ context.Context, room domain.RoomID, _ int) ([]domain.Member, error) {
	return []domain.Member{{UserID: f.name}}, f.touch(room)
}

func (f *fake) RefreshMembers(_ context.Context, room domain.RoomID) ([]domain.Member, error) {
	return []domain.Member{{UserID: f.name}}, f.touch(room)
}

func (f *fake) MentionCandidates(_ context.Context, room domain.RoomID, _ int) ([]domain.Member, error) {
	return []domain.Member{{UserID: f.name}}, f.touch(room)
}

func (f *fake) DirectCandidates(_ context.Context, limit int) ([]domain.Member, error) {
	out := make([]domain.Member, 0, limit)
	for i := range limit {
		out = append(out, domain.Member{UserID: f.name + string(rune('a'+i))})
	}
	return out, f.fail
}

func (f *fake) RoomEncryption(_ context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, rooms...)
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, room := range rooms {
		out[room] = f.encrypted[room]
	}
	return out, f.fail
}

// fakeMatrix is a fake that also has Matrix's own roles, recording what reached them.
type fakeMatrix struct {
	*fake
	matrixCalls []string
}

func newFakeMatrix() *fakeMatrix { return &fakeMatrix{fake: newFake("matrix")} }

func (m *fakeMatrix) only(call string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.matrixCalls = append(m.matrixCalls, call)
}

func (m *fakeMatrix) Spaces(context.Context) ([]domain.Space, error) {
	m.only("Spaces")
	return nil, nil
}
func (m *fakeMatrix) RefreshSpaces(context.Context) ([]domain.Space, error) {
	m.only("RefreshSpaces")
	return nil, nil
}

func (m *fakeMatrix) AddToSpace(context.Context, domain.SpaceID, domain.RoomID) error {
	m.only("AddToSpace")
	return nil
}

func (m *fakeMatrix) RemoveFromSpace(context.Context, domain.SpaceID, domain.RoomID) error {
	m.only("RemoveFromSpace")
	return nil
}

func (m *fakeMatrix) CachedInvites(context.Context) ([]domain.Room, error) {
	m.only("CachedInvites")
	return nil, nil
}
func (m *fakeMatrix) Invites() <-chan []domain.Room { return nil }
func (m *fakeMatrix) JoinRoom(context.Context, string, []string) (domain.RoomID, error) {
	m.only("JoinRoom")
	return "", nil
}

func (m *fakeMatrix) CreateRoom(context.Context, domain.NewRoom) (domain.RoomID, error) {
	m.only("CreateRoom")
	return "", nil
}

func (m *fakeMatrix) InviteUser(context.Context, domain.RoomID, string) error {
	m.only("InviteUser")
	return nil
}

func (m *fakeMatrix) KickUser(context.Context, domain.RoomID, string, string) error {
	m.only("KickUser")
	return nil
}

func (m *fakeMatrix) BanUser(context.Context, domain.RoomID, string, string) error {
	m.only("BanUser")
	return nil
}

func (m *fakeMatrix) UnbanUser(context.Context, domain.RoomID, string) error {
	m.only("UnbanUser")
	return nil
}

func (m *fakeMatrix) LeaveRoom(context.Context, domain.RoomID) error {
	m.only("LeaveRoom")
	return nil
}

func (m *fakeMatrix) ListThreads(context.Context, domain.RoomID) ([]domain.Thread, error) {
	m.only("ListThreads")
	return nil, nil
}

func (m *fakeMatrix) ThreadPage(context.Context, domain.RoomID, domain.EventID, string, int) (domain.TimelinePage, error) {
	m.only("ThreadPage")
	return domain.TimelinePage{}, nil
}

func (m *fakeMatrix) MarkThreadRead(context.Context, domain.RoomID, domain.EventID, domain.EventID, bool) error {
	m.only("MarkThreadRead")
	return nil
}

func (m *fakeMatrix) ThreadParticipant(context.Context, domain.RoomID, domain.EventID) bool {
	m.only("ThreadParticipant")
	return true
}

func (m *fakeMatrix) Verifications() <-chan domain.Verification { return nil }
func (m *fakeMatrix) StartVerification(context.Context) (string, error) {
	m.only("StartVerification")
	return "", nil
}

func (m *fakeMatrix) AcceptVerification(context.Context, string) error {
	m.only("AcceptVerification")
	return nil
}

func (m *fakeMatrix) ConfirmSAS(context.Context, string) error {
	m.only("ConfirmSAS")
	return nil
}

func (m *fakeMatrix) CancelVerification(context.Context, string) error {
	m.only("CancelVerification")
	return nil
}

func (m *fakeMatrix) RestoreKeyBackup(context.Context, string) (int, error) {
	m.only("RestoreKeyBackup")
	return 0, nil
}

func (m *fakeMatrix) ExportRoomKeys(context.Context, string) ([]byte, error) {
	m.only("ExportRoomKeys")
	return nil, nil
}

func (m *fakeMatrix) ImportRoomKeys(context.Context, string, []byte) (int, int, error) {
	m.only("ImportRoomKeys")
	return 0, 0, nil
}

func (m *fakeMatrix) BootstrapKeyBackup(context.Context, string) (domain.KeyBackup, error) {
	m.only("BootstrapKeyBackup")
	return domain.KeyBackup{}, nil
}
func (m *fakeMatrix) Attached() <-chan bool { return nil }

func (m *fakeMatrix) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.matrixCalls...)
}
