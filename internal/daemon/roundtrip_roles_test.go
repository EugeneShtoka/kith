package daemon_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// recording answers with fixtures and remembers its arguments, so each method's wiring is
// checked from both ends of the socket.
type recording struct {
	apitest.Nop

	page     domain.TimelinePage
	spaces   []domain.Space
	unread   []domain.Unread
	reacts   []domain.Reaction
	members  []domain.Member
	hits     []domain.SearchHit
	words    []domain.WordCandidate
	emoji    []string
	image    []byte
	result   domain.ReadResult
	refusals []domain.ReactionRefusal
	scores   map[string]int
	slots    map[string]int
	spam     []domain.SpamVerdict
	spelling []domain.Misspelling
	model    domain.ModelSuggestion
	failWith error

	gotRoom     domain.RoomID
	gotEvent    domain.EventID
	gotDraft    domain.Draft
	gotFrom     string
	gotLimit    int
	gotQuery    string
	gotKey      string
	gotKind     domain.EmojiKind
	gotSearch   domain.SearchRequest
	gotComplete domain.CompleteRequest
	gotScope    string
	gotRooms    []domain.RoomID
	gotTxn      string
	gotAlias    string
	gotVia      []string
	gotEmoji    string
	gotSlots    map[string]int
	gotVerdicts []domain.SpamVerdict
	gotWord     string
}

func (r *recording) RefreshRooms(context.Context) ([]domain.Room, error) {
	return []domain.Room{{ID: "!fresh:example.org", Name: "Fresh"}}, r.failWith
}

func (r *recording) MarkRead(_ context.Context, roomID domain.RoomID, eventID domain.EventID, _ bool) error {
	r.gotRoom, r.gotEvent = roomID, eventID
	return r.failWith
}

func (r *recording) EmojiScores(_ context.Context, kind domain.EmojiKind, roomID domain.RoomID, spaceRooms []domain.RoomID, scope string) (map[string]int, error) {
	r.gotKind, r.gotRoom, r.gotRooms, r.gotScope = kind, roomID, spaceRooms, scope
	return r.scores, r.failWith
}

func (r *recording) ReactionRefusals(context.Context) ([]domain.ReactionRefusal, error) {
	return r.refusals, r.failWith
}

func (r *recording) RecordReactionRefusal(_ context.Context, protocol, emoji string) error {
	r.gotScope, r.gotEmoji = protocol, emoji
	return r.failWith
}

func (r *recording) MarkRoomsRead(_ context.Context, roomIDs []domain.RoomID, _ bool) (domain.ReadResult, error) {
	r.gotRooms = roomIDs
	return r.result, r.failWith
}

func (r *recording) Spaces(context.Context) ([]domain.Space, error) { return r.spaces, r.failWith }

func (r *recording) RefreshSpaces(context.Context) ([]domain.Space, error) {
	return r.spaces, r.failWith
}

func (r *recording) CachedTimeline(_ context.Context, roomID domain.RoomID) ([]domain.Message, error) {
	r.gotRoom = roomID
	return r.page.Messages, r.failWith
}

func (r *recording) Timeline(_ context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	r.gotRoom, r.gotFrom, r.gotLimit = roomID, from, limit
	return r.page, r.failWith
}

func (r *recording) FetchEvent(_ context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	r.gotRoom, r.gotEvent = roomID, eventID
	if len(r.page.Messages) == 0 {
		return domain.Message{}, r.failWith
	}
	return r.page.Messages[0], r.failWith
}

func (r *recording) Send(_ context.Context, roomID domain.RoomID, draft domain.Draft) error {
	r.gotRoom, r.gotDraft = roomID, draft
	return r.failWith
}

func (r *recording) CachedUnread(context.Context) ([]domain.Unread, error) {
	return r.unread, r.failWith
}

func (r *recording) CachedReactions(_ context.Context, roomID domain.RoomID) ([]domain.Reaction, error) {
	r.gotRoom = roomID
	return r.reacts, r.failWith
}

func (r *recording) SendReaction(_ context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	r.gotRoom, r.gotEvent, r.gotKey = roomID, target, key
	return r.failWith
}

func (r *recording) Members(_ context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	r.gotRoom, r.gotLimit = roomID, limit
	return r.members, r.failWith
}

