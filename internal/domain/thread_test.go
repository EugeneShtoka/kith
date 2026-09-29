package domain_test

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// at builds a timestamp n minutes into an arbitrary fixed hour, so a test's
// ordering is readable at the call site.
func at(minute int) time.Time {
	return time.Date(2026, 8, 26, 10, minute, 0, 0, time.UTC)
}

func TestCollapseThreadsHidesRepliesUnderTheirRoot(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$root:x", RoomID: "!r:x", Sender: "@alice:x", Body: "ship it?", Timestamp: at(0)},
		{ID: "$other:x", RoomID: "!r:x", Sender: "@carol:x", Body: "unrelated", Timestamp: at(1)},
		{ID: "$r1:x", RoomID: "!r:x", Sender: "@bob:x", SenderName: "Bob", Body: "yes", Timestamp: at(2), ThreadRoot: "$root:x"},
		{ID: "$r2:x", RoomID: "!r:x", Sender: "@dan:x", SenderName: "Dan", Body: "wait", Timestamp: at(3), ThreadRoot: "$root:x"},
	}

	main, threads := domain.CollapseThreads(msgs)

	if len(main) != 2 || main[0].ID != "$root:x" || main[1].ID != "$other:x" {
		t.Fatalf("main timeline = %v, want the root and the unrelated message", ids(main))
	}
	if len(threads) != 1 {
		t.Fatalf("threads = %d, want 1", len(threads))
	}
	got := threads[0]
	switch {
	case got.Root != "$root:x":
		t.Errorf("Root = %q, want $root:x", got.Root)
	case got.RoomID != "!r:x":
		t.Errorf("RoomID = %q, want !r:x", got.RoomID)
	case got.Count != 2:
		t.Errorf("Count = %d, want 2 (replies, not the root)", got.Count)
	case got.Latest != "$r2:x" || !got.LatestAt.Equal(at(3)):
		t.Errorf("latest = %q at %v, want $r2:x at %v", got.Latest, got.LatestAt, at(3))
	case got.LatestSender != "@dan:x" || got.LatestSenderName != "Dan":
		t.Errorf("latest sender = %q/%q, want @dan:x/Dan", got.LatestSender, got.LatestSenderName)
	case !got.RootLoaded || got.Anchor != "$root:x":
		t.Errorf("anchor = %q (loaded %v), want the root", got.Anchor, got.RootLoaded)
	}
}

// A thread older than the cached window has replies we know about and a root we do not.
func TestCollapseThreadsAnchorsAnOlderThreadAtItsNewestReply(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$a:x", RoomID: "!r:x", Body: "first", Timestamp: at(0)},
		{ID: "$r1:x", RoomID: "!r:x", Body: "in the old thread", Timestamp: at(1), ThreadRoot: "$gone:x"},
		{ID: "$b:x", RoomID: "!r:x", Body: "second", Timestamp: at(2)},
		{ID: "$r2:x", RoomID: "!r:x", Body: "still in it", Timestamp: at(3), ThreadRoot: "$gone:x"},
	}

	main, threads := domain.CollapseThreads(msgs)

	if len(main) != 2 {
		t.Fatalf("main timeline = %v, want two messages", ids(main))
	}
	if len(threads) != 1 {
		t.Fatalf("threads = %d, want 1", len(threads))
	}
	if threads[0].RootLoaded {
		t.Error("RootLoaded = true, want false: the root aged out of the window")
	}
	if threads[0].Anchor != "$b:x" {
		t.Errorf("Anchor = %q, want $b:x — the newest message before the newest reply", threads[0].Anchor)
	}
}

// Replies older than everything else loaded have nothing to hang under, which is the
// one case an empty anchor means: draw it above the timeline rather than not at all.
func TestCollapseThreadsAnchorsAboveEverything(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$r1:x", RoomID: "!r:x", Body: "old thread reply", Timestamp: at(0), ThreadRoot: "$gone:x"},
		{ID: "$a:x", RoomID: "!r:x", Body: "first loaded", Timestamp: at(1)},
	}

	main, threads := domain.CollapseThreads(msgs)

	if len(main) != 1 || main[0].ID != "$a:x" {
		t.Fatalf("main timeline = %v, want just $a:x", ids(main))
	}
	if len(threads) != 1 || threads[0].Anchor != "" {
		t.Fatalf("threads = %+v, want one anchored above everything", threads)
	}
}

