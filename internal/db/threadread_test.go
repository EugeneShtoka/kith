package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// threadRoom lays down one room with a main timeline and two threads, read to
// $read, and returns the times it used so a test can place a receipt among them.
func threadRoom(t *testing.T, cache *Cache) func(int) time.Time {
	t.Helper()
	base := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }
	msgs := []domain.Message{
		{ID: "$read", Sender: "@alice:x", Body: "before", Timestamp: at(0)},
		{ID: "$root", Sender: "@alice:x", Body: "a question", Timestamp: at(1)},
		{ID: "$main", Sender: "@alice:x", Body: "in the room", Timestamp: at(2)},
		{ID: "$a1", Sender: "@bob:x", Body: "an answer", Timestamp: at(3), ThreadRoot: "$root"},
		{ID: "$a2", Sender: "@bob:x", Body: "hey you", Timestamp: at(4), ThreadRoot: "$root", Mentioned: true},
		{ID: "$mine", Sender: me, Body: "and one from me", Timestamp: at(5), ThreadRoot: "$root"},
		{ID: "$b1", Sender: "@bob:x", Body: "elsewhere", Timestamp: at(6), ThreadRoot: "$other"},
	}
	for i := range msgs {
		mustSave(t, cache, "!a:x", msgs[i])
	}
	if err := cache.SaveUnread(context.Background(), domain.Unread{RoomID: "!a:x", ReadEvent: "$read"}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	return at
}

// A thread's replies are counted against that thread's own position, and the room's
// count stops at the main timeline. Together they are the room, which is what makes
// the sub-rows a breakdown of the badge above them rather than a second opinion.
func TestCountingSplitsTheRoomFromItsThreads(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	// The floor sits where the room was read, which is what migration writes for
	// every room that had a read position before threads existed.
	if err := cache.SeedThreadFloor(ctx, "!a:x", "$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}

	messages, mentions, counted, err := cache.CountUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountUnread() error = %v", err)
	}
	// $root and $main only: the thread replies are the threads' to count.
	if !counted || messages != 2 || mentions != 0 {
		t.Errorf("room count = %d/%d (counted %v), want 2/0 counted", messages, mentions, counted)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	// **One thread, not two — because a reply of your own is a read position.**
	if len(threads) != 1 {
		t.Fatalf("threads = %+v, want only $other — a reply in $root floors it", threads)
	}
	if threads[0].Root != "$other" || threads[0].Unread != 1 {
		t.Errorf("thread = %+v, want $other with 1 unread", threads[0])
	}
	// The receipt a "mark this read" would send points at the newest unread reply.
	if threads[0].Latest != "$b1" {
		t.Errorf("latest unread in $other = %q, want $b1", threads[0].Latest)
	}
}

// A reply of your own floors only its own thread, and only up to itself: a later
// answer in the same thread is unread again, and the room's own count is untouched.
func TestYourReplyFloorsOnlyItsOwnThreadAndOnlyUpToItself(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, "!a:x", "$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}
	// Somebody answers *after* our reply, in the thread we had answered.
	mustSave(t, cache, "!a:x", domain.Message{
		ID: "$a3", Sender: "@bob:x", Body: "one more", Timestamp: at(7), ThreadRoot: "$root",
	})

	threads, err := cache.CountThreadUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %+v, want $root back with the newer reply", threads)
	}
	var root domain.ThreadUnread
	for i := range threads {
		if threads[i].Root == "$root" {
			root = threads[i]
		}
	}
	if root.Unread != 1 || root.Latest != "$a3" {
		t.Errorf("$root = %+v, want exactly the reply that came after ours", root)
	}
	// And the room's main timeline is where it was: our reply was in a thread, and a
	// thread says nothing about the room.
	messages, _, counted, err := cache.CountUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountUnread() error = %v", err)
	}
	if !counted || messages != 2 {
		t.Errorf("room count = %d (counted %v), want the 2 main-timeline messages", messages, counted)
	}
}

