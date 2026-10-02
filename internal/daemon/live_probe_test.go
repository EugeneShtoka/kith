package daemon_test

// An opt-in probe against a real, running kithd and a real account, kept because it
// earned its place: it is what found the shutdown-as-failure bug, where a clean SIGTERM was
// reported as a sync failure and systemd restarted the daemon.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/adrg/xdg"
)

func TestLiveDaemon(t *testing.T) {
	user := os.Getenv("KITH_LIVE")
	if user == "" {
		t.Skip("KITH_LIVE not set")
	}
	ctx := context.Background()

	r, socket := attachLive(t, ctx, user)
	ready, syncedAt, lastErr, err := r.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	t.Logf("STATUS ready=%t syncedAt=%s lastErr=%q", ready, syncedAt.Format(time.RFC3339), lastErr)

	rooms, err := r.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	t.Logf("ROOMS %d over the wire", len(rooms))
	if len(rooms) == 0 {
		t.Fatal("no rooms: the wire or the cache is not delivering")
	}

	spaces, err := r.Spaces(ctx)
	if err != nil {
		t.Fatalf("Spaces: %v", err)
	}
	t.Logf("SPACES %d", len(spaces))

	unread, err := r.CachedUnread(ctx)
	if err != nil {
		t.Fatalf("CachedUnread: %v", err)
	}
	t.Logf("UNREAD rows %d", len(unread))

	invites, err := r.CachedInvites(ctx)
	if err != nil {
		t.Fatalf("CachedInvites: %v", err)
	}
	t.Logf("INVITES %d", len(invites))

	// One room's contents, to prove message conversion and per-room reads work with real
	// (including encrypted) history.
	var room domain.RoomID
	var msgs []domain.Message
	scanned := 0
	for _, candidate := range rooms {
		scanned++
		got, terr := r.CachedTimeline(ctx, candidate.ID)
		if terr != nil {
			t.Fatalf("CachedTimeline(%s): %v", candidate.ID, terr)
		}
		if len(got) > 0 {
			room, msgs = candidate.ID, got
			break
		}
	}
	if room == "" {
		t.Fatal("no room has cached messages: CachedTimeline is not delivering over the wire")
	}
	t.Logf("TIMELINE %d cached messages (found after scanning %d rooms)", len(msgs), scanned)
	last := msgs[len(msgs)-1]
	t.Logf("  newest: id=%s sender=%q senderName-present=%t body-len=%d ts=%s (unix_ms=%d) mentions=%d media=%t",
		last.ID, last.Sender, last.SenderName != "", len(last.Body),
		last.Timestamp.Format(time.RFC3339Nano), last.Timestamp.UnixMilli(),
		len(last.Mentions), last.Media != nil)
	t.Logf("  room = %s", room)
	for i, m := range msgs {
		if i >= 3 {
			break
		}
		t.Logf("  [%d] id=%s ts=%s unix_ms=%d", i, m.ID, m.Timestamp.Format(time.RFC3339Nano), m.Timestamp.UnixMilli())
	}
	if last.Timestamp.IsZero() {
		t.Error("newest message has a zero timestamp: the wire lost it")
	}
	if last.Body == "" && last.Media == nil {
		t.Error("newest message has neither body nor media: the wire lost the content")
	}

	// The mark-read path, exercised without changing anything: a room ID the account is not
	// in has no cached event, so it comes back skipped and no receipt is sent.
	result, err := r.MarkRoomsRead(ctx, []domain.RoomID{"!definitely-not-a-room:invalid"}, false)
	if err != nil {
		t.Fatalf("MarkRoomsRead: %v", err)
	}
	t.Logf("MARK READ (unknown room) marked=%d skipped=%d failed=%d",
		result.Marked, result.Skipped, result.Failed)
	if want := (domain.ReadResult{Skipped: 1}); result != want {
		t.Errorf("MarkRoomsRead(unknown room) = %+v, want %+v — nothing should have been receipted", result, want)
	}

	// A live page from the homeserver, not the cache: this is the slow path, and the
	// one that carries reactions alongside messages.
	page, err := r.Timeline(ctx, room, "", 20)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	t.Logf("PAGE %d messages, %d reactions, next-token=%t",
		len(page.Messages), len(page.Reactions), page.Next != "")

	members, err := r.Members(ctx, room, 5)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	t.Logf("MEMBERS %d (capped at 5)", len(members))

	reacts, err := r.CachedReactions(ctx, room)
	if err != nil {
		t.Fatalf("CachedReactions: %v", err)
	}
	t.Logf("REACTIONS %d in the first room", len(reacts))

	scores, err := r.EmojiScores(ctx, "reaction", room, nil, "global")
	if err != nil {
		t.Fatalf("EmojiScores: %v", err)
	}
	t.Logf("EMOJI SCORED %d (global scope)", len(scores))

	// Search over the daemon's cache: a term common enough to hit, short enough to
	// be meaningless on its own.
	hits, err := r.SearchMessages(ctx, domain.SearchRequest{Filter: domain.ParseSearch("the", time.Now()), Rooms: domain.EveryRoom(), Limit: 5})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	t.Logf("SEARCH %d hits for a common term", len(hits))

	// The live stream: attach and see whether anything arrives. A quiet account is
	// not a failure, so this only reports.
	streamCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	stream := daemon.NewRemote(socket)
	done := make(chan error, 1)
	go func() { done <- stream.Start(streamCtx) }()
	select {
	case m := <-stream.Messages():
		t.Logf("STREAM live message in %s from %s", m.RoomID, m.Sender)
	case u := <-stream.Unread():
		t.Logf("STREAM live unread update for %s (%d notifications)", u.RoomID, u.Notifications)
	case <-streamCtx.Done():
		t.Log("STREAM nothing arrived in 12s (a quiet account, not a failure)")
	}
	stream.Stop()
	if serr := <-done; serr != nil {
		t.Errorf("Start returned %v, want nil after Stop", serr)
	}

	liveDND(ctx, t, r)
}

