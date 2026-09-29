//go:build !windows

package notify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/notify"
)

// A hook that fails, or a player that is not there, is reported rather than lost —
// and the report never carries the message.
func TestFailedDeliveryIsReported(t *testing.T) {
	t.Parallel()
	type failure struct{ sink, err string }
	got := make(chan failure, 4)
	n := notify.New(notify.Settings{
		Command:      "exit 3",
		SoundCommand: "/nonexistent/kith-player",
		Templates:    tpl,
		Report:       func(sink string, err error) { got <- failure{sink, err.Error()} },
	})
	n.Notify(notify.Notification{Sender: "a", Body: "secret words", Sound: "/tmp/x.oga"})

	seen := map[string]string{}
	for len(seen) < 2 {
		select {
		case f := <-got:
			seen[f.sink] = f.err
		case <-time.After(5 * time.Second):
			t.Fatalf("reports so far %v; want the command and the sound player", seen)
		}
	}
	if !strings.Contains(seen["command"], "exit status 3") {
		t.Errorf("command report = %q, want its exit status", seen["command"])
	}
	if seen["sound"] == "" {
		t.Error("the missing sound player was not reported")
	}
	for _, e := range seen {
		if strings.Contains(e, "secret words") {
			t.Errorf("a report carried the message body: %q", e)
		}
	}
}