func (r *recording) RefreshMembers(_ context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	r.gotRoom = roomID
	return r.members, r.failWith
}

func (r *recording) MentionCandidates(_ context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	r.gotRoom, r.gotLimit = roomID, limit
	return r.members, r.failWith
}

func (r *recording) RecordEmoji(_ context.Context, kind domain.EmojiKind, roomID domain.RoomID, emoji string) error {
	r.gotKind, r.gotRoom, r.gotEmoji = kind, roomID, emoji
	return r.failWith
}

func (r *recording) LoadImage(_ context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	r.gotRoom, r.gotEvent = roomID, eventID
	return r.image, r.failWith
}

func (r *recording) SearchMessages(_ context.Context, req domain.SearchRequest) ([]domain.SearchHit, error) {
	r.gotQuery, r.gotLimit, r.gotSearch = req.Filter.Terms, req.Limit, req
	r.gotRoom = ""
	if len(req.Rooms.IDs) == 1 {
		r.gotRoom = req.Rooms.IDs[0]
	}
	return r.hits, r.failWith
}

func (r *recording) CompleteWord(_ context.Context, req domain.CompleteRequest) ([]domain.WordCandidate, error) {
	r.gotComplete = req
	return r.words, r.failWith
}

func (r *recording) CachedInvites(context.Context) ([]domain.Room, error) {
	return []domain.Room{{ID: "!invite:example.org", Membership: domain.MembershipInvite}}, r.failWith
}

func (r *recording) JoinRoom(_ context.Context, roomIDOrAlias string, via []string) (domain.RoomID, error) {
	r.gotAlias, r.gotVia = roomIDOrAlias, via
	return domain.RoomID("!joined:example.org"), r.failWith
}

func (r *recording) LeaveRoom(_ context.Context, roomID domain.RoomID) error {
	r.gotRoom = roomID
	return r.failWith
}

func (r *recording) AcceptVerification(_ context.Context, txnID string) error {
	r.gotTxn = txnID
	return r.failWith
}

func (r *recording) ConfirmSAS(_ context.Context, txnID string) error {
	r.gotTxn = txnID
	return r.failWith
}

func (r *recording) CancelVerification(_ context.Context, txnID string) error {
	r.gotTxn = txnID
	return r.failWith
}

func (r *recording) SenderSlots(_ context.Context, roomID domain.RoomID) (map[string]int, error) {
	r.gotRoom = roomID
	return r.slots, r.failWith
}

func (r *recording) SaveSenderSlots(_ context.Context, roomID domain.RoomID, slots map[string]int) error {
	r.gotRoom, r.gotSlots = roomID, slots
	return r.failWith
}

func (r *recording) SpamRooms(context.Context) ([]domain.SpamVerdict, error) {
	return r.spam, r.failWith
}

func (r *recording) MarkSpam(_ context.Context, verdict domain.SpamVerdict) error {
	r.gotVerdicts = append(r.gotVerdicts, verdict)
	return r.failWith
}

func (r *recording) CheckSpelling(context.Context, string) ([]domain.Misspelling, error) {
	return r.spelling, r.failWith
}

func (r *recording) AllowRareWord(_ context.Context, word string) error {
	r.gotWord = word
	return r.failWith
}

func (r *recording) DetectModel(context.Context) (domain.ModelSuggestion, error) {
	return r.model, r.failWith
}

func (r *recording) InstallModel(_ context.Context, tag string) error {
	r.gotWord = tag
	if tag == "no-such-model" {
		return errors.New("no such model")
	}
	return r.failWith
}

var _ api.Backend = (*recording)(nil)

// stamp is a fixed instant for fixtures.
func stamp() time.Time { return time.UnixMilli(time.Now().UnixMilli()) }