// A receipt for one thread clears that thread and leaves the room and every other
// thread exactly where they were. That is the whole point of a threaded receipt,
// and the reason reading a room no longer clears the conversations inside it.
func TestAThreadReceiptClearsOnlyItsThread(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, "!a:x", "$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}
	if err := cache.SaveThreadRead(ctx, "!a:x", "$root", "$a2", at(4).UnixMilli()); err != nil {
		t.Fatalf("SaveThreadRead() error = %v", err)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	// $root is read to $a2, and the only thing after it there is our own reply.
	if len(threads) != 1 || threads[0].Root != "$other" {
		t.Fatalf("threads = %+v, want only $other", threads)
	}
	messages, _, _, err := cache.CountUnread(ctx, mine, "!a:x")
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
	if err := cache.SeedThreadFloor(ctx, "!a:x", "$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}
	// The floor row is the one with an empty root; an unthreaded receipt writes it.
	if err := cache.SaveThreadRead(ctx, "!a:x", "", "$b1", at(6).UnixMilli()); err != nil {
		t.Fatalf("SaveThreadRead() error = %v", err)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, "!a:x")
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
	if err := cache.SaveThreadRead(ctx, "!a:x", "$root", "$a2", at(4).UnixMilli()); err != nil {
		t.Fatalf("SaveThreadRead() error = %v", err)
	}
	if err := cache.SaveThreadRead(ctx, "!a:x", "$root", "$a1", at(3).UnixMilli()); err != nil {
		t.Fatalf("second SaveThreadRead() error = %v", err)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	for _, th := range threads {
		if th.Root == "$root" {
			t.Errorf("$root is unread again after an older receipt: %+v", th)
		}
	}
}

// The floor is seeded once and then only unthreaded receipts move it. If opening a
// room could re-seed it, every gesture that marks the main timeline read would mark
// its threads read too — which is exactly what threaded receipts exist to prevent.
func TestTheFloorIsSeededOnceAndNotAgain(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, "!a:x", "$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}
	if err := cache.SeedThreadFloor(ctx, "!a:x", "$b1", at(6).UnixMilli()); err != nil {
		t.Fatalf("second SeedThreadFloor() error = %v", err)
	}

	threads, err := cache.CountThreadUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread() error = %v", err)
	}
	// Only $other: the second seeding did not move the floor, and our own reply
	// quiets $root.
	if len(threads) != 1 || threads[0].Root != "$other" {
		t.Errorf("threads = %+v, want $other still unread", threads)
	}
}

// Threads lists what the cache holds for a room whether or not it is open, which is
// what the room list and the picker need. A thread whose root aged out is still
// listed: being a root is a fact about the replies, and they are here.
func TestListingARoomsThreads(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	at := threadRoom(t, cache)
	if err := cache.SeedThreadFloor(ctx, "!a:x", "$read", at(0).UnixMilli()); err != nil {
		t.Fatalf("SeedThreadFloor() error = %v", err)
	}

	threads, err := cache.Threads(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("Threads() error = %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %+v, want two", threads)
	}
	// Newest activity first: $other's only reply is after $root's last.
	if threads[0].Root != "$other" || threads[0].Count != 1 || threads[0].Latest != "$b1" {
		t.Errorf("first thread = %+v, want $other with one reply", threads[0])
	}
	// Three replies and **none** unread: the third is ours, and answering a thread is
	// proof of having read what came before the answer.
	if threads[1].Root != "$root" || threads[1].Count != 3 || threads[1].Unread != 0 {
		t.Errorf("second thread = %+v, want $root with 3 replies and none unread", threads[1])
	}
	if threads[1].LatestSender != me {
		t.Errorf("newest reply sender = %q, want the last one written", threads[1].LatestSender)
	}
}

// Threads with the same newest-reply time (bridged backfill stamps whole seconds) are
// listed in one order every time: by root, after activity.
func TestThreadsWithEqualActivityAreListedByRoot(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!r:x")
	at := time.UnixMilli(5000)
	var msgs []domain.Message
	for _, root := range []domain.EventID{"$c", "$a", "$b"} {
		msgs = append(msgs, domain.Message{ID: root + "-reply", Sender: "@d:x", Body: "re", ThreadRoot: root, Timestamp: at})
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
	if want := []domain.EventID{"$a", "$b", "$c"}; !slices.Equal(roots, want) {
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
	if want := []domain.EventID{"$a", "$b", "$c"}; !slices.Equal(roots, want) {
		t.Errorf("unread threads came back %v, want %v", roots, want)
	}
}

// A message a bridge posted for you (your Slack or WhatsApp puppet, listed with your
// account in [[display.identity]]) is your own: it floors what came before it, in the
// room and in a thread, and is not counted itself. Only the account, and it was not.
func TestAMessageFromYourBridgedAccountIsYourOwn(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const puppet = "@slack_me:x"
	base := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }
	for _, msg := range []domain.Message{
		{ID: "$read", Sender: "@alice:x", Body: "before", Timestamp: at(0)},
		{ID: "$root", Sender: "@alice:x", Body: "a question", Timestamp: at(1)},
		{ID: "$a1", Sender: "@bob:x", Body: "an answer", Timestamp: at(2), ThreadRoot: "$root"},
		{ID: "$a2", Sender: "@bob:x", Body: "another", Timestamp: at(3), ThreadRoot: "$root"},
		{ID: "$slack", Sender: puppet, Body: "mine, through Slack", Timestamp: at(4), ThreadRoot: "$root"},
		{ID: "$a3", Sender: "@bob:x", Body: "after mine", Timestamp: at(5), ThreadRoot: "$root"},
		{ID: "$main", Sender: puppet, Body: "mine in the room", Timestamp: at(6)},
	} {
		mustSave(t, cache, "!a:x", msg)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", ReadEvent: "$read"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := cache.SeedThreadFloor(ctx, "!a:x", "$read", at(0).UnixMilli()); err != nil {
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
		messages, _, _, err := cache.CountUnread(ctx, c.me, "!a:x")
		if err != nil {
			t.Fatal(err)
		}
		threads, err := cache.CountThreadUnread(ctx, c.me, "!a:x")
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
	if spoke, err := cache.SpokeInThread(ctx, "!a:x", "$root", []string{me, puppet}); err != nil || !spoke {
		t.Errorf("SpokeInThread = %v, %v; want true for a reply from the puppet", spoke, err)
	}
}

// A thread names you while its root or a reply that is not deleted does.
func TestAThreadNamesYouWhileAMentionStands(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	threadRoom(t, cache)
	for root, want := range map[domain.EventID]bool{"$root": true, "$other": false} {
		if named, err := cache.NamedInThread(ctx, "!a:x", root); err != nil || named != want {
			t.Errorf("NamedInThread(%s) = %v, %v; want %v", root, named, err, want)
		}
	}
	if err := cache.MarkRedacted(ctx, "!a:x", "$a2", "@bob:x", "", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if named, err := cache.NamedInThread(ctx, "!a:x", "$root"); err != nil || named {
		t.Errorf("NamedInThread after the mention was deleted = %v, %v; want false", named, err)
	}
}
