package setup

import (
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// listKind is how a list of places reads, so what deleting a tag may take out of it.
type listKind int

const (
	includes  listKind = iota // entries OR'd; empty is every room
	excludes                  // entries OR'd; empty is none
	ruleTerms                 // a [[tag]] rule: positives OR'd (none is every room), `not` terms AND'd
)

// cleanedList is one list a delete cleans, and how it reads.
type cleanedList struct {
	what string
	kind listKind
	get  func(*config.Settings) *[]string
}

// cleanedLists is every list naming places that a delete cleans, as the settings
// read them (tagged settings below).
func cleanedLists() []cleanedList {
	return []cleanedList{
		{"codes.include", includes, func(s *config.Settings) *[]string { return &s.Bridge.Codes.Include }},
		{"codes.exclude", excludes, func(s *config.Settings) *[]string { return &s.Bridge.Codes.Exclude }},
		{"agent.read.rooms", includes, func(s *config.Settings) *[]string { return &s.Bridge.Agent.Read.Rooms }},
		{"agent.read.except", excludes, func(s *config.Settings) *[]string { return &s.Bridge.Agent.Read.Except }},
		{"agent.write.rooms", includes, func(s *config.Settings) *[]string { return &s.Bridge.Agent.Write.Rooms }},
		{"agent.write.except", excludes, func(s *config.Settings) *[]string { return &s.Bridge.Agent.Write.Except }},
		{"agent.write.send", includes, func(s *config.Settings) *[]string { return &s.Bridge.Agent.Write.Send }},
		{"assist.rooms", includes, func(s *config.Settings) *[]string { return &s.Bridge.Assist.Rooms }},
		{"assist.except", excludes, func(s *config.Settings) *[]string { return &s.Bridge.Assist.Except }},
		{"display.tracked.rule.in", includes, func(s *config.Settings) *[]string { return &s.Bridge.Display.Tracked.Rules[0].In }},
		{"display.tracked.rule.except", excludes, func(s *config.Settings) *[]string { return &s.Bridge.Display.Tracked.Rules[0].Except }},
		{"filter.in", includes, func(s *config.Settings) *[]string { return &s.Bridge.Filters[0].In }},
		{"profile.include", includes, func(s *config.Settings) *[]string { return &s.Bridge.Profiles[0].Include }},
		{"profile.exclude", excludes, func(s *config.Settings) *[]string { return &s.Bridge.Profiles[0].Exclude }},
		{"profile.archive.rooms", ruleTerms, func(s *config.Settings) *[]string { return &s.Bridge.Profiles[0].Archive.Rooms }},
		{"tag.rule", ruleTerms, func(s *config.Settings) *[]string { return &s.Bridge.Tags[len(s.Bridge.Tags)-1].Rule }},
		// An rtl or ltr list sets the rooms it names; empty sets none.
		{"display.direction.rtl", excludes, func(s *config.Settings) *[]string { return &s.Device.Display.Direction.RTL }},
		{"display.direction.ltr", excludes, func(s *config.Settings) *[]string { return &s.Device.Display.Direction.LTR }},
	}
}

var (
	// gone is the deleted tag, as its entries spell it; place:X and space:X name
	// something else and stay.
	gone       = []string{"tag:X", "TAG:x ", " tag: X"}
	placeTerms = []string{
		"tag:Keep", "tag:Other", "space:Work", "space:X", "place:X", "protocol:" + domain.ProtocolWhatsApp.String(),
		"dm", "group", mx + "!a:x", mx + "!b:x",
	}
	ruleOnly = []string{"*", "unread", "not dm", "not unread", "not space:Work", "not tag:Keep", "not place:X"}
)

// Over random lists of every kind and random rooms: deleting tag X leaves each list
// matching exactly the rooms it matched with X empty, and takes out every entry naming
// X that can go. A command whose `in` named only X goes with it.
func TestDeletingATagKeepsWhatEveryListMatches(t *testing.T) {
	t.Parallel()
	lists := cleanedLists()
	for seed := range uint64(2000) {
		rng := rand.New(rand.NewPCG(seed, 59)) // #nosec G404 -- reproducible
		pick := func(from ...[]string) string {
			all := slices.Concat(from...)
			return all[rng.IntN(len(all))]
		}
		list := func(kind listKind) []string {
			var out []string
			for range rng.IntN(5) {
				switch {
				case rng.IntN(3) == 0:
					out = append(out, pick(gone))
				case kind == ruleTerms && rng.IntN(4) == 0:
					out = append(out, "not "+pick(gone))
				case kind == ruleTerms:
					out = append(out, pick(placeTerms, ruleOnly))
				default:
					out = append(out, pick(placeTerms))
				}
			}
			return out
		}
		var s config.Settings
		s.Bridge.Tags = []config.Tag{{Name: "X"}, {Name: "Keep", Rule: []string{"dm"}}, {Name: "Other"}}
		s.Bridge.Display.Tracked.Rules = []config.TrackedRule{{Words: []string{"deploy"}}}
		s.Bridge.Filters = []config.Filter{{Name: "f", Tag: "Keep"}}
		s.Bridge.Profiles = []config.Profile{{Name: "p"}}
		for _, l := range lists {
			*l.get(&s) = list(l.kind)
		}
		commands := []config.Command{{Name: "c"}}
		for i := range 4 {
			commands = append(commands, config.Command{Name: fmt.Sprintf("c%d", i), In: list(includes)})
		}
		s.Device.UserCommands = commands
		was := s.Clone()

		out := config.Settings{Bridge: DeleteTag(s.Bridge, pick([]string{"X", "x", " X "})), Device: DeleteTagOnDevice(s.Device, "x")}
		if !reflect.DeepEqual(s, was) {
			t.Fatalf("seed %d: deleting changed the settings it was given", seed)
		}
		if slices.ContainsFunc(out.Bridge.Tags, func(t config.Tag) bool { return sameTag(t.Name, "x") }) {
			t.Fatalf("seed %d: the tag is still there", seed)
		}
		bt, _, berr := Tags(s.Bridge)
		at, _, aerr := Tags(out.Bridge)
		if berr != nil || aerr != nil {
			t.Fatalf("seed %d: tags %v / %v", seed, berr, aerr)
		}
		for range 6 {
			room, state := randomRoom(rng)
			for _, l := range lists {
				before, after := *l.get(&s), *l.get(&out)
				if b, a := listMatches(l.kind, before, true, room, state), listMatches(l.kind, after, false, room, state); a != b {
					t.Fatalf("seed %d: %s %q became %q: %s matched %t, now %t", seed, l.what, before, after, room.ID, b, a)
				}
			}
			for _, c := range commands {
				i := slices.IndexFunc(out.Device.UserCommands, func(o config.Command) bool { return o.Name == c.Name })
				if i >= 0 && admits(c.In, room) != admits(out.Device.UserCommands[i].In, room) {
					t.Fatalf("seed %d: command in %q became %q", seed, c.In, out.Device.UserCommands[i].In)
				}
			}
			// Every tag's rule holds what it held with X empty.
			for _, name := range []string{"Keep", "Other"} {
				if b, a := bt.Has(name, room, state), at.Has(name, room, state); a != b {
					t.Fatalf("seed %d: tag %s held %s: %t, now %t", seed, name, room.ID, b, a)
				}
			}
		}
		for _, l := range lists {
			if left := leftOver(l.kind, *l.get(&out)); left != "" {
				t.Fatalf("seed %d: %s %q became %q: %s could go", seed, l.what, *l.get(&s), *l.get(&out), left)
			}
		}
		for _, c := range commands {
			only := len(c.In) > 0 && !slices.ContainsFunc(c.In, func(e string) bool { return !namesX(e) })
			i := slices.IndexFunc(out.Device.UserCommands, func(o config.Command) bool { return o.Name == c.Name })
			if (i < 0) != only {
				t.Fatalf("seed %d: command in %q deleted %t, want %t", seed, c.In, i < 0, only)
			}
			if i >= 0 && leftOver(includes, out.Device.UserCommands[i].In) != "" {
				t.Fatalf("seed %d: command in %q became %q", seed, c.In, out.Device.UserCommands[i].In)
			}
		}
	}
}

// randomRoom is a room and its state, in none of tag X: the deleted tag is empty.
func randomRoom(rng *rand.Rand) (domain.RoomFacts, domain.RoomState) {
	room := domain.RoomFacts{
		ID: []string{mx + "!a:x", mx + "!b:x", mx + "!c:x"}[rng.IntN(3)], Direct: rng.IntN(2) == 0,
		Protocol: []domain.Protocol{domain.ProtocolWhatsApp, domain.ProtocolTelegram}[rng.IntN(2)],
	}
	for _, space := range []string{"Work", "X"} {
		if rng.IntN(2) == 0 {
			room.Spaces = append(room.Spaces, space)
		}
	}
	for _, tag := range []string{"Keep", "Other"} {
		if rng.IntN(2) == 0 {
			room.Tags = append(room.Tags, tag)
		}
	}
	return room, domain.RoomState{Unread: rng.IntN(2) == 0}
}

// listMatches is whether a list of the kind matches the room. A rule is judged in a
// set beside tag Keep (dm) and, withX, an empty tag X, as a [[tag]] or a profile's
// archive.
func listMatches(kind listKind, list []string, withX bool, room domain.RoomFacts, state domain.RoomState) bool {
	switch kind {
	case includes:
		return admits(list, room)
	case excludes:
		return slices.ContainsFunc(list, room.Names)
	default:
		tags := []domain.Tag{{Name: "Keep", Rule: []string{"dm"}}, {Name: "Subject", Rule: list}}
		if withX {
			tags = append(tags, domain.Tag{Name: "X"})
		}
		set, _, err := domain.NewTagSet(tags)
		if err != nil {
			panic(err)
		}
		return set.Has("Subject", room, state)
	}
}

func admits(include []string, room domain.RoomFacts) bool {
	return domain.PlaceFilter{Include: include}.Admits(room)
}

func namesX(entry string) bool { return namesTag("X")(entry) }

// leftOver is an entry naming X that the list's kind lets go, or "".
func leftOver(kind listKind, list []string) string {
	for _, entry := range list {
		body, not := notTerm(entry)
		if !namesX(body) {
			continue
		}
		switch {
		case kind == excludes:
			return entry
		case kind == includes && slices.ContainsFunc(list, func(e string) bool { return !namesX(e) }):
			return entry
		case kind == ruleTerms && !not && slices.ContainsFunc(list, func(e string) bool { _, n := notTerm(e); return !n && !namesX(e) }):
			return entry
		case kind == ruleTerms && not && slices.ContainsFunc(list, func(e string) bool { b, n := notTerm(e); return !n || !namesX(b) }):
			return entry
		}
	}
	return ""
}

// indices are a path's array indices ("profile[0].include").
var indices = regexp.MustCompile(`\[\d+\]`)

// notPlaces are the lists of strings in either file that name no place, so keep a
// tag:X entry: everything else a delete must clean. A new list naming places fails
// TestADeleteCleansEveryListOfPlaces until it is cleaned or named here.
var notPlaces = []string{
	"bridge_contacts", "tag.picked", "tag.excluded", "tag.people", "tag.exclusive", "filter.words",
	"filter.lang", "filter.not_lang", "filter.script", "filter.moves", "display.tracked.words",
	"display.tracked.rule.words", "display.tracked.rule.from", "display.identity.ids",
	"profile.people.exclude", "spell.dictionaries", "display.rooms.sort", "display.filing_spaces", // spaces only
	"assist.rewrite.stop", "assist.summary.stop", "assist.thread_name.stop", "assist.todo.stop",
	"display.rooms.rule.sort", "display.reactions.static", "spell.declined", "spell.frequencies_declined",
	"complete.sources", "command.needs", "command.secrets", "remote.listen",
}

// Every list of strings in either file holding tag:X and another entry loses tag:X on
// delete, unless it names no place: no list naming places is missed. Settings of one
// entry keep it (matching nothing), so only lists are filled.
func TestADeleteCleansEveryListOfPlaces(t *testing.T) {
	t.Parallel()
	var s config.Settings
	var fill func(v reflect.Value)
	fill = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Struct:
			for i := range v.NumField() {
				if v.Type().Field(i).IsExported() {
					fill(v.Field(i))
				}
			}
		case reflect.Slice:
			if v.Type().Elem().Kind() == reflect.String {
				v.Set(reflect.MakeSlice(v.Type(), 2, 2))
				v.Index(0).SetString("tag:X")
				v.Index(1).SetString("tag:Keep")
				return
			}
			if v.Type().Elem().Kind() == reflect.Struct {
				v.Set(reflect.MakeSlice(v.Type(), 1, 1))
				fill(v.Index(0))
			}
		default:
		}
	}
	fill(reflect.ValueOf(&s).Elem())
	s.Bridge.Tags = append(s.Bridge.Tags, config.Tag{Name: "X"}, config.Tag{Name: "Keep"})
	out := config.Settings{Bridge: DeleteTag(s.Bridge, "X"), Device: DeleteTagOnDevice(s.Device, "X")}
	var kept []string
	for _, warning := range DanglingTags(out) {
		path, _, _ := strings.Cut(warning, ":")
		path = indices.ReplaceAllString(path, "")
		kept = append(kept, path)
		if !slices.Contains(notPlaces, path) {
			t.Errorf("%s: a list naming places kept the deleted tag", warning)
		}
	}
	for _, path := range notPlaces {
		if !slices.Contains(kept, path) {
			t.Errorf("notPlaces names %s, which is no list in either file", path)
		}
	}
}