// Reads: a room refresh, the space hierarchy, cached unread and invites all come
// back field-for-field.
func TestReadRolesRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{
		spaces: []domain.Space{{ID: "#work:example.org", Name: "Work", Children: []domain.RoomID{"!a:example.org", "!b:example.org"}}},
		unread: []domain.Unread{{RoomID: "!a:example.org", Notifications: 3, Highlights: 1, ReadEvent: "$seen:example.org"}},
	}
	r, _ := attach(t, b)
	ctx := context.Background()

	rooms, err := r.RefreshRooms(ctx)
	if err != nil || len(rooms) != 1 || rooms[0].Name != "Fresh" {
		t.Errorf("RefreshRooms() = %+v, %v; want the daemon's fresh list", rooms, err)
	}
	spaces, err := r.Spaces(ctx)
	if err != nil {
		t.Fatalf("Spaces() error = %v", err)
	}
	if !reflect.DeepEqual(spaces, b.spaces) {
		t.Errorf("Spaces() = %+v, want %+v", spaces, b.spaces)
	}
	refreshed, err := r.RefreshSpaces(ctx)
	if err != nil || !reflect.DeepEqual(refreshed, b.spaces) {
		t.Errorf("RefreshSpaces() = %+v, %v; want %+v", refreshed, err, b.spaces)
	}
	unread, err := r.CachedUnread(ctx)
	if err != nil {
		t.Fatalf("CachedUnread() error = %v", err)
	}
	if !reflect.DeepEqual(unread, b.unread) {
		t.Errorf("CachedUnread() = %+v, want %+v", unread, b.unread)
	}
	invites, err := r.CachedInvites(ctx)
	if err != nil || len(invites) != 1 || invites[0].Membership != domain.MembershipInvite {
		t.Errorf("CachedInvites() = %+v, %v; want one pending invite", invites, err)
	}
}

// MarkRoomsRead carries a list out and three counts back; a swapped count is invisible
// to the compiler.
func TestMarkRoomsReadRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{result: domain.ReadResult{Marked: 5, Skipped: 2, Failed: 1}}
	r, _ := attach(t, b)

	asked := []domain.RoomID{"!a:example.org", "!b:example.org", "!c:example.org"}
	got, err := r.MarkRoomsRead(context.Background(), asked, false)
	if err != nil {
		t.Fatalf("MarkRoomsRead() error = %v", err)
	}
	if !reflect.DeepEqual(got, b.result) {
		t.Errorf("MarkRoomsRead() = %+v, want %+v", got, b.result)
	}
	if !reflect.DeepEqual(b.gotRooms, asked) {
		t.Errorf("backend saw rooms %+v, want %+v", b.gotRooms, asked)
	}
}

// A timeline page keeps its messages, the reactions discovered alongside them, and its
// pagination token.
func TestTimelineRoundTrip(t *testing.T) {
	t.Parallel()

	at := stamp()
	b := &recording{page: domain.TimelinePage{
		Messages: []domain.Message{
			{ID: "$one:example.org", RoomID: "!a:example.org", Sender: "@ada:example.org", Body: "older", Timestamp: at},
			{ID: "$two:example.org", RoomID: "!a:example.org", Sender: "@ada:example.org", Body: "newer", Timestamp: at, Edited: true},
		},
		Reactions: []domain.Reaction{{ID: "$r:example.org", RoomID: "!a:example.org", Target: "$one:example.org", Sender: "@grace:example.org", Key: "👍"}},
		Next:      "t42-back",
	}}
	r, _ := attach(t, b)

	got, err := r.Timeline(context.Background(), "!a:example.org", "t7", 20)
	if err != nil {
		t.Fatalf("Timeline() error = %v", err)
	}
	if !reflect.DeepEqual(got, b.page) {
		t.Errorf("Timeline() = %+v, want %+v", got, b.page)
	}
	if b.gotRoom != "!a:example.org" || b.gotFrom != "t7" || b.gotLimit != 20 {
		t.Errorf("backend saw (%q, %q, %d), want (!a:example.org, t7, 20)", b.gotRoom, b.gotFrom, b.gotLimit)
	}

	cached, err := r.CachedTimeline(context.Background(), "!a:example.org")
	if err != nil || !reflect.DeepEqual(cached, b.page.Messages) {
		t.Errorf("CachedTimeline() = %+v, %v; want the page's messages", cached, err)
	}

	// One event by ID: what a thread older than the cached window asks for.
	one, err := r.FetchEvent(context.Background(), "!a:example.org", "$one:example.org")
	if err != nil {
		t.Fatalf("FetchEvent() error = %v", err)
	}
	if !reflect.DeepEqual(one, b.page.Messages[0]) {
		t.Errorf("FetchEvent() = %+v, want %+v", one, b.page.Messages[0])
	}
	if b.gotEvent != "$one:example.org" {
		t.Errorf("backend saw event %q, want $one:example.org", b.gotEvent)
	}
}