func TestCollapseThreadsOrdersByLatestActivity(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$root1:x", RoomID: "!r:x", Timestamp: at(0), Body: "one"},
		{ID: "$root2:x", RoomID: "!r:x", Timestamp: at(1), Body: "two"},
		{ID: "$r1:x", RoomID: "!r:x", Timestamp: at(2), Body: "a", ThreadRoot: "$root2:x"},
		{ID: "$r2:x", RoomID: "!r:x", Timestamp: at(3), Body: "b", ThreadRoot: "$root1:x"},
	}

	_, threads := domain.CollapseThreads(msgs)

	if len(threads) != 2 || threads[0].Root != "$root2:x" || threads[1].Root != "$root1:x" {
		t.Errorf("thread order = %+v, want the least recently active first", threads)
	}
}

func TestCollapseThreadsWithoutThreadsChangesNothing(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$a:x", RoomID: "!r:x", Body: "one", Timestamp: at(0)},
		{ID: "$b:x", RoomID: "!r:x", Body: "two", Timestamp: at(1)},
	}

	main, threads := domain.CollapseThreads(msgs)

	if len(main) != 2 || threads != nil {
		t.Errorf("collapse = %v / %+v, want the messages untouched and no threads", ids(main), threads)
	}
}

func TestThreadMessages(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$root:x", Timestamp: at(0), Body: "root"},
		{ID: "$other:x", Timestamp: at(1), Body: "elsewhere"},
		{ID: "$r1:x", Timestamp: at(2), Body: "reply", ThreadRoot: "$root:x"},
		{ID: "$x:x", Timestamp: at(3), Body: "another thread", ThreadRoot: "$root2:x"},
	}

	got := domain.ThreadMessages(msgs, "$root:x")

	if len(got) != 2 || got[0].ID != "$root:x" || got[1].ID != "$r1:x" {
		t.Errorf("thread = %v, want the root followed by its reply", ids(got))
	}
	if domain.ThreadMessages(msgs, "") != nil {
		t.Error("an empty root should select nothing")
	}
}

// An edit arrives carrying its target's ID and its own (m.replace) relation, so it has
// no thread root.
func TestMergeMessagesKeepsTheThreadThroughAnEdit(t *testing.T) {
	t.Parallel()

	original := domain.Message{ID: "$m:x", RoomID: "!r:x", Body: "typo", Timestamp: at(0), ThreadRoot: "$root:x"}
	edit := domain.Message{ID: "$m:x", RoomID: "!r:x", Body: "fixed", Timestamp: at(1), Edited: true}

	tests := map[string][2][]domain.Message{
		"edit after the original":  {{original}, {edit}},
		"edit before the original": {{edit}, {original}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			merged := domain.MergeMessages(tc[0], tc[1])
			if len(merged) != 1 {
				t.Fatalf("merged %d messages, want 1", len(merged))
			}
			if merged[0].ThreadRoot != "$root:x" {
				t.Errorf("ThreadRoot = %q, want $root:x", merged[0].ThreadRoot)
			}
			if merged[0].Body != "fixed" {
				t.Errorf("Body = %q, want the edited text", merged[0].Body)
			}
		})
	}
}

func ids(msgs []domain.Message) []domain.EventID {
	out := make([]domain.EventID, 0, len(msgs))
	for i := range msgs {
		out = append(out, msgs[i].ID)
	}
	return out
}

// The case this client shipped with: a plain reply — m.in_reply_to and no thread
// relation, which is what replying from the main timeline sends — answering a message a
// thread has since grown under.
func TestCollapseThreadsFoldsAPlainReplyIntoTheConversationItAnswers(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$root:x", RoomID: "!r:x", Sender: "@alice:x", Body: "did you fix it?", Timestamp: at(0)},
		{ID: "$mine:x", RoomID: "!r:x", Sender: "@me:x", SenderName: "Me", Body: "yes", Timestamp: at(1), ReplyTo: "$root:x"},
		{ID: "$ok:x", RoomID: "!r:x", Sender: "@alice:x", SenderName: "Alice", Body: "ok", Timestamp: at(2), ThreadRoot: "$root:x"},
	}

	main, threads := domain.CollapseThreads(msgs)

	if len(main) != 1 || main[0].ID != "$root:x" {
		t.Fatalf("main timeline = %v, want the root alone — the reply belongs to the thread", ids(main))
	}
	if len(threads) != 1 || threads[0].Count != 2 {
		t.Fatalf("threads = %+v, want one with both replies", threads)
	}
	if got := ids(domain.ThreadMessages(msgs, "$root:x")); len(got) != 3 || got[1] != "$mine:x" {
		t.Errorf("thread = %v, want the root, the plain reply, then the threaded one", got)
	}
}

