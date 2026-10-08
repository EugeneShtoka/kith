package notify_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/notify"
)

// A popup writes {date} and {time} as the person has chosen; a hook is handed ISO
// whatever was chosen, and its title the chosen form: scripts parse the variables,
// people read the title.
func TestThePersonsClockIsForPeopleAndISOForScripts(t *testing.T) {
	t.Parallel()
	sent := time.Date(2026, 8, 21, 14, 5, 0, 0, time.Local)
	n := notify.Notification{Room: "Standup", Sent: sent, Date: "21.08.2026", Time: "2:05 PM"}
	if got := n.Render("{date} {time}"); got != "21.08.2026 2:05 PM" {
		t.Errorf("rendered %q, want the chosen formats", got)
	}

	out := filepath.Join(t.TempDir(), "env")
	hook := notify.New(notify.Settings{
		Command:   `printf '%s|%s|%s' "$KITH_DATE" "$KITH_TIME" "$KITH_TITLE" > ` + out,
		Templates: notify.Templates{Title: "{room} at {time}"},
	})
	hook.Notify(n)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := os.ReadFile(out)
		if err == nil && len(got) > 0 {
			if want := "2026-08-21|14:05|Standup at 2:05 PM"; string(got) != want {
				t.Errorf("the hook got %q, want %q", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hook never ran (%v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
