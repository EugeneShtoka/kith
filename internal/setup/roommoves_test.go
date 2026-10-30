package setup

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp chat known by the person's LID, merged into their number's. Invented.
const (
	movedLID    = "whatsapp:15550100/88001234567@lid"
	movedNumber = "whatsapp:15550100/15550199@s.whatsapp.net"
)

// Over random configs naming the LID's chat in every place an entry names a room (a
// profile's lists, a tag's picked and rule, a room's name, the agent's rooms), each way
// it can be written, among decoys: every reference names the number's chat afterwards,
// written the same way, and no decoy changes.
func TestAMergedChatIsNamedByTheChatItJoined(t *testing.T) {
	t.Parallel()
	refs := map[string]string{
		movedLID:                      movedNumber,
		"room:" + movedLID:            "room:" + movedNumber,
		"Room:" + movedLID:            "Room:" + movedNumber,
		"not room:" + movedLID:        "not room:" + movedNumber,
		"not " + movedLID:             "not " + movedNumber,
		"not  room:" + movedLID + " ": "not room:" + movedNumber,
	}
	decoys := []string{movedLID + "x", "room:Dana", movedNumber, "whatsapp:15550100/99001234567@lid", "dm", "tag:" + movedLID}
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	moved := map[domain.RoomID]domain.RoomID{movedLID: movedNumber}
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 53)) // #nosec G404 -- reproducible
		pick := func() string {
			if rng.IntN(2) == 0 {
				return keys[rng.IntN(len(keys))]
			}
			return decoys[rng.IntN(len(decoys))]
		}
		var cfg config.Config
		cfg.Profiles = []config.Profile{{Name: "work", Include: []string{pick(), pick()}, Exclude: []string{pick()}}}
		cfg.Tags = []config.Tag{{Name: "Family", Picked: []string{pick()}, Excluded: []string{pick()}, Rule: []string{pick()}}}
		cfg.Display.Names = []config.DisplayName{{Target: pick(), Name: "Dana"}}
		cfg.Agent.Read.Rooms = []string{pick()}
		cfg.Notifications.Rules = []config.Rule{{Match: pick()}}

		out := MoveRooms(cfg, moved)
		var was, now []string
		allStrings(reflect.ValueOf(cfg), &was)
		allStrings(reflect.ValueOf(out), &now)
		if len(was) != len(now) {
			t.Fatalf("seed %d: %d strings became %d", seed, len(was), len(now))
		}
		for i := range was {
			want, ref := refs[was[i]]
			if !ref {
				want = was[i]
			}
			if now[i] != want {
				t.Errorf("seed %d: %q became %q, want %q", seed, was[i], now[i], want)
			}
			if strings.Contains(now[i], movedLID) && ref {
				t.Errorf("seed %d: %q still names the LID's chat", seed, now[i])
			}
		}
	}
}

// allStrings appends every exported string reachable from v, in a fixed order.
func allStrings(v reflect.Value, out *[]string) {
	switch v.Kind() {
	case reflect.String:
		*out = append(*out, v.String())
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			allStrings(v.Elem(), out)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			allStrings(v.Index(i), out)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				allStrings(v.Field(i), out)
			}
		}
	default:
	}
}