// A reply chains: answering the plain reply above is answering into the same
// conversation, and so is every answer after it.
func TestCollapseThreadsFollowsAChainOfPlainReplies(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$root:x", RoomID: "!r:x", Body: "question", Timestamp: at(0)},
		{ID: "$r1:x", RoomID: "!r:x", Body: "answer", Timestamp: at(1), ThreadRoot: "$root:x"},
		{ID: "$r2:x", RoomID: "!r:x", Body: "about that answer", Timestamp: at(2), ReplyTo: "$r1:x"},
		{ID: "$r3:x", RoomID: "!r:x", Body: "and that", Timestamp: at(3), ReplyTo: "$r2:x"},
	}

	main, threads := domain.CollapseThreads(msgs)

	if len(main) != 1 || main[0].ID != "$root:x" {
		t.Fatalf("main timeline = %v, want the root alone", ids(main))
	}
	if len(threads) != 1 || threads[0].Count != 3 || threads[0].Latest != "$r3:x" {
		t.Fatalf("threads = %+v, want one holding all three, newest $r3:x", threads)
	}
}

// The rule stops where the evidence does.
func TestCollapseThreadsLeavesAnOrdinaryReplyInTheTimeline(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$a:x", RoomID: "!r:x", Body: "question", Timestamp: at(0)},
		{ID: "$b:x", RoomID: "!r:x", Body: "answer", Timestamp: at(1), ReplyTo: "$a:x"},
		{ID: "$c:x", RoomID: "!r:x", Body: "answer to that", Timestamp: at(2), ReplyTo: "$b:x"},
	}

	main, threads := domain.CollapseThreads(msgs)

	if len(main) != 3 {
		t.Errorf("main timeline = %v, want all three — no thread was ever started", ids(main))
	}
	if len(threads) != 0 {
		t.Errorf("threads = %+v, want none", threads)
	}
}

// A reply whose target is not among the loaded messages says nothing about any
// thread, so it stays where it is rather than being filed under a guess.
func TestCollapseThreadsKeepsAReplyWhoseTargetIsNotLoaded(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$root:x", RoomID: "!r:x", Body: "question", Timestamp: at(0)},
		{ID: "$r1:x", RoomID: "!r:x", Body: "answer", Timestamp: at(1), ThreadRoot: "$root:x"},
		{ID: "$far:x", RoomID: "!r:x", Body: "answering something older", Timestamp: at(2), ReplyTo: "$gone:x"},
	}

	main, _ := domain.CollapseThreads(msgs)

	if len(main) != 2 || main[1].ID != "$far:x" {
		t.Errorf("main timeline = %v, want the root and the reply to the unloaded message", ids(main))
	}
}

func TestThreadRootOfNamesTheConversationAMessageIsIn(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$root:x", RoomID: "!r:x", Body: "question", Timestamp: at(0)},
		{ID: "$plain:x", RoomID: "!r:x", Body: "answer", Timestamp: at(1), ReplyTo: "$root:x"},
		{ID: "$r1:x", RoomID: "!r:x", Body: "threaded", Timestamp: at(2), ThreadRoot: "$root:x"},
		{ID: "$loose:x", RoomID: "!r:x", Body: "unrelated", Timestamp: at(3)},
	}

	for _, tc := range []struct{ id, want domain.EventID }{
		{"$root:x", "$root:x"}, // a root is in its own conversation
		{"$r1:x", "$root:x"},
		{"$plain:x", "$root:x"},
		{"$loose:x", ""},
		{"$gone:x", ""},
		{"", ""},
	} {
		if got := domain.ThreadRootOf(msgs, tc.id); got != tc.want {
			t.Errorf("ThreadRootOf(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