// A draft reaches the daemon whole.
func TestSendCarriesTheWholeDraft(t *testing.T) {
	t.Parallel()

	b := &recording{}
	r, _ := attach(t, b)
	want := domain.Draft{
		Body:       "morning Grace",
		Mentions:   []domain.Mention{{UserID: "@grace:example.org", Name: "Grace"}},
		ReplyTo:    "$parent:example.org",
		ThreadRoot: "$root:example.org",
	}

	if err := r.Send(context.Background(), "!a:example.org", want); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if b.gotRoom != "!a:example.org" {
		t.Errorf("backend saw room %q, want !a:example.org", b.gotRoom)
	}
	if !reflect.DeepEqual(b.gotDraft, want) {
		t.Errorf("backend saw draft %+v, want %+v", b.gotDraft, want)
	}
}

// Reactions, members, emoji, media and search: one call each, checking both the
// arguments that arrive and the result that returns.
func TestRemainingRolesRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{
		reacts:  []domain.Reaction{{ID: "$r:example.org", RoomID: "!a:example.org", Target: "$m:example.org", Sender: "@ada:example.org", Key: "🎉"}},
		members: []domain.Member{{UserID: "@ada:example.org", DisplayName: "Ada"}, {UserID: "@grace:example.org"}},
		hits:    []domain.SearchHit{{RoomID: "!a:example.org", EventID: "$m:example.org", Sender: "@ada:example.org", SenderName: "Ada", Timestamp: stamp(), Snippet: "the " + domain.HighlightStart + "code" + domain.HighlightEnd + " is"}},
		emoji:   []string{"👍", "🎉"},
		image:   []byte{0x89, 'P', 'N', 'G', 0x00, 0xff},
		words:   []domain.WordCandidate{{Word: "completion", Score: 42}, {Word: "compiler", Score: 7}},
	}
	r, _ := attach(t, b)
	ctx := context.Background()

	reacts, err := r.CachedReactions(ctx, "!a:example.org")
	if err != nil || !reflect.DeepEqual(reacts, b.reacts) {
		t.Errorf("CachedReactions() = %+v, %v; want %+v", reacts, err, b.reacts)
	}
	if serr := r.SendReaction(ctx, "!a:example.org", "$m:example.org", "🚀"); serr != nil {
		t.Fatalf("SendReaction() error = %v", serr)
	}
	if b.gotKey != "🚀" || b.gotEvent != "$m:example.org" {
		t.Errorf("backend saw reaction (%q, %q), want ($m:example.org, 🚀)", b.gotEvent, b.gotKey)
	}

	members, err := r.Members(ctx, "!a:example.org", 50)
	if err != nil || !reflect.DeepEqual(members, b.members) {
		t.Errorf("Members() = %+v, %v; want %+v", members, err, b.members)
	}
	if b.gotLimit != 50 {
		t.Errorf("backend saw limit %d, want 50", b.gotLimit)
	}
	if got, rerr := r.RefreshMembers(ctx, "!a:example.org"); rerr != nil || !reflect.DeepEqual(got, b.members) {
		t.Errorf("RefreshMembers() = %+v, %v; want %+v", got, rerr, b.members)
	}
	if got, merr := r.MentionCandidates(ctx, "!a:example.org", 8); merr != nil || !reflect.DeepEqual(got, b.members) {
		t.Errorf("MentionCandidates() = %+v, %v; want %+v", got, merr, b.members)
	}

	if rerr := r.RecordEmoji(ctx, domain.EmojiComposed, "!a:example.org", "🙂"); rerr != nil {
		t.Fatalf("RecordEmoji() error = %v", rerr)
	}
	if b.gotKind != domain.EmojiComposed || b.gotEmoji != "🙂" {
		t.Errorf("backend saw (%q, %q), want (compose, 🙂)", b.gotKind, b.gotEmoji)
	}

	// Bytes, not a path: a byte lost or a copy truncated shows up as an image that
	// will not decode, so the exact slice matters.
	image, err := r.LoadImage(ctx, "!a:example.org", "$m:example.org")
	if err != nil || !reflect.DeepEqual(image, b.image) {
		t.Errorf("LoadImage() = %v, %v; want %v", image, err, b.image)
	}

	hits, err := r.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("code", time.Now()), Rooms: domain.TheseRooms([]domain.RoomID{"!a:example.org"}), Limit: 30})
	if err != nil || !reflect.DeepEqual(hits, b.hits) {
		t.Errorf("SearchMessages() = %+v, %v; want %+v", hits, err, b.hits)
	}
	if b.gotQuery != "code" {
		t.Errorf("backend saw query %q, want code", b.gotQuery)
	}

	// Completion crosses as a prefix, two room lists and a scope, and comes back in the
	// order it was ranked in.
	words, err := r.CompleteWord(ctx, domain.CompleteRequest{
		Prefix:     "comp",
		RoomIDs:    []domain.RoomID{"!a:example.org"},
		SpaceRooms: []domain.RoomID{"!a:example.org", "!b:example.org"},
		Scope:      "room",
		Limit:      5,
	})
	if err != nil || !reflect.DeepEqual(words, b.words) {
		t.Errorf("CompleteWord() = %+v, %v; want %+v", words, err, b.words)
	}
	if b.gotComplete.Prefix != "comp" || b.gotComplete.Scope != "room" || b.gotComplete.Limit != 5 {
		t.Errorf("backend saw %+v, want the prefix, scope and limit as sent", b.gotComplete)
	}
	if len(b.gotComplete.RoomIDs) != 1 || len(b.gotComplete.SpaceRooms) != 2 {
		t.Errorf("backend saw rooms %+v / %+v, want one room and its two space rooms",
			b.gotComplete.RoomIDs, b.gotComplete.SpaceRooms)
	}
}

