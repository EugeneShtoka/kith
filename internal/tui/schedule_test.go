package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func scheduledFixture() domain.ScheduledMessage {
	return domain.ScheduledMessage{
		ID: "s1", RoomID: mx + "!a:x", Body: "later",
		At: time.Now().Add(time.Hour), Written: time.Now(),
	}
}

func TestParseScheduleTime(t *testing.T) {
	t.Parallel()

	// A Monday at 08:00, so "09:00" is later today and "07:00" is tomorrow.
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		kind, arg string
		wantAt    time.Time
		wantBody  string
	}{
		"clock later today": {
			"at", "09:00 good morning",
			time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC), "good morning",
		},
		"clock already past rolls to tomorrow": {
			"at", "07:00 good morning",
			time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC), "good morning",
		},
		"twelve hour clock": {
			"at", "9pm night night",
			time.Date(2026, 9, 7, 21, 0, 0, 0, time.UTC), "night night",
		},
		"delay in hours": {
			"in", "2h see you then",
			now.Add(2 * time.Hour), "see you then",
		},
		"delay compound": {
			"in", "1h30m nearly there",
			now.Add(90 * time.Minute), "nearly there",
		},
		"body with several words and punctuation": {
			"in", "30m ok — see you at the usual place",
			now.Add(30 * time.Minute), "ok — see you at the usual place",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			at, body, err := parseScheduleTime(tc.kind, tc.arg, now)
			if err != nil {
				t.Fatalf("parseScheduleTime(%q, %q): %v", tc.kind, tc.arg, err)
			}
			if !at.Equal(tc.wantAt) {
				t.Errorf("at = %v, want %v", at, tc.wantAt)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

// Refusals say why.
func TestParseScheduleTimeRefusals(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	tests := map[string]struct{ kind, arg string }{
		"no message":             {"at", "09:00"},
		"no time at all":         {"at", ""},
		"message but no time":    {"in", "just some words"},
		"time that is not one":   {"at", "half past fish tomorrow"},
		"delay that is not one":  {"in", "soonish tomorrow"},
		"delay of zero":          {"in", "0s now then"},
		"delay running backward": {"in", "-2h yesterday"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := parseScheduleTime(tc.kind, tc.arg, now); err == nil {
				t.Errorf("parseScheduleTime(%q, %q) was accepted", tc.kind, tc.arg)
			}
		})
	}
}

func TestSchedulingWithoutADaemonSaysSo(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m.schedules = nil

	cmd := m.scheduleCmd(scheduledFixture(), "queued")
	if cmd == nil {
		t.Fatal("no command, so nothing would ever be reported")
	}
	msg, ok := cmd().(scheduleDoneMsg)
	if !ok {
		t.Fatalf("command produced %T, want scheduleDoneMsg", cmd())
	}
	if msg.err == nil {
		t.Error("scheduling without a daemon reported success")
	}
}

func TestScheduledScopedToOneRoom(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
		{ID: mx + "!b:x", Name: "Beta"},
	}})
	now := time.Now()
	queue := []domain.ScheduledMessage{
		{ID: "s1", RoomID: mx + "!a:x", Body: "here", At: now.Add(time.Hour), Written: now},
		{ID: "s2", RoomID: mx + "!b:x", Body: "elsewhere", At: now.Add(2 * time.Hour), Written: now},
		{ID: "s3", RoomID: mx + "!a:x", Body: "here too", At: now.Add(3 * time.Hour), Written: now},
	}

	all := update(t, m, scheduledMsg{queue: queue})
	if got := len(all.picker.items); got != 3 {
		t.Fatalf(":scheduled listed %d rows, want 3", got)
	}
	if !strings.Contains(all.picker.items[0].label, "Alpha") {
		t.Errorf("row = %q, want the room named when the list spans rooms", all.picker.items[0].label)
	}

	// One room: only its two rows, without the room name.
	scoped := update(t, m, scheduledMsg{queue: queue, onlyRoom: mx + "!a:x"})
	if got := len(scoped.picker.items); got != 2 {
		t.Fatalf("/scheduled listed %d rows, want the 2 in this room", got)
	}
	for _, item := range scoped.picker.items {
		if strings.Contains(item.label, "Alpha") {
			t.Errorf("row = %q, want the room left off a single-room list", item.label)
		}
		if item.value == "s2" {
			t.Error("a message for another room is in the room-scoped list")
		}
	}
}

func TestScheduledInThisRoomWhenEmpty(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}})
	now := time.Now()
	other := []domain.ScheduledMessage{{ID: "s1", RoomID: mx + "!b:x", Body: "elsewhere", At: now.Add(time.Hour), Written: now}}

	scoped := update(t, m.clearStatus(), scheduledMsg{queue: other, onlyRoom: mx + "!a:x"})
	if scoped.status() == "" {
		t.Errorf("status = %q, want it scoped to the room", scoped.status())
	}
	if scoped.picker.active() {
		t.Error("an empty room-scoped list opened a chooser anyway")
	}
}

// testdata/schedule.json says what /at and /in read, for the desktop's port too
// (desktop/src/renderer/schedule.ts); this holds the table to parseScheduleTime.
func TestTheScheduleTableIsWhatTheTUIReads(t *testing.T) {
	t.Parallel()
	var table struct {
		Now   string `json:"now"`
		Cases []struct {
			Kind  string `json:"kind"`
			Arg   string `json:"arg"`
			At    string `json:"at"`
			Body  string `json:"body"`
			Error string `json:"error"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	const wall = "2006-01-02T15:04:05"
	now, err := time.ParseInLocation(wall, table.Now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range table.Cases {
		at, body, err := parseScheduleTime(c.Kind, c.Arg, now)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.Error {
			t.Errorf("/%s %q: error %q, the table says %q", c.Kind, c.Arg, got, c.Error)
			continue
		}
		if err == nil && (at.Format(wall) != c.At || body != c.Body) {
			t.Errorf("/%s %q: %s %q, the table says %s %q", c.Kind, c.Arg, at.Format(wall), body, c.At, c.Body)
		}
	}
}
