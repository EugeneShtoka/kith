package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// autoCopyNotifier builds a daemon notifier copying codes from the SMS bridge room to a
// file standing in for the clipboard, after the given number of sync responses.
func autoCopyNotifier(t *testing.T, autoCopy bool, syncs int) (*daemon.Notifications, *recorder, string) {
	t.Helper()

	// The daemon only copies inside a graphical session.
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	clip := filepath.Join(t.TempDir(), "clipboard")
	cfg := notifsOn("all")
	cfg.Clipboard.Command = "cat > " + clip
	cfg.Clipboard.AutoCopy = autoCopy
	cfg.Codes.Include = []string{smsRoom}

	rec := &recorder{}
	n, err := daemon.NewNotifications(cfg, codeWorld(), me, func(config.Notifications) notify.Notifier { return rec })
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	for range syncs {
		n.Synced(time.Now())
	}
	return n, rec, clip
}

// capturingDaemon is autoCopyNotifier past the catch-up batch.
func capturingDaemon(t *testing.T) (*daemon.Notifications, *recorder, string) {
	t.Helper()
	return autoCopyNotifier(t, true, 2)
}

// noCopy asserts, after giving the sink time to run, that nothing reached the clipboard.
func noCopy(t *testing.T, clip, why string) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(clip); err == nil {
		t.Error(why)
	}
}

// smsRoom is the bridge room codes arrive in.
const smsRoom = "!sms:example.org"

// codeWorld is the room list the capturing daemon resolves against.
func codeWorld() *scopeBackend {
	return &scopeBackend{rooms: []domain.Room{
		{ID: smsRoom, Name: "SMS", IsDirect: true},
		{ID: chatRm, Name: chatNam},
	}}
}

// codeMsg is a message carrying a verification code.
func codeMsg(room domain.RoomID, body string) domain.Message {
	return domain.Message{ID: "$1", RoomID: room, Sender: "@bridge:example.org", Body: body, Timestamp: time.Now()}
}

// clipboardHolds reads back what the sink was given, waiting for the command to
// finish writing.
func clipboardHolds(t *testing.T, path string) string {
	t.Helper()
	var got []byte
	waitFor(t, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		got = b
		return true
	})
	return string(got)
}

// The case the feature exists for: a code arrives with nothing attached, and it is
// in the clipboard by the time anyone looks.
func TestAutoCopyWritesTheClipboard(t *testing.T) {
	n, rec, clip := capturingDaemon(t)

	n.Deliver(context.Background(), codeMsg(smsRoom, "Your code is 482910"))

	if got := clipboardHolds(t, clip); got != "482910" {
		t.Errorf("clipboard = %q, want the code alone", got)
	}
	// And it says so: the clipboard changed without a keystroke, which the user
	// would otherwise discover by pasting the wrong thing.
	var said bool
	for _, alert := range rec.all() {
		if strings.Contains(alert.Body, "482910") && strings.Contains(alert.Body, "copied") {
			said = true
		}
	}
	if !said {
		t.Errorf("notifications = %+v, want one saying the code was copied", rec.all())
	}
}

// A room nobody named is not one to copy from, however code-shaped the message.
func TestAutoCopyStaysWhereItWasTold(t *testing.T) {
	n, _, clip := capturingDaemon(t)

	n.Deliver(context.Background(), codeMsg(chatRm, "the build failed with code 482910"))

	noCopy(t, clip, "a room outside the include list should not have reached the clipboard")
}

// Before the daemon has caught up, nothing is copied: the first sync response is everything
// that happened while it was down, and a week-old code pasted over whatever you were
// carrying is a real cost for no benefit.
func TestAutoCopyWaitsUntilCaughtUp(t *testing.T) {
	n, _, clip := autoCopyNotifier(t, true, 1) // the catch-up response only
	n.Deliver(context.Background(), codeMsg(smsRoom, "Your code is 482910"))
	noCopy(t, clip, "the catch-up batch should not reach the clipboard")
}

// A copy that cannot run has failed invisibly, which is the mode this project treats as a
// bug — so the code is delivered in the notification instead.
func TestAutoCopyReportsWhenItCannotCopy(t *testing.T) {
	n, rec, _ := capturingDaemon(t)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")

	n.Deliver(context.Background(), codeMsg(smsRoom, "Your code is 482910"))

	var told bool
	for _, alert := range rec.all() {
		if strings.Contains(alert.Body, "482910") && strings.Contains(alert.Body, "could not copy") {
			told = true
		}
	}
	if !told {
		t.Errorf("notifications = %+v, want the code delivered with the reason it was not copied", rec.all())
	}
}

// Auto-copy fires regardless of do-not-disturb. Copying does not interrupt you, and
// "don't ping me" is not "stop doing your job".
func TestAutoCopyIgnoresDND(t *testing.T) {
	n, _, clip := capturingDaemon(t)
	n.SetDND(hideRule("", ""))

	msg := codeMsg(smsRoom, "Your code is 482910")
	if _, notified := n.Deliver(context.Background(), msg); notified {
		t.Error("do-not-disturb should still hold the ordinary notification back")
	}
	if got := clipboardHolds(t, clip); got != "482910" {
		t.Errorf("clipboard = %q, want the code copied anyway", got)
	}
}

// With auto-copy off (the default) nothing is copied, even from an included room.
func TestAutoCopyOffCopiesNothing(t *testing.T) {
	n, _, clip := autoCopyNotifier(t, false, 2)
	n.Deliver(context.Background(), codeMsg(smsRoom, "Your code is 482910"))
	noCopy(t, clip, "nothing should be copied with the switch off")
}
