package setup

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Over random configs naming a tag in every kind of setting, in any case, after `not`,
// beside decoys that only look like it: renaming it changes no room's membership in
// any tag, leaves no reference to the old name, touches no decoy, and the config is
// as valid as it was.
func TestRenamingATagRewritesEveryReferenceAndNothingElse(t *testing.T) {
	t.Parallel()
	refs := []string{"tag:T0", "TAG:t0", "not tag:T0", "not  Tag:T0 "}
	decoys := []string{"tag:T0x", "room:tag:T0", "T0", "tag:T1", "space:T0"}
	rooms := []domain.RoomFacts{
		{ID: "!a:x", Name: "tag:T0", Spaces: []string{"T0"}},
		{ID: "!b:x", Name: "Bravo", Direct: true},
		{ID: "!c:x", Name: "T0", Spaces: []string{"Work"}},
	}
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 31)) // #nosec G404 -- reproducible
		ref := func() string { return refs[rng.IntN(len(refs))] }
		decoy := func() string { return decoys[rng.IntN(len(decoys))] }
		positive := func() string { return refs[rng.IntN(2)] } // a place, not a `not` term
		cfg := config.Config{Homeserver: "https://x", User: "@me:x"}
		cfg.Tags = []config.Tag{
			{Name: "T0", Rule: []string{"dm", "room:tag:T0"}, Picked: []string{"!c:x"}},
			{Name: "T1", Rule: []string{ref(), "space:Work"}},
			{Name: "T2", Rule: []string{"*", ref()}, Excluded: []string{"!b:x"}},
		}
		cfg.Notifications.Rules = []config.Rule{{Match: positive()}, {Match: "space:T0"}}
		cfg.Codes.Include = []string{positive(), decoy()}
		cfg.Agent.Read.Rooms = []string{positive(), "room:tag:T0"}
		cfg.Display.Priority = []string{positive(), "T0"}
		cfg.Display.Rail.Order = []string{positive(), "T0", "-"}
		cfg.Display.Rail.Hidden = []string{"t0", decoy()}
		cfg.Display.SpaceRules = []config.SpaceRule{{Space: positive(), FirstNameOnly: true}}
		cfg.Display.Emoji.Extra = map[string]string{"tag:T0": "x"} // a key, not a value

		out := RenameTag(cfg, "t0", "Family")
		before, _, errBefore := Tags(cfg)
		after, _, errAfter := Tags(out)
		if errBefore != nil || errAfter != nil {
			t.Fatalf("seed %d: tags %v / %v", seed, errBefore, errAfter)
		}
		for _, room := range rooms {
			for _, state := range []domain.RoomState{{}, {Unread: true}} {
				for i := range before.Len() {
					if b, a := before.HasAt(i, room, state), after.HasAt(i, room, state); a != b {
						t.Fatalf("seed %d: %s in tag %d was %t, after renaming %t", seed, room.ID, i, b, a)
					}
				}
			}
		}
		if out.Tags[0].Name != "Family" || after.At(0).Name != "Family" {
			t.Fatalf("seed %d: the tag itself is %q", seed, out.Tags[0].Name)
		}
		var left []string
		rewriteStrings(reflect.ValueOf(&out).Elem(), func(s string) string {
			if renamedReference(s, "T0", "?") != s {
				left = append(left, s)
			}
			return s
		})
		if len(left) > 0 {
			t.Fatalf("seed %d: references to the old name left: %q", seed, left)
		}
		for _, d := range []string{"room:tag:T0", "space:T0"} {
			if !slices.Contains(out.Agent.Read.Rooms, d) && slices.Contains(cfg.Agent.Read.Rooms, d) {
				t.Fatalf("seed %d: decoy %q rewritten", seed, d)
			}
		}
		if out.Tags[0].Rule[1] != "room:tag:T0" || out.Notifications.Rules[1].Match != "space:T0" ||
			out.Display.Priority[1] != "T0" || out.Display.Rail.Order[1] != "T0" || out.Display.Emoji.Extra["tag:T0"] != "x" {
			t.Fatalf("seed %d: a decoy changed: %+v", seed, out)
		}
		if out.Display.Rail.Hidden[0] != "Family" {
			t.Fatalf("seed %d: the rail's bare name %q not renamed", seed, out.Display.Rail.Hidden[0])
		}
		if (Validate(cfg) == nil) != (Validate(out) == nil) {
			t.Fatalf("seed %d: valid before %v, after %v", seed, Validate(cfg), Validate(out))
		}
		if cfg.Tags[0].Name != "T0" || cfg.Display.Rail.Hidden[0] != "t0" {
			t.Fatalf("seed %d: renaming changed the config it was given", seed)
		}
	}
}