// Membership and account: the two answers to an invitation, and the credentials
// path. An access token crossing the socket is the reason its mode is 0600.
func TestMembershipAndAccountRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{}
	r, _ := attach(t, b)
	ctx := context.Background()

	joined, err := r.JoinRoom(ctx, "#book-club:example.org", []string{"one.org", "two.org"})
	if err != nil || joined != "!joined:example.org" {
		t.Errorf("JoinRoom() = %q, %v; want !joined:example.org", joined, err)
	}
	if b.gotAlias != "#book-club:example.org" {
		t.Errorf("backend saw alias %q, want #book-club:example.org", b.gotAlias)
	}
	// via is what makes a room unknown to this homeserver joinable.
	if !reflect.DeepEqual(b.gotVia, []string{"one.org", "two.org"}) {
		t.Errorf("backend saw via %+v, want [one.org two.org]", b.gotVia)
	}
	if lerr := r.LeaveRoom(ctx, "!a:example.org"); lerr != nil {
		t.Fatalf("LeaveRoom() error = %v", lerr)
	}
	if b.gotRoom != "!a:example.org" {
		t.Errorf("backend saw room %q, want !a:example.org", b.gotRoom)
	}
}

// The three verification controls carry their transaction ID, which is the only
// thing tying an answer to the flow being answered.
func TestVerificationControlsRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{}
	r, _ := attach(t, b)
	ctx := context.Background()

	controls := map[string]func(context.Context, string) error{
		"accept":  r.AcceptVerification,
		"confirm": r.ConfirmSAS,
		"cancel":  r.CancelVerification,
	}
	for name, control := range controls {
		b.gotTxn = ""
		if err := control(ctx, "txn-"+name); err != nil {
			t.Fatalf("%s error = %v", name, err)
		}
		if b.gotTxn != "txn-"+name {
			t.Errorf("%s: backend saw txn %q, want txn-%s", name, b.gotTxn, name)
		}
	}
}

// blockingReader holds MarkRoomsRead open long enough for the test to hang up on it, then
// reports whether the context it was handed was still alive.
type blockingReader struct {
	apitest.Nop
	started chan struct{}
	alive   chan bool
}

func (b *blockingReader) MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, _ bool) (domain.ReadResult, error) {
	close(b.started)
	select {
	case <-ctx.Done():
		b.alive <- false
	case <-time.After(500 * time.Millisecond):
		b.alive <- true
	}
	return domain.ReadResult{Marked: len(roomIDs)}, nil
}

