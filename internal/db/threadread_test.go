package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// threadRoom lays down a room with a main timeline and two threads, read to $read,
// and returns its clock.
func threadRoom(t *testing.T, cache *Cache) func(int) time.Time {
	t.Helper()
	base := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }
	msgs := []domain.Message{
		{ID: mx + "$read", Sender: "matrix:@alice:x", Body: "before", Timestamp: at(0)},
		{ID: mx + "$root", Sender: "matrix:@alice:x", Body: "a question", Timestamp: at(1)},
		{ID: mx + "$main", Sender: "matrix:@alice:x", Body: "in the room", Timestamp: at(2)},
		{ID: mx + "$a1", Sender: "matrix:@bob:x", Body: "an answer", Timestamp: at(3), ThreadRoot: mx + "$root"},
		{ID: mx + "$a2", Sender: "matrix:@bob:x", Body: "hey you", Timestamp: at(4), ThreadRoot: mx + "$root", Mentioned: true},
		{ID: mx + "$mine", Sender: me, Body: "and one from me", Timestamp: at(5), ThreadRoot: mx + "$root"},
		{ID: mx + "$b1", Sender: "matrix:@bob:x", Body: "elsewhere", Timestamp: at(6), ThreadRoot: mx + "$other"},
	}
	for i := range msgs {
		mustSave(t, cache, mx+"!a:x", msgs[i])
	}
	if err := cache.SaveUnread(context.Background(), domain.Unread{RoomID: mx + "!a:x", ReadEvent: mx + "$read"}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	return at
}

// Thread replies count against their thread's position and the room's count stops at
// the main timeline, so the thread rows break down the room's badge.
func TestCountingSplitsTheRoomFromItsThreads(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	// The floor sits where the room was read, as migration seeds it.
	if err := cache.SeedThreadFloor(ctx, mx+"!a:x", mx+"$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}

	messages, mentions, counted, err := cache.CountUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountUnread() error = %v", err)
	}
	// $root and $main only: the thread replies are the threads' to count.
	if !counted || messages != 2 || mentions != 0 {
		t.Errorf("room count = %d/%d (counted %v), want 2/0 counted", messages, mentions, counted)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	// One thread, not two: your own reply is a read position.
	if len(threads) != 1 {
		t.Fatalf("threads = %+v, want only $other — a reply in $root floors it", threads)
	}
	if threads[0].Root != mx+"$other" || threads[0].Unread != 1 {
		t.Errorf("thread = %+v, want $other with 1 unread", threads[0])
	}
	// The receipt a "mark this read" would send points at the newest unread reply.
	if threads[0].Latest != mx+"$b1" {
		t.Errorf("latest unread in $other = %q, want $b1", threads[0].Latest)
	}
}

// Your reply floors only its own thread, up to itself; a later answer is unread and
// the room's count is untouched.
func TestYourReplyFloorsOnlyItsOwnThreadAndOnlyUpToItself(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, mx+"!a:x", mx+"$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}
	// An answer after our reply, in the same thread.
	mustSave(t, cache, mx+"!a:x", domain.Message{
		ID: mx + "$a3", Sender: "matrix:@bob:x", Body: "one more", Timestamp: at(7), ThreadRoot: mx + "$root",
	})

	threads, err := cache.CountThreadUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %+v, want $root back with the newer reply", threads)
	}
	var root domain.ThreadUnread
	for i := range threads {
		if threads[i].Root == mx+"$root" {
			root = threads[i]
		}
	}
	if root.Unread != 1 || root.Latest != mx+"$a3" {
		t.Errorf("$root = %+v, want exactly the reply that came after ours", root)
	}
	// The main timeline is unchanged: a thread reply says nothing about the room.
	messages, _, counted, err := cache.CountUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountUnread() error = %v", err)
	}
	if !counted || messages != 2 {
		t.Errorf("room count = %d (counted %v), want the 2 main-timeline messages", messages, counted)
	}
}