// Deleting a tag drops it from the rail's lists and priority; a rule naming it is
// left for the person, and the config then says where.
func TestDeletingATag(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Homeserver: "https://x", User: "@me:x", Tags: []config.Tag{{Name: "Old"}, {Name: "Keep"}}}
	cfg.Display.Rail.Order = []string{"tag:Old", "Work", "tag:Keep"}
	cfg.Display.Rail.Hidden = []string{"old"}
	cfg.Display.Priority = []string{"tag:old", "Old"}
	out := DeleteTag(cfg, "OLD")
	if len(out.Tags) != 1 || out.Tags[0].Name != "Keep" {
		t.Errorf("tags = %+v", out.Tags)
	}
	if !slices.Equal(out.Display.Rail.Order, []string{"Work", "tag:Keep"}) || len(out.Display.Rail.Hidden) != 0 ||
		!slices.Equal(out.Display.Priority, []string{"Old"}) { // a space named Old stays
		t.Errorf("rail %+v, priority %v", out.Display.Rail, out.Display.Priority)
	}
	if err := Validate(out); err != nil {
		t.Errorf("valid config refused: %v", err)
	}
	cfg.Notifications.Rules = []config.Rule{{Match: "tag:Old"}}
	if err := Validate(DeleteTag(cfg, "Old")); err == nil {
		t.Error("a rule naming the deleted tag was accepted")
	} else if !strings.Contains(strings.ToLower(err.Error()), "tag:old") {
		t.Errorf("error %v does not say what names it", err)
	}
}

// A network's archive goes with its tag: renamed with it (the default name too), and
// let go when it is deleted.
func TestANetworksArchiveGoesWithItsTag(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	cfg.Tags = []config.Tag{{Name: "Archived"}, {Name: "Old"}}
	cfg.WhatsApp.Archive.Tag = "Old"
	renamed := RenameTag(cfg, "archived", "Shelf")
	if renamed.Telegram.Archive.TagName() != "Shelf" || renamed.WhatsApp.Archive.TagName() != "Old" {
		t.Errorf("after renaming Archived: telegram %q, whatsapp %q", renamed.Telegram.Archive.TagName(), renamed.WhatsApp.Archive.TagName())
	}
	deleted := DeleteTag(cfg, "Old")
	if deleted.WhatsApp.Archive.Tag != "" {
		t.Errorf("after deleting Old, WhatsApp's archive is %q", deleted.WhatsApp.Archive.Tag)
	}
}

