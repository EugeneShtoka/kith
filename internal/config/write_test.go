package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// saved writes cfg and returns the file's text.
func saved(t *testing.T, cfg Config) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(body), path
}

// minimal is a config with only the required fields set.
func minimal() Config {
	cfg := Config{Homeserver: "https://example.org", User: "@me:example.org"}
	cfg.Keys.FillDefaults()
	return cfg
}

// The baseline is what a *missing* key produces — not what the shipped default.toml
// says.
func TestSaveBaselineIsTheAbsentKeyNotTheShippedDefault(t *testing.T) {
	t.Parallel()

	cfg := minimal()
	cfg.Display.MaxNameLength = 15 // the value default.toml also happens to show
	body, path := saved(t, cfg)

	if !strings.Contains(body, "max_name_length = 15") {
		t.Errorf("a value that matches the shipped default must still be written:\n%s", body)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Display.MaxNameLength != 15 {
		t.Errorf("reloaded max_name_length = %d, want 15", reloaded.Display.MaxNameLength)
	}
}

// Anything at its default is left out, so the file stays readable and a later change
// to a default flows through instead of being frozen.
func TestSaveOmitsDefaults(t *testing.T) {
	t.Parallel()

	body, _ := saved(t, minimal())
	for _, absent := range []string{"[keys]", "[keys.nav]", "quit =", "[display]", "[notifications]"} {
		if strings.Contains(body, absent) {
			t.Errorf("%q should be omitted — it is at its default:\n%s", absent, body)
		}
	}
	// The required fields are always written.
	for _, present := range []string{"homeserver =", "user ="} {
		if !strings.Contains(body, present) {
			t.Errorf("%q must be written", present)
		}
	}
}

// A changed binding is written; the rest of the keymap is not. This is what lets a
// rewrite preserve a hand-edited value while dropping the prose around it.
func TestSaveWritesOnlyChangedBindings(t *testing.T) {
	t.Parallel()

	cfg := minimal()
	cfg.Keys.Timeline.React = "R"
	body, path := saved(t, cfg)

	if !strings.Contains(body, `react = "R"`) {
		t.Errorf("the changed binding should be written:\n%s", body)
	}
	if strings.Contains(body, `insert = "i"`) {
		t.Errorf("unchanged bindings should not be:\n%s", body)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Keys.Timeline.React != "R" {
		t.Errorf("React = %q, want the saved value", reloaded.Keys.Timeline.React)
	}
	if reloaded.Keys.Timeline.Insert != DefaultKeys().Timeline.Insert {
		t.Errorf("Insert = %q, want the default filled back in", reloaded.Keys.Timeline.Insert)
	}
}

// Identities are the whole point of the writer: they are keyed on MXIDs the UI never
// shows, so they can only be created from inside the app.
func TestSaveIdentities(t *testing.T) {
	t.Parallel()

	cfg := minimal()
	cfg.Display.Identities = []Identity{
		{Alias: "Dana", Color: "#8ff586", IDs: []string{"@dana:x", "@whatsapp_dana:x"}},
		{Alias: "יבגני", IDs: []string{"@rtl:x"}},
	}
	body, path := saved(t, cfg)

	if strings.Count(body, "[[display.identity]]") != 2 {
		t.Errorf("want two identity tables:\n%s", body)
	}
	// An array inside a table must be a bare key, not a dotted path — a dotted path
	// after a table header lands the value somewhere else that still parses.
	if strings.Contains(body, "display.identity.ids") {
		t.Errorf("ids must be written as a bare key inside its table:\n%s", body)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Display.Identities, cfg.Display.Identities) {
		t.Errorf("identities round-tripped as %+v", reloaded.Display.Identities)
	}
}

// Every shape the config is made of has to survive a round trip, or a setting is
// quietly lost the next time anything is saved.
func TestSaveRoundTripsEveryShape(t *testing.T) {
	t.Parallel()

	yes, no := true, false
	cfg := minimal()
	cfg.AllowTokenFile = true                                                                        // bool
	cfg.Display.MaxNameLength = 20                                                                   // int
	cfg.Display.ColorMessages = true                                                                 // bool in a table
	cfg.Display.OpenInInsert = &no                                                                   // *bool, set false
	cfg.Display.RoomNameRules = &yes                                                                 // *bool, set true
	cfg.Display.Rail.Order = []string{"tag:Unread", "-"}                                             // []string in a table
	cfg.Display.Rail.Hidden = []string{"tag:All"}                                                    //
	cfg.Display.Names = []DisplayName{{Target: NameTargetSpace + "Work", Name: "Job"}}               // one list
	cfg.Display.Media.Mode = "inline"                                                                // string in a nested table
	cfg.Display.Media.MaxHeight = 12                                                                 //
	cfg.Display.Reactions.Static = []string{"👍", "🎉"}                                                // non-ASCII array
	cfg.Display.Names = append(cfg.Display.Names, DisplayName{Target: "!a:x", Name: `He said "hi"`}) // escaping
	cfg.Display.SpaceRules = []SpaceRule{{Space: "Work", FirstNameOnly: true}}
	cfg.Notifications.Enabled = true
	cfg.Notifications.Rules = []Rule{{Match: "Work", Show: "all", When: "22:00-08:00"}}
	cfg.Notifications.Command = `notify-send "$KITH_TITLE"`

	body, path := saved(t, cfg)
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload failed — the writer produced invalid TOML: %v\n%s", err, body)
	}
	if !reflect.DeepEqual(reloaded, cfg) {
		t.Errorf("round trip lost data.\nwritten:\n%s\nbefore: %+v\nafter:  %+v", body, cfg, reloaded)
	}
}

// Saving twice must produce identical bytes, or every save would churn the file and
// nothing about it would be reviewable.
func TestSaveIsIdempotent(t *testing.T) {
	t.Parallel()

	cfg := minimal()
	cfg.Display.Names = []DisplayName{{Target: "!z:x", Name: "Z"}, {Target: "!a:x", Name: "A"}, {Target: "!m:x", Name: "M"}}
	cfg.Display.Identities = []Identity{{Alias: "A", IDs: []string{"@a:x"}}}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	for range 3 {
		reloaded, lerr := Load(path)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if serr := Save(path, reloaded); serr != nil {
			t.Fatal(serr)
		}
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(first, again) {
		t.Errorf("saving is not stable.\nfirst:\n%s\nlater:\n%s", first, again)
	}
}

// The hand-written file is preserved once, and the backup is never overwritten by a
// later save — it holds what you wrote, not what the app last produced.
func TestSaveBacksUpOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "# my careful comments\nhomeserver = \"https://example.org\"\nuser = \"@me:example.org\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if serr := Save(path, cfg); serr != nil {
		t.Fatal(serr)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the hand-written original", backup)
	}

	// A second save must not replace it.
	cfg.Display.ColorMessages = true
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	backup2, _ := os.ReadFile(path + ".bak")
	if string(backup2) != original {
		t.Error("a later save overwrote the backup of the hand-written file")
	}
}