// A receipt for one thread clears only that thread.
func TestAThreadReceiptClearsOnlyItsThread(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, mx+"!a:x", mx+"$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}
	if err := cache.SaveThreadRead(ctx, mx+"!a:x", mx+"$root", mx+"$a2", at(4).UnixMilli()); err != nil {
		t.Fatalf("SaveThreadRead() error = %v", err)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	// $root is read to $a2, and the only thing after it there is our own reply.
	if len(threads) != 1 || threads[0].Root != mx+"$other" {
		t.Fatalf("threads = %+v, want only $other", threads)
	}
	messages, _, _, err := cache.CountUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountUnread() error = %v", err)
	}
	if messages != 2 {
		t.Errorf("room count = %d, want 2 — a thread receipt says nothing about the room", messages)
	}
}

// An unthreaded receipt says the whole room is read, threads included.
func TestAnUnthreadedReceiptAdvancesEveryThread(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, mx+"!a:x", mx+"$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}
	// The floor row has an empty root; an unthreaded receipt writes it.
	if err := cache.SaveThreadRead(ctx, mx+"!a:x", "", mx+"$b1", at(6).UnixMilli()); err != nil {
		t.Fatalf("SaveThreadRead() error = %v", err)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	if len(threads) != 0 {
		t.Errorf("threads = %+v, want none — the room was marked read", threads)
	}
}

// A read position never moves backwards.
func TestAReadPositionOnlyMovesForward(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SaveThreadRead(ctx, mx+"!a:x", mx+"$root", mx+"$a2", at(4).UnixMilli()); err != nil {
		t.Fatalf("SaveThreadRead() error = %v", err)
	}
	if err := cache.SaveThreadRead(ctx, mx+"!a:x", mx+"$root", mx+"$a1", at(3).UnixMilli()); err != nil {
		t.Fatalf("second SaveThreadRead() error = %v", err)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	for _, th := range threads {
		if th.Root == mx+"$root" {
			t.Errorf("$root is unread again after an older receipt: %+v", th)
		}
	}
}

// The floor is seeded once; only unthreaded receipts move it, or marking the main
// timeline read would mark its threads read too.
func TestTheFloorIsSeededOnceAndNotAgain(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, mx+"!a:x", mx+"$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}
	if err := cache.SeedThreadFloor(ctx, mx+"!a:x", mx+"$b1", at(6).UnixMilli()); err != nil {
		t.Fatalf("second SeedThreadFloor() error = %v", err)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	// Only $other: the reseed did not move the floor, and our reply quiets $root.
	if len(threads) != 1 || threads[0].Root != mx+"$other" {
		t.Errorf("threads = %+v, want $other still unread", threads)
	}
}

// Threads lists a room's cached threads whether or not it is open, including one
// whose root aged out.
func TestListingARoomsThreads(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, mx+"!a:x", mx+"$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}

	threads, err := cache.Threads(ctx, mine, mx+"!a:x")
	if err != nil {
		t.Fatalf("Threads() error = %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %+v, want two", threads)
	}
	// Newest activity first: $other's only reply is after $root's last.
	if threads[0].Root != mx+"$other" || threads[0].Count != 1 || threads[0].Latest != mx+"$b1" {
		t.Errorf("first thread = %+v, want $other with one reply", threads[0])
	}
	// Three replies, none unread: the third is ours, which reads what came before.
	if threads[1].Root != mx+"$root" || threads[1].Count != 3 || threads[1].Unread != 0 {
		t.Errorf("second thread = %+v, want $root with 3 replies and none unread", threads[1])
	}
	if threads[1].LatestSender != me {
		t.Errorf("newest reply sender = %q, want the last one written", threads[1].LatestSender)
	}
}

// Threads with the same newest-reply time (bridges stamp whole seconds) are listed
// by root.
func TestThreadsWithEqualActivityAreListedByRoot(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID(mx + "!r:x")
	at := time.UnixMilli(5000)
	var msgs []domain.Message
	for _, root := range []domain.EventID{mx + "$c", mx + "$a", mx + "$b"} {
		msgs = append(msgs, domain.Message{ID: root + "-reply", Sender: "matrix:@d:x", Body: "re", ThreadRoot: root, Timestamp: at})
	}
	if err := cache.SaveMessages(ctx, room, msgs); err != nil {
		t.Fatal(err)
	}
	threads, err := cache.Threads(ctx, mine, room)
	if err != nil {
		t.Fatal(err)
	}
	var roots []domain.EventID
	for _, th := range threads {
		roots = append(roots, th.Root)
	}
	if want := []domain.EventID{mx + "$a", mx + "$b", mx + "$c"}; !slices.Equal(roots, want) {
		t.Errorf("threads came back %v, want %v", roots, want)
	}
	unread, err := cache.CountThreadUnread(ctx, mine, room)
	if err != nil {
		t.Fatal(err)
	}
	roots = roots[:0]
	for _, u := range unread {
		roots = append(roots, u.Root)
	}
	if want := []domain.EventID{mx + "$a", mx + "$b", mx + "$c"}; !slices.Equal(roots, want) {
		t.Errorf("unread threads came back %v, want %v", roots, want)
	}
}

// A message your bridge puppet posted is your own: it floors what came before it, in
// the room and in a thread, and is not counted. The account alone does not know it.
func TestAMessageFromYourBridgedAccountIsYourOwn(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const puppet = "matrix:@slack_me:x"
	base := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }
	for _, msg := range []domain.Message{
		{ID: mx + "$read", Sender: "matrix:@alice:x", Body: "before", Timestamp: at(0)},
		{ID: mx + "$root", Sender: "matrix:@alice:x", Body: "a question", Timestamp: at(1)},
		{ID: mx + "$a1", Sender: "matrix:@bob:x", Body: "an answer", Timestamp: at(2), ThreadRoot: mx + "$root"},
		{ID: mx + "$a2", Sender: "matrix:@bob:x", Body: "another", Timestamp: at(3), ThreadRoot: mx + "$root"},
		{ID: mx + "$slack", Sender: puppet, Body: "mine, through Slack", Timestamp: at(4), ThreadRoot: mx + "$root"},
		{ID: mx + "$a3", Sender: "matrix:@bob:x", Body: "after mine", Timestamp: at(5), ThreadRoot: mx + "$root"},
		{ID: mx + "$main", Sender: puppet, Body: "mine in the room", Timestamp: at(6)},
	} {
		mustSave(t, cache, mx+"!a:x", msg)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: mx + "!a:x", ReadEvent: mx + "$read"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := cache.SeedThreadFloor(ctx, mx+"!a:x", mx+"$read", at(0).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name          string
		me            []string
		room, threads int
	}{
		{"the account alone", mine, 2, 4},
		{"the account and its puppet", []string{me, puppet}, 0, 1},
	} {
		messages, _, _, err := cache.CountUnread(ctx, c.me, mx+"!a:x")
		if err != nil {
			t.Fatal(err)
		}
		threads, err := cache.CountThreadUnread(ctx, c.me, mx+"!a:x")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, th := range threads {
			n += th.Unread
		}
		if messages != c.room || n != c.threads {
			t.Errorf("%s: room %d, threads %d; want %d, %d", c.name, messages, n, c.room, c.threads)
		}
	}
	if spoke, err := cache.SpokeInThread(ctx, mx+"!a:x", mx+"$root", []string{me, puppet}); err != nil || !spoke {
		t.Errorf("SpokeInThread = %v, %v; want true for a reply from the puppet", spoke, err)
	}
}

// A thread names you while its root or a reply that is not deleted does.
func TestAThreadNamesYouWhileAMentionStands(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	threadRoom(t, cache)
	for root, want := range map[domain.EventID]bool{mx + "$root": true, mx + "$other": false} {
		if named, err := cache.NamedInThread(ctx, mx+"!a:x", root); err != nil || named != want {
			t.Errorf("NamedInThread(%s) = %v, %v; want %v", root, named, err, want)
		}
	}
	if err := cache.MarkRedacted(ctx, mx+"!a:x", mx+"$a2", "matrix:@bob:x", "", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if named, err := cache.NamedInThread(ctx, mx+"!a:x", mx+"$root"); err != nil || named {
		t.Errorf("NamedInThread after the mention was deleted = %v, %v; want false", named, err)
	}
}