// liveDND exercises the notification surface against the running daemon: the
// do-not-disturb set and the config re-read.
func liveDND(ctx context.Context, t *testing.T, r *daemon.Remote) {
	t.Helper()

	before, err := r.DND(ctx)
	if err != nil {
		t.Fatalf("DND: %v", err)
	}
	t.Logf("DND %d entries in force before the probe", len(before))
	if len(before) > 0 {
		t.Skip("something is already muted; not disturbing it")
	}
	t.Cleanup(func() {
		if _, cerr := r.ClearAllDND(context.WithoutCancel(ctx)); cerr != nil {
			t.Errorf("could not clear the probe mute: %v", cerr)
		}
	})

	// A scoped, timed mute.
	until := time.Now().Add(90 * time.Minute)
	mutes, err := r.SetDND(ctx, withUntil(named(hideRule("!probe:example.org", ""), "Probe"), until))
	if err != nil {
		t.Fatalf("SetDND: %v", err)
	}
	if len(mutes) != 1 {
		t.Fatalf("mutes = %+v, want the one just set", mutes)
	}
	got := mutes[0]
	t.Logf("DND set: match=%q sender=%q name=%q until=%s remaining=%s",
		got.Match, got.Sender, got.Name, got.Until.Format(time.RFC3339), got.Remaining(time.Now()).Round(time.Second))
	if !got.Until.Equal(until.Truncate(time.Nanosecond)) && got.Until.Sub(until).Abs() > time.Second {
		t.Errorf("Until = %v, want %v — the deadline did not survive the wire", got.Until, until)
	}
	if _, offset := got.Until.Zone(); offset != localOffset(until) {
		t.Errorf("Until came back in the wrong zone (%v): a countdown drawn from it would be hours out", got.Until)
	}

	// Reading it back proves it is daemon state, not something this client remembers.
	read, err := r.DND(ctx)
	if err != nil {
		t.Fatalf("DND: %v", err)
	}
	if len(read) != 1 || read[0].Match != "!probe:example.org" {
		t.Errorf("DND = %+v, want the mute the daemon is holding", read)
	}

	left, err := r.ClearDND(ctx, "!probe:example.org", "")
	if err != nil {
		t.Fatalf("ClearDND: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("mutes = %+v, want none left", left)
	}
	t.Log("DND cleared: the daemon is as it was found")

	// The config re-read. It reads the file the daemon was started with, so this
	// only passes if that file is still valid — which is the point.
	if rerr := r.ReloadConfig(ctx); rerr != nil {
		t.Errorf("ReloadConfig: %v", rerr)
	} else {
		t.Log("RELOAD the daemon re-read and accepted its config")
	}
}

// localOffset is the local zone's offset at t.
func localOffset(t time.Time) int {
	_, offset := t.Local().Zone()
	return offset
}

// attachLive connects to the running daemon for this account, or fails the test saying
// which half was missing.
func attachLive(t *testing.T, ctx context.Context, user string) (*daemon.Remote, string) {
	t.Helper()
	// A live install from before instances: the instance is the account's hash.
	socket, err := daemon.SocketPath(domain.Storage{Instance: domain.AccountKey(user), RuntimeDir: filepath.Join(xdg.RuntimeDir, "kith")})
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	t.Logf("socket = %s", socket)
	r, err := daemon.Attach(ctx, socket, 30*time.Second)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return r, socket
}