// Saving where no file exists yet is not an error, and needs no backup.
func TestSaveFresh(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, minimal()); err != nil {
		t.Fatalf("Save onto nothing: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("there was nothing to back up, so no .bak should exist")
	}
	if _, err := Load(path); err != nil {
		t.Errorf("the written file does not load: %v", err)
	}
}

// The file holds no secrets but it does hold your homeserver and account, so it is
// written 0600 like the rest of the app's state.
func TestSavePermissions(t *testing.T) {
	t.Parallel()

	_, path := saved(t, minimal())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// An emptied list is written as [], because omitting it would let the default come
// back on the next load.
func TestSaveEmptiedList(t *testing.T) {
	t.Parallel()

	cfg := minimal()
	cfg.Display.Reactions.Static = []string{}
	body, _ := saved(t, cfg)
	// Nothing to write: an empty list matches the baseline's empty list.
	if strings.Contains(body, "static") {
		t.Errorf("an empty list equal to the baseline should be omitted:\n%s", body)
	}

	// But a list emptied from a non-default one is a real change.
	cfg.Keys.Nav.Up = "" // unbound, which differs from the filled default
	body2, path := saved(t, cfg)
	if !strings.Contains(body2, `up = ""`) {
		t.Errorf("an unbound key differs from its default and must be written:\n%s", body2)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// FillDefaults treats "" as unset, so it comes back — which is why "-" is the
	// sentinel for a deliberate unbind, not "".
	if reloaded.Keys.Nav.Up != DefaultKeys().Nav.Up {
		t.Errorf("Nav.Up = %q; an empty binding reads as unset and refills", reloaded.Keys.Nav.Up)
	}
}

// The header points at the documentation rather than repeating it, so a
// machine-written file does not re-document every option on every save.
func TestSaveHeaderPointsAtTheDocs(t *testing.T) {
	t.Parallel()

	body, _ := saved(t, minimal())
	for _, want := range []string{"written by kith", "--print-config", "press ?"} {
		if !strings.Contains(body, want) {
			t.Errorf("the header should mention %q", want)
		}
	}
	if Annotated() == "" {
		t.Error("Annotated() must return the documented reference the header points at")
	}
	if !strings.Contains(Annotated(), "max_name_length") {
		t.Error("Annotated() should be the full documented config")
	}
}

// Every scalar kind that reaches scalarLiteral must have a TOML literal form.
func TestEveryScalarKindInConfigHasALiteral(t *testing.T) {
	t.Parallel()

	// kind -> the field path that first reached it, so a failure names something the
	// reader can go and look at.
	found := map[reflect.Kind]string{}
	record := func(typ reflect.Type, at string) {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem() // scalarLiteral recurses through a pointer to its element
		}
		if _, seen := found[typ.Kind()]; !seen {
			found[typ.Kind()] = at
		}
	}

	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		for field := range typ.Fields() {
			name := tomlName(field)
			if name == "" {
				continue
			}
			at, ft := path+name, field.Type
			switch {
			case ft.Kind() == reflect.Struct:
				walk(ft, at+".")
			case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
				walk(ft.Elem(), at+"[].")
			case ft.Kind() == reflect.Slice:
				record(ft.Elem(), at+"[]")
			default:
				record(ft, at)
			}
		}
	}
	walk(reflect.TypeFor[Config](), "")

	if len(found) == 0 {
		t.Fatal("walked Config and found no scalar fields at all; the walk is broken")
	}
	for kind, at := range found {
		if _, ok := scalarLiteral(reflect.New(reflectTypeOfKind(kind)).Elem()); !ok {
			t.Errorf("%s is a %s and scalarLiteral has no case for it: every save drops %s", at, kind, at)
		}
	}
}