// The client hanging up must not stop a mark-read that is already under way.
func TestMarkRoomsReadSurvivesTheClientHangingUp(t *testing.T) {
	t.Parallel()

	b := &blockingReader{started: make(chan struct{}), alive: make(chan bool, 1)}
	r, _ := attach(t, b)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// The result is discarded: by design there is nobody left to receive it.
		_, _ = r.MarkRoomsRead(ctx, []domain.RoomID{"!a:example.org", "!b:example.org"}, false)
	}()
	<-b.started
	cancel()

	if alive := <-b.alive; !alive {
		t.Error("the walk was canceled when the client hung up; the remaining rooms would stay unread")
	}
}

// What a bridge refuses crosses the wire as a list of three-field rows, and the timestamp
// is the field a conversion is most likely to drop.
func TestReactionRefusalsRoundTrip(t *testing.T) {
	t.Parallel()

	when := time.UnixMilli(1756000000000)
	b := &recording{refusals: []domain.ReactionRefusal{
		{Protocol: "Telegram", Emoji: "🫶", At: when},
		{Protocol: "RCS", Emoji: "🤯", At: when.Add(time.Hour)},
	}}
	r, _ := attach(t, b)
	ctx := context.Background()

	got, err := r.ReactionRefusals(ctx)
	if err != nil {
		t.Fatalf("ReactionRefusals() error = %v", err)
	}
	if !reflect.DeepEqual(got, b.refusals) {
		t.Errorf("ReactionRefusals() = %+v, want %+v", got, b.refusals)
	}
	if rerr := r.RecordReactionRefusal(ctx, "WhatsApp", "🙈"); rerr != nil {
		t.Fatalf("RecordReactionRefusal() error = %v", rerr)
	}
	if b.gotScope != "WhatsApp" || b.gotEmoji != "🙈" {
		t.Errorf("backend saw (%q, %q), want (WhatsApp, 🙈)", b.gotScope, b.gotEmoji)
	}
}

// A map crossing the wire, which protobuf carries as repeated entries.
func TestEmojiScoresRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{scores: map[string]int{"🎯": 14, "🧭": 4, "🛰️": 1}}
	r, _ := attach(t, b)

	siblings := []domain.RoomID{"!a:example.org", "!b:example.org"}
	got, err := r.EmojiScores(context.Background(), domain.EmojiComposed, "!a:example.org", siblings, "global")
	if err != nil {
		t.Fatalf("EmojiScores() error = %v", err)
	}
	if !reflect.DeepEqual(got, b.scores) {
		t.Errorf("EmojiScores() = %v, want %v", got, b.scores)
	}
	if b.gotKind != domain.EmojiComposed || !reflect.DeepEqual(b.gotRooms, siblings) {
		t.Errorf("backend saw (%q, %+v), want (compose, %+v)", b.gotKind, b.gotRooms, siblings)
	}
	// The scope is the client's setting and decides which rooms count at all, so a
	// dropped one is a ranking silently answering a different question.
	if b.gotScope != "global" {
		t.Errorf("backend saw scope %q, want global", b.gotScope)
	}
}

