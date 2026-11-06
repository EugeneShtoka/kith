package daemon_test

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room's name is written into [[display.name]] beside every other entry and served on
// the rail of each profile holding the room; an empty name takes it away; a room the
// profile does not hold is refused, nothing written; and two clients naming two rooms
// at once both keep their names, whichever write lands first.
func TestARoomsNameIsWrittenAndServed(t *testing.T) {
	t.Parallel()
	w := &filingWorld{railWorld: newRailWorld(), spaceErr: map[domain.SpaceID]error{}}
	alpha, bravo, tele := domain.RoomID(mx+"!a:x"), domain.RoomID(mx+"!b:x"), domain.RoomID("telegram:42/-1")
	w.set([]domain.Room{{ID: alpha, Name: "Alpha"}, {ID: bravo, Name: "Bravo", IsDirect: true}, {ID: tele, Name: "Tele"}},
		nil, []domain.Room{{ID: mx + "!i:x", Name: "Asked", Membership: domain.MembershipInvite}})
	path := filepath.Join(t.TempDir(), "config.toml")
	thread := config.DisplayName{Target: config.NameTargetThread + "$root", Name: "Plans"}
	if err := config.Save(path, config.Config{
		Display:  config.Display{Names: []config.DisplayName{thread, {Target: string(tele), Name: "Chat"}}},
		Profiles: []config.Profile{{Name: "home", Include: []string{"*"}}, {Name: "work", Include: []string{"group"}}},
	}); err != nil {
		t.Fatal(err)
	}
	h, _ := filingServed(t, w, path)
	c := daemon.Client(h.path)
	name := func(profile string, room domain.RoomID, to string) error {
		req := connect.NewRequest(&v1.SetRoomNameRequest{RoomId: string(room), Name: to})
		req.Header().Set("Kith-Profile", profile)
		_, err := c.SetRoomName(t.Context(), req)
		return err
	}
	// served waits until profile's rail serves room under want, and fails if it never does.
	served := func(profile string, room domain.RoomID, want string) {
		t.Helper()
		got := "(not served)"
		for deadline := time.Now().Add(settle); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			req := connect.NewRequest(&v1.RailRequest{})
			req.Header().Set("Kith-Profile", profile)
			resp, err := c.Rail(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range resp.Msg.GetRooms() {
				if r.GetFacts().GetId() == string(room) {
					got = r.GetName()
				}
			}
			if got == want {
				return
			}
		}
		t.Errorf("%s's rail serves %s named %q, want %q", profile, room, got, want)
	}
	names := func() []string {
		t.Helper()
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range cfg.Display.Names {
			out = append(out, n.Target+"="+n.Name)
		}
		slices.Sort(out)
		return out
	}

	if err := name("work", alpha, " Boss "); err != nil {
		t.Fatal(err)
	}
	served("home", alpha, "Boss")
	served("work", alpha, "Boss")
	served("home", tele, "Chat")
	served("home", bravo, "")
	want := []string{string(alpha) + "=Boss", string(tele) + "=Chat", thread.Target + "=Plans"}
	if got := names(); !slices.Equal(got, want) {
		t.Errorf("named: %q, want %q", got, want)
	}

	for _, tc := range []struct {
		what, profile string
		room          domain.RoomID
	}{
		{"a room of another profile", "work", bravo},
		{"an invitation", "home", mx + "!i:x"},
		{"a room kithd does not hold", "home", mx + "!nowhere:x"},
	} {
		if err := name(tc.profile, tc.room, "Nope"); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s: %v, want it refused", tc.what, err)
		}
	}
	if got := names(); !slices.Equal(got, want) {
		t.Errorf("after refusals: %q, want %q", got, want)
	}

	if err := name("home", alpha, ""); err != nil {
		t.Fatal(err)
	}
	served("work", alpha, "")
	want = []string{string(tele) + "=Chat", thread.Target + "=Plans"}
	if got := names(); !slices.Equal(got, want) {
		t.Errorf("cleared: %q, want %q", got, want)
	}

	// Two clients at once, round after round: a write lost to the other is made again
	// on what the other wrote, so neither name is lost.
	const rounds = 5
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	for _, room := range []domain.RoomID{alpha, tele} {
		wg.Go(func() {
			for i := range rounds {
				errs <- name("home", room, fmt.Sprintf("%s %d", room, i))
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	last := fmt.Sprint(rounds - 1)
	want = []string{string(alpha) + "=" + string(alpha) + " " + last, string(tele) + "=" + string(tele) + " " + last, thread.Target + "=Plans"}
	if got := names(); !slices.Equal(got, want) {
		t.Errorf("named at once: %q, want %q", got, want)
	}
}