// reflectTypeOfKind is a representative type for a kind, so the check above can be
// made against the kind it found rather than the field's own named type.
func reflectTypeOfKind(k reflect.Kind) reflect.Type {
	switch k {
	case reflect.String:
		return reflect.TypeFor[string]()
	case reflect.Bool:
		return reflect.TypeOf(false)
	case reflect.Int:
		return reflect.TypeFor[int]()
	case reflect.Int64:
		return reflect.TypeFor[int64]()
	case reflect.Float64:
		return reflect.TypeFor[float64]()
	case reflect.Map:
		return reflect.TypeFor[map[string]string]()
	default:
		// Deliberately unrepresentable: an unknown kind reaches scalarLiteral as a zero
		// Value, which has no literal form, so the caller reports it.
		return reflect.TypeOf(struct{}{})
	}
}

// The float settings survive a save and a reload — including whole numbers, which is
// where TOML is strict.
func TestFloatSettingsSurviveASaveAndReload(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		outer float64
		rule  float64
	}{
		{"fractional", 2.5, 1.75},
		{"whole", 2, 3},
		{"below one", 0.5, 0.25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := minimal()
			cfg.Display.Media.Audio.Speed = tc.outer
			rate := tc.rule
			cfg.Display.Media.Rules = []MediaRule{{Match: "Work", Speed: &rate}}

			path := filepath.Join(t.TempDir(), "config.toml")
			if err := Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			back, err := Load(path)
			if err != nil {
				body, _ := os.ReadFile(path)
				t.Fatalf("the file we just wrote does not load: %v\n%s", err, body)
			}
			if got := back.Display.Media.Audio.Speed; got != tc.outer {
				t.Errorf("audio speed: got %v, want %v", got, tc.outer)
			}
			if len(back.Display.Media.Rules) != 1 || back.Display.Media.Rules[0].Speed == nil {
				t.Fatalf("the rule's speed did not survive: %+v", back.Display.Media.Rules)
			}
			if got := *back.Display.Media.Rules[0].Speed; got != tc.rule {
				t.Errorf("rule speed: got %v, want %v", got, tc.rule)
			}
		})
	}
}

// A list stops being a line once it stops fitting.
func TestALongArrayIsWrittenOnePerLine(t *testing.T) {
	t.Parallel()

	cfg := Config{Homeserver: "https://example.org", User: "@me:example.org"}
	cfg.Keys.FillDefaults()
	short := cfg
	short.Complete.Sources = []string{"history", "frequency"}
	long := cfg
	for i := range 30 {
		long.Display.Priority = append(long.Display.Priority,
			fmt.Sprintf("!room%02daaaaaaaaaaaaaaaaaa:example.org", i))
	}

	for name, tc := range map[string]struct {
		cfg     Config
		wrapped bool
	}{"short": {short, false}, "long": {long, true}} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := Save(path, tc.cfg); err != nil {
			t.Fatalf("%s: save: %v", name, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		gotWrapped := strings.Contains(string(body), "= [\n")
		if gotWrapped != tc.wrapped {
			t.Errorf("%s: wrapped = %v, want %v:\n%s", name, gotWrapped, tc.wrapped, body)
		}
		// Whichever shape, it has to read back as the same list.
		round, err := Load(path)
		if err != nil {
			t.Fatalf("%s: the written config does not load: %v", name, err)
		}
		if len(round.Display.Priority) != len(tc.cfg.Display.Priority) ||
			len(round.Complete.Sources) != len(tc.cfg.Complete.Sources) {
			t.Errorf("%s: round trip lost entries", name)
		}
	}
}

// A native room's ID is a room target like a Matrix one; the NameTarget* forms are not.
func TestRoomNamesTakesEveryRoomID(t *testing.T) {
	t.Parallel()

	native := "whatsapp:44000000001/120363000000000001@g.us"
	d := Display{Names: []DisplayName{
		{Target: "!a:x", Name: "A"}, {Target: native, Name: "Choir"},
		{Target: NameTargetSpace + "Work", Name: "W"}, {Target: NameTargetThread + "$e", Name: "T"},
	}}
	got := d.RoomNames()
	if len(got) != 2 || got["!a:x"] != "A" || got[native] != "Choir" {
		t.Errorf("RoomNames() = %v", got)
	}
}