// A backend failure fails the call on every method group rather than reading as an empty result.
func TestEveryRolePropagatesFailure(t *testing.T) {
	t.Parallel()

	b := &recording{failWith: errors.New("cache is locked")}
	r, _ := attach(t, b)
	ctx := context.Background()

	calls := map[string]func() error{
		"RefreshRooms": func() error { _, err := r.RefreshRooms(ctx); return err },
		"MarkRead":     func() error { return r.MarkRead(ctx, "!a:example.org", "$m:example.org", false) },
		"EmojiScores": func() error {
			_, err := r.EmojiScores(ctx, domain.EmojiReaction, "!a:example.org", nil, "room")
			return err
		},
		"ReactionRefusals": func() error {
			_, err := r.ReactionRefusals(ctx)
			return err
		},
		"RecordReactionRefusal": func() error { return r.RecordReactionRefusal(ctx, "Telegram", "🫶") },
		"MarkRoomsRead": func() error {
			_, err := r.MarkRoomsRead(ctx, []domain.RoomID{"!a:example.org"}, false)
			return err
		},
		"Spaces":         func() error { _, err := r.Spaces(ctx); return err },
		"CachedTimeline": func() error { _, err := r.CachedTimeline(ctx, "!a:example.org"); return err },
		"Timeline":       func() error { _, err := r.Timeline(ctx, "!a:example.org", "", 10); return err },
		"FetchEvent": func() error {
			_, err := r.FetchEvent(ctx, "!a:example.org", "$m:example.org")
			return err
		},
		"Send":              func() error { return r.Send(ctx, "!a:example.org", domain.Draft{Body: "hi"}) },
		"CachedUnread":      func() error { _, err := r.CachedUnread(ctx); return err },
		"CachedReactions":   func() error { _, err := r.CachedReactions(ctx, "!a:example.org"); return err },
		"SendReaction":      func() error { return r.SendReaction(ctx, "!a:example.org", "$m:example.org", "👍") },
		"Members":           func() error { _, err := r.Members(ctx, "!a:example.org", 0); return err },
		"MentionCandidates": func() error { _, err := r.MentionCandidates(ctx, "!a:example.org", 0); return err },
		"RecordEmoji":       func() error { return r.RecordEmoji(ctx, domain.EmojiReaction, "!a:example.org", "👍") },
		"LoadImage":         func() error { _, err := r.LoadImage(ctx, "!a:example.org", "$m:example.org"); return err },
		"SearchMessages": func() error {
			_, err := r.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("x", time.Now()), Rooms: domain.EveryRoom(), Limit: 10})
			return err
		},
		"CompleteWord": func() error {
			_, err := r.CompleteWord(ctx, domain.CompleteRequest{Prefix: "comp", Limit: 5})
			return err
		},
		"JoinRoom":   func() error { _, err := r.JoinRoom(ctx, "#x:example.org", nil); return err },
		"LeaveRoom":  func() error { return r.LeaveRoom(ctx, "!a:example.org") },
		"ConfirmSAS": func() error { return r.ConfirmSAS(ctx, "txn") },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s() error = nil, want the daemon's failure", name)
		}
	}
}

// A sentinel keeps its identity (errors.Is) across the wire; an unclassified error gains none.
func TestSentinelSurvivesTheWire(t *testing.T) {
	t.Parallel()

	for _, fail := range []error{api.ErrNoEncryption, errors.New("cache is locked")} {
		r, _ := attach(t, &recording{failWith: fail})
		err := r.AcceptVerification(context.Background(), "txn-1")
		if err == nil {
			t.Fatalf("AcceptVerification() error = nil, want %v", fail)
		}
		if got, want := errors.Is(err, api.ErrNoEncryption), errors.Is(fail, api.ErrNoEncryption); got != want {
			t.Errorf("AcceptVerification() error = %v; errors.Is(ErrNoEncryption) = %v, want %v", err, got, want)
		}
	}
}