// The case that broke a client: a tag named in [display.direction] and in commands'
// `in` is deleted. Both files are written and read back as a restart does, and load;
// the command naming only the tag is gone, the other keeps its other place. A second
// device, whose file this delete never touched, still loads, and is told what it names.
func TestADeletedTagStillNamedOnADeviceLoads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bridge, phone, laptop := dir+"/config.toml", dir+"/device.toml", dir+"/laptop.toml"
	cfg := config.Config{
		Matrix:   config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://x", User: "@me:x"}}},
		Tags:     []config.Tag{{Name: "Work", Rule: []string{"space:Work"}}, {Name: "Home", Rule: []string{"not tag:Work"}}},
		Profiles: []config.Profile{{Name: "default", Include: []string{"*"}}},
	}
	cfg.Notifications.Rules = []config.Rule{{Match: "tag:Work", Show: "all"}}
	device := `[display.direction]
rtl = ["dm", "tag:Work"]

[[command]]
name = "standup"
in = ["tag:Work"]

[[command]]
name = "zoom"
in = ["tag:Work", "group"]
`
	for _, path := range []string{phone, laptop} {
		if err := os.WriteFile(path, []byte(device), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(bridge, cfg); err != nil {
		t.Fatal(err)
	}
	dev, err := config.LoadDevice(phone)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Save(bridge, DeleteTag(cfg, "work")); err != nil {
		t.Fatal(err)
	}
	if _, err = config.UpdateDevice(phone, dev, DeleteTagOnDevice(dev, "work")); err != nil {
		t.Fatal(err)
	}

	cfg, err = config.Load(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("the bridge's config is refused: %v", err)
	}
	if got := cfg.Tags[slices.IndexFunc(cfg.Tags, func(t config.Tag) bool { return t.Name == "Home" })].Rule; !slices.Equal(got, []string{"not tag:Work"}) {
		t.Errorf("Home's rule = %q, want its one term kept (empty would hold no room)", got)
	}
	for _, path := range []string{phone, laptop} {
		dev, err := config.LoadDevice(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateDevice(dev); err != nil {
			t.Fatalf("%s is refused: %v", path, err)
		}
		warned := DanglingTags(config.Settings{Bridge: cfg, Device: dev})
		if path == laptop {
			if len(warned) != 5 { // its three lists, Home's rule and the notification rule
				t.Errorf("the laptop is warned of %q", warned)
			}
			continue
		}
		names := make([]string, 0, len(dev.UserCommands))
		for _, c := range dev.UserCommands {
			names = append(names, c.Name)
		}
		if !slices.Equal(dev.Display.Direction.RTL, []string{"dm"}) ||
			!slices.Equal(names, []string{"zoom"}) || !slices.Equal(dev.UserCommands[0].In, []string{"group"}) {
			t.Errorf("phone: direction %+v, commands %+v", dev.Display.Direction, dev.UserCommands)
		}
		want := []string{"notifications.rule[0].match: tag:Work names no [[tag]]", "tag[0].rule: not tag:Work names no [[tag]]"}
		if !slices.Equal(warned, want) {
			t.Errorf("phone warned of %q, want %q", warned, want)
		}
	}
}
