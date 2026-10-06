package setup

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Over random tags (rules, picks and exclusions by ID or by name), rooms of two
// Telegram accounts and of WhatsApp, a random folder of one account and a random
// choice: taking Telegram's makes the account's rooms in the tag exactly the folder's,
// merging adds the folder's and takes none away, keeping changes nothing — and no
// other room, nor any other tag, changes; the diff says exactly what taking would do.
func TestAFolderIsCopiedIntoItsTagAsChosen(t *testing.T) {
	t.Parallel()
	names := []string{"Mom", "Dana", "Work"}
	terms := []string{"*", "dm", "group", "space:Family", "protocol:telegram", "protocol:whatsapp", "room:Mom", "not dm", "not room:Dana"}
	owner := domain.AccountRooms(domain.ProtocolTelegram, "42")
	for seed := range uint64(1500) {
		rng := rand.New(rand.NewPCG(seed, 43)) // #nosec G404 -- reproducible
		var rooms []domain.RoomFacts
		for i := range 3 + rng.IntN(6) {
			id, network := fmt.Sprintf("telegram:42/%d", i+1), domain.ProtocolTelegram
			switch rng.IntN(3) {
			case 1:
				id = fmt.Sprintf("telegram:43/%d", i+1)
			case 2:
				id, network = fmt.Sprintf("whatsapp:44/%d@g.us", i+1), domain.ProtocolWhatsApp
			}
			r := domain.RoomFacts{ID: id, Name: names[rng.IntN(len(names))], Direct: rng.IntN(2) == 0, Protocol: network}
			if rng.IntN(2) == 0 {
				r.Spaces = []string{"Family"}
			}
			rooms = append(rooms, r)
		}
		entry := func() string {
			if rng.IntN(2) == 0 {
				return "room:" + names[rng.IntN(len(names))]
			}
			return rooms[rng.IntN(len(rooms))].ID
		}
		var cfg config.Config
		for _, name := range []string{"Friends", "Other"} {
			if name == "Friends" && rng.IntN(3) == 0 {
				continue // a folder no tag is named after: one is made
			}
			tag := config.Tag{Name: name}
			for range rng.IntN(3) {
				tag.Rule = append(tag.Rule, terms[rng.IntN(len(terms))])
			}
			for range rng.IntN(3) {
				tag.Picked = append(tag.Picked, entry())
			}
			for range rng.IntN(3) {
				tag.Excluded = append(tag.Excluded, entry())
			}
			cfg.Tags = append(cfg.Tags, tag)
		}
		var folder domain.Grouping
		folder.Name = "friends"
		for _, r := range rooms {
			if owner.Owns(domain.RoomID(r.ID)) && rng.IntN(2) == 0 {
				folder.Rooms = append(folder.Rooms, domain.RoomID(r.ID))
			}
		}
		choice := domain.GroupingChoice(rng.IntN(3))
		before, _, err := Tags(cfg)
		if err != nil {
			t.Fatal(err)
		}
		diff := domain.DiffGroupings([]domain.Grouping{folder}, before, rooms, owner)[0]
		got, err := ApplyGroupings(cfg, owner, []domain.Grouping{folder}, map[string]domain.GroupingChoice{"friends": choice}, rooms)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		after, _, err := Tags(got)
		if err != nil {
			t.Fatalf("seed %d: the result does not load: %v", seed, err)
		}
		holds := func(set domain.TagSet, name string, r domain.RoomFacts) bool {
			i, ok := set.Index(name)
			return ok && set.HasAt(i, r, domain.RoomState{})
		}
		where := func(r domain.RoomFacts) string {
			return fmt.Sprintf("seed %d choice %d: %s (%s)", seed, choice, r.ID, r.Name)
		}
		for _, r := range rooms {
			was, is := holds(before, "Friends", r), holds(after, "Friends", r)
			in := containsID(folder.Rooms, r.ID)
			mine := owner.Owns(domain.RoomID(r.ID))
			switch {
			case choice == domain.KeepTags || !mine:
				if was != is {
					t.Fatalf("%s: Friends held %v, now %v; it was not the copy's to change", where(r), was, is)
				}
			case choice == domain.TakeNetworks && is != in:
				t.Fatalf("%s: taking Telegram's, Friends holds %v, the folder %v", where(r), is, in)
			case choice == domain.MergeIn && is != (was || in):
				t.Fatalf("%s: merging, Friends holds %v (held %v, folder %v)", where(r), is, was, in)
			}
			if holds(before, "Other", r) != holds(after, "Other", r) {
				t.Fatalf("%s: the copy changed another tag", where(r))
			}
			if !mine && (containsID(diff.Add, r.ID) || containsID(diff.Remove, r.ID)) {
				t.Fatalf("%s: the diff names a room not the account's", where(r))
			}
			if mine && (containsID(diff.Add, r.ID) != (in && !was) || containsID(diff.Remove, r.ID) != (!in && was)) {
				t.Fatalf("%s: the diff (add %v, remove %v) is not what taking does", where(r), diff.Add, diff.Remove)
			}
		}
	}
}