// Each followed network's archive is the tag it names; a tag named that does not
// exist is refused, the default name with no such tag follows nothing, and a network
// not followed is left out.
func TestArchivesAreTheTagsTheyName(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	cfg.Tags = []config.Tag{{Name: "Old"}}
	off := false
	cfg.Telegram.Archive.Tag = "old"
	cfg.WhatsApp.Archive.Follow = &off
	tags, _, err := Tags(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Archives(cfg, tags)
	if err != nil || len(got) != 1 || got[domain.ProtocolTelegram] != "old" {
		t.Errorf("archives = %v, %v", got, err)
	}
	cfg.WhatsApp.Archive.Follow = nil // the default "Archived", which no tag is
	if got, err := Archives(cfg, tags); err != nil || len(got) != 1 {
		t.Errorf("with the default name and no such tag: %v, %v", got, err)
	}
	cfg.WhatsApp.Archive.Tag = "Shelf"
	if _, err := Archives(cfg, tags); err == nil {
		t.Error("a tag that does not exist was taken")
	}
}

// Over random rooms (names shared), tags with rules (`not` terms, references between
// them), picks and exclusions by ID or by name, and unread state: combining B into A
// leaves A holding exactly what A or B held, no room more or less.
func TestCombiningTagsHoldsExactlyWhatEitherHeld(t *testing.T) {
	t.Parallel()
	names := []string{"Mom", "Standup", "Dana"}
	terms := []string{"*", "dm", "group", "unread", "space:Work", "room:Mom", "not dm", "not unread", "not room:Dana", "tag:C", "tag:B", "tag:A"}
	for seed := range uint64(3000) {
		rng := rand.New(rand.NewPCG(seed, 41)) // #nosec G404 -- reproducible
		var rooms []domain.RoomFacts
		state := map[string]domain.RoomState{}
		for i := range 2 + rng.IntN(5) {
			r := domain.RoomFacts{ID: fmt.Sprintf("!r%d:x", i), Name: names[rng.IntN(len(names))], Direct: rng.IntN(2) == 0}
			if rng.IntN(2) == 0 {
				r.Spaces = []string{"Work"}
			}
			rooms = append(rooms, r)
			state[r.ID] = domain.RoomState{Unread: rng.IntN(2) == 0}
		}
		entry := func() string {
			if rng.IntN(2) == 0 {
				return "room:" + names[rng.IntN(len(names))]
			}
			return rooms[rng.IntN(len(rooms))].ID
		}
		var cfg config.Config
		for _, name := range []string{"A", "B", "C"} {
			tag := config.Tag{Name: name}
			for range rng.IntN(3) {
				if term := terms[rng.IntN(len(terms))]; term != "tag:"+name {
					tag.Rule = append(tag.Rule, term)
				}
			}
			for range rng.IntN(3) {
				tag.Picked = append(tag.Picked, entry())
			}
			for range rng.IntN(3) {
				tag.Excluded = append(tag.Excluded, entry())
			}
			cfg.Tags = append(cfg.Tags, tag)
		}
		set, _, err := Tags(cfg)
		if err != nil {
			continue // a cycle the rules made: not this test's subject
		}
		held := func(tags domain.TagSet) func(string, domain.RoomFacts) bool {
			return func(name string, r domain.RoomFacts) bool {
				i, ok := tags.Index(name)
				return ok && tags.HasAt(i, r, state[r.ID])
			}
		}
		before := held(set)
		combined := CombineTags(cfg, "B", "A", rooms, before)
		after, _, err := Tags(combined)
		if err != nil {
			t.Fatalf("seed %d: the combined config does not load: %v\n%+v", seed, err, combined.Tags)
		}
		now := held(after)
		for _, r := range rooms {
			if want := before("A", r) || before("B", r); now("A", r) != want {
				t.Fatalf("seed %d: %s (%s) held %v after, %v before (A %v, B %v)\nbefore %+v\nafter  %+v",
					seed, r.ID, r.Name, now("A", r), want, before("A", r), before("B", r), cfg.Tags, combined.Tags)
			}
		}
	}
}

// Combined, two tags without `not` terms keep a rule: the two joined, a reference of
// one to the other dropped (the tag would name itself), and every reference elsewhere —
// another tag's rule, the rail, a network's archive — naming the combined tag.
func TestCombinedTagsKeepTheirRules(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	cfg.Tags = []config.Tag{
		{Name: "Friends", Rule: []string{"space:Work", "tag:Mates"}, Exclusive: true},
		{Name: "Mates", Rule: []string{"dm"}, Picked: []string{"!x:y"}},
		{Name: "Quiet", Rule: []string{"tag:Mates"}},
	}
	cfg.Display.Rail.Order = []string{"tag:Mates", "tag:Friends"}
	cfg.Telegram.Archive.Tag = "Mates"
	got := CombineTags(cfg, "mates", "Friends", nil, func(string, domain.RoomFacts) bool { return false })
	if len(got.Tags) != 2 || !slices.Equal(got.Tags[0].Rule, []string{"space:Work", "dm"}) || !slices.Equal(got.Tags[0].Picked, []string{"!x:y"}) || !got.Tags[0].Exclusive {
		t.Errorf("combined = %+v", got.Tags)
	}
	if !slices.Equal(got.Tags[1].Rule, []string{"tag:Friends"}) || !slices.Equal(got.Display.Rail.Order, []string{"tag:Friends"}) || got.Telegram.Archive.Tag != "Friends" {
		t.Errorf("references: Quiet %v, rail %v, archive %q", got.Tags[1].Rule, got.Display.Rail.Order, got.Telegram.Archive.Tag)
	}
}

// [storage]'s numbers are -1 (every message) or at least one, and a rule names a place.
func TestStorageRulesAreChecked(t *testing.T) {
	t.Parallel()
	n := func(v int) *int { return &v }
	for name, c := range map[string]struct {
		st config.Storage
		ok bool
	}{
		"default":        {config.Storage{}, true},
		"a number":       {config.Storage{MessagesPerRoom: n(500)}, true},
		"zero":           {config.Storage{MessagesPerRoom: n(0)}, false},
		"a rule":         {config.Storage{Rules: []config.StorageRule{{Match: "tag:Archived", Messages: n(10)}}}, true},
		"a rule of zero": {config.Storage{Rules: []config.StorageRule{{Match: "tag:Archived", Messages: n(0)}}}, false},
		"no number":      {config.Storage{Rules: []config.StorageRule{{Match: "tag:Archived"}}}, false},
		"no place":       {config.Storage{Rules: []config.StorageRule{{Match: "", Messages: n(10)}}}, false},
	} {
		if _, _, err := KeepRules(c.st); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