// A link handed from one kith process to another, over the daemon.
func TestFollowReachesAnAttachedClientAndSaysSo(t *testing.T) {
	t.Parallel()

	b := &recording{}
	r, h := attach(t, b)
	ctx := context.Background()

	// Nobody on the receiving stream yet: the link reaches no one, and the answer says so
	// rather than pretending.
	delivered, err := r.Follow(ctx, "matrix:r/x:example.org")
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	if delivered {
		t.Error("Follow claimed delivery with nothing subscribed")
	}

	// With the client attached, it is delivered and it arrives.
	started := make(chan error, 1)
	go func() { started <- r.Start(context.Background()) }()
	h.waitAttached(1)
	waitFor(t, func() bool { return h.streams.FollowSubscribers() > 0 })
	delivered, err = r.Follow(ctx, "matrix:roomid/abc:example.org")
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	if !delivered {
		t.Fatal("Follow found nobody attached")
	}
	select {
	case got := <-r.Follows():
		if got != "matrix:roomid/abc:example.org" {
			t.Errorf("client received %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("the link never arrived at the client")
	}
}

// Maps cross as repeated entries, so keys and values are compared whole; an uncolored
// room comes back empty rather than as an error.
func TestSenderSlotsRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{}
	r, _ := attach(t, b)
	ctx := context.Background()

	if got, err := r.SenderSlots(ctx, "!a:example.org"); err != nil || len(got) != 0 {
		t.Errorf("SenderSlots() on an uncolored room = %v, %v; want nothing", got, err)
	}
	b.slots = map[string]int{"@bob:example.org": 0, "@zara:example.org": 7}
	got, err := r.SenderSlots(ctx, "!a:example.org")
	if err != nil || !reflect.DeepEqual(got, b.slots) || b.gotRoom != "!a:example.org" {
		t.Errorf("SenderSlots() = %v, %v (room %q); want %v", got, err, b.gotRoom, b.slots)
	}

	send := map[string]int{"@carol:example.org": 3, "@dave:example.org": 4}
	if err := r.SaveSenderSlots(ctx, "!b:example.org", send); err != nil {
		t.Fatalf("SaveSenderSlots() error = %v", err)
	}
	if b.gotRoom != "!b:example.org" || !reflect.DeepEqual(b.gotSlots, send) {
		t.Errorf("backend saw %q %v, want !b:example.org %v", b.gotRoom, b.gotSlots, send)
	}
}

// A verdict carries its reason (rule, filter) and a release travels as one.
func TestSpamRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{spam: []domain.SpamVerdict{
		{Room: "!crypto:example.org", Rule: domain.SpamFirstMessage, Filter: "crypto", At: time.UnixMilli(1_700_000_000_000)},
		{Room: "!sms:example.org", Rule: domain.SpamDirect, Filter: "that number"},
		{Room: "!freed:example.org", Released: true, At: time.UnixMilli(1_700_000_100_000)},
	}}
	r, _ := attach(t, b)
	ctx := context.Background()

	got, err := r.SpamRooms(ctx)
	if err != nil || !reflect.DeepEqual(got, b.spam) {
		t.Errorf("SpamRooms() = %+v, %v; want %+v", got, err, b.spam)
	}
	sent := []domain.SpamVerdict{
		{Room: "!crypto:example.org", Rule: domain.SpamMostly, Filter: "crypto"},
		{Room: "!crypto:example.org", Released: true, At: time.UnixMilli(1_700_000_200_000)},
	}
	for _, v := range sent {
		if err := r.MarkSpam(ctx, v); err != nil {
			t.Fatalf("MarkSpam(%+v) error = %v", v, err)
		}
	}
	if !reflect.DeepEqual(b.gotVerdicts, sent) {
		t.Errorf("the daemon was told %+v, want %+v", b.gotVerdicts, sent)
	}
}

// Rare and Certain are single bools: dropped, a hint arrives as a misspelling (or an
// uncertain hint as license to rewrite).
func TestSpellingRoundTrip(t *testing.T) {
	t.Parallel()

	b := &recording{spelling: []domain.Misspelling{
		{Word: "wrng", Start: 2, End: 9, Suggestions: []string{"receive"}},
		{Word: "hte", Start: 10, End: 13, Suggestions: []string{"the", "tie"}, Rare: true},
		{Word: "thq", Start: 14, End: 17, Suggestions: []string{"the"}, Rare: true, Certain: true},
	}}
	r, _ := attach(t, b)
	ctx := context.Background()

	got, err := r.CheckSpelling(ctx, "I wrng hte thq")
	if err != nil || !reflect.DeepEqual(got, b.spelling) {
		t.Errorf("CheckSpelling() = %+v, %v; want %+v", got, err, b.spelling)
	}
	if err := r.AllowRareWord(ctx, "שגיא"); err != nil || b.gotWord != "שגיא" {
		t.Errorf("AllowRareWord() = %v, daemon saw %q", err, b.gotWord)
	}
}

// The model offer's evidence (size, recall), the no-offer case and the missing-program
// sentence all survive; an install refusal crosses as an error.
func TestModelRoundTrip(t *testing.T) {
	t.Parallel()

	for _, want := range []domain.ModelSuggestion{
		{Offer: true, Candidate: domain.ModelCandidate{
			Tag: "smollm2-360m", Name: "SmolLM2 360M Instruct (Q8_0)", Bytes: 386404992, Recall: 0.339,
		}},
		{},
		{Why: "llama-server is not installed — Arch: pacman -S llama.cpp"},
	} {
		r, _ := attach(t, &recording{model: want})
		got, err := r.DetectModel(context.Background())
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("DetectModel() = %+v, %v; want %+v", got, err, want)
		}
	}

	b := &recording{}
	r, _ := attach(t, b)
	if err := r.InstallModel(context.Background(), "smollm2-360m"); err != nil || b.gotWord != "smollm2-360m" {
		t.Errorf("InstallModel() = %v, daemon saw %q", err, b.gotWord)
	}
	if err := r.InstallModel(context.Background(), "no-such-model"); err == nil {
		t.Error("a failed install came back as a success")
	}
}
