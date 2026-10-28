package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	cfg := Config{Matrix: Matrix{Accounts: []MatrixAccount{{Name: "me", Homeserver: "https://example.org", User: "@me:example.org"}}}}
	cfg.Keys.FillDefaults()
	return cfg
}

// The baseline is what a missing key produces, not the shipped default.toml.
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

// Settings at their default are omitted, so later default changes flow through.
func TestSaveOmitsDefaults(t *testing.T) {
	t.Parallel()

	body, _ := saved(t, minimal())
	for _, absent := range []string{"[keys]", "[keys.nav]", "quit =", "[display]", "[notifications]"} {
		if strings.Contains(body, absent) {
			t.Errorf("%q should be omitted — it is at its default:\n%s", absent, body)
		}
	}
	// The required fields are always written.
	for _, present := range []string{"[[matrix.account]]", "homeserver =", "user ="} {
		if !strings.Contains(body, present) {
			t.Errorf("%q must be written", present)
		}
	}
}

// Only changed bindings are written.
func TestSaveWritesOnlyChangedBindings(t *testing.T) {
	t.Parallel()

	cfg := minimal()
	cfg.Keys.Bindings["timeline.react"] = "R"
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
	if reloaded.Keys.Bindings["timeline.react"] != "R" {
		t.Errorf("React = %q, want the saved value", reloaded.Keys.Bindings["timeline.react"])
	}
	if reloaded.Keys.Bindings["timeline.insert"] != DefaultKeys().Bindings["timeline.insert"] {
		t.Errorf("Insert = %q, want the default filled back in", reloaded.Keys.Bindings["timeline.insert"])
	}
}

// Identities round-trip through the writer.
func TestSaveIdentities(t *testing.T) {
	t.Parallel()

	cfg := minimal()
	cfg.Display.Identities = []Identity{
		{Alias: "Dana", Color: "#8ff586", IDs: []string{"matrix:@dana:x", "matrix:@whatsapp_dana:x"}},
		{Alias: "יבגני", IDs: []string{"matrix:@rtl:x"}},
	}
	body, path := saved(t, cfg)

	if strings.Count(body, "[[display.identity]]") != 2 {
		t.Errorf("want two identity tables:\n%s", body)
	}
	// Inside a table, an array needs a bare key; a dotted path would land elsewhere.
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

// Every config shape survives a save and reload.
func TestSaveRoundTripsEveryShape(t *testing.T) {
	t.Parallel()

	yes, no := true, false
	cfg := minimal()
	cfg.AllowTokenFile = true                                                                             // bool
	cfg.Display.MaxNameLength = 20                                                                        // int
	cfg.Display.ColorMessages = true                                                                      // bool in a table
	cfg.Display.OpenInInsert = &no                                                                        // *bool, set false
	cfg.Display.RoomNameRules = &yes                                                                      // *bool, set true
	cfg.Display.Rail.Order = []string{"tag:Unread", "-"}                                                  // []string in a table
	cfg.Display.Rail.Hidden = []string{"tag:All"}                                                         //
	cfg.Display.Names = []DisplayName{{Target: NameTargetSpace + "Work", Name: "Job"}}                    // one list
	cfg.Display.Media.Mode = "inline"                                                                     // string in a nested table
	cfg.Display.Media.MaxHeight = 12                                                                      //
	cfg.Display.Reactions.Static = []string{"👍", "🎉"}                                                     // non-ASCII array
	cfg.Display.Names = append(cfg.Display.Names, DisplayName{Target: mx + "!a:x", Name: `He said "hi"`}) // escaping
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

// Saving twice produces identical bytes.
func TestSaveIsIdempotent(t *testing.T) {
	t.Parallel()

	cfg := minimal()
	cfg.Display.Names = []DisplayName{{Target: mx + "!z:x", Name: "Z"}, {Target: mx + "!a:x", Name: "A"}, {Target: mx + "!m:x", Name: "M"}}
	cfg.Display.Identities = []Identity{{Alias: "A", IDs: []string{"matrix:@a:x"}}}

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

// The first save backs up the hand-written file; later saves never overwrite the backup.
func TestSaveBacksUpOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "# my careful comments\n[[matrix.account]]\nname = \"me\"\nhomeserver = \"https://example.org\"\nuser = \"@me:example.org\"\n"
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

// The config is written 0600.
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

// An emptied list is written as [] so the default does not return on load.
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
	cfg.Keys.Bindings["nav.up"] = "" // unbound, which differs from the filled default
	body2, path := saved(t, cfg)
	if !strings.Contains(body2, `up = ""`) {
		t.Errorf("an unbound key differs from its default and must be written:\n%s", body2)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// FillDefaults treats "" as unset, so "-" is the unbind sentinel.
	if reloaded.Keys.Bindings["nav.up"] != DefaultKeys().Bindings["nav.up"] {
		t.Errorf("Nav.Up = %q; an empty binding reads as unset and refills", reloaded.Keys.Bindings["nav.up"])
	}
}

// Every scalar kind that reaches scalarLiteral must have a TOML literal form.
func TestEveryScalarKindInConfigHasALiteral(t *testing.T) {
	t.Parallel()

	// kind -> the first field path that reached it, for the failure message.
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

// reflectTypeOfKind is a representative type for a kind.
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
		// A zero Value has no literal form, so the caller reports the kind.
		return reflect.TypeOf(struct{}{})
	}
}

// Float settings survive a save and reload, including whole numbers.
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

// A list wraps to one entry per line once too long.
func TestALongArrayIsWrittenOnePerLine(t *testing.T) {
	t.Parallel()

	cfg := Config{Matrix: Matrix{Accounts: []MatrixAccount{{Name: "me", Homeserver: "https://example.org", User: "@me:example.org"}}}}
	cfg.Keys.FillDefaults()
	short := cfg
	short.Complete.Sources = []string{"history", "frequency"}
	long := cfg
	for i := range 30 {
		long.Display.Priority = append(long.Display.Priority,
			fmt.Sprintf(mx+"!room%02daaaaaaaaaaaaaaaaaa:example.org", i))
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
		// Either shape reads back as the same list.
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
		{Target: mx + "!a:x", Name: "A"}, {Target: native, Name: "Choir"},
		{Target: NameTargetSpace + "Work", Name: "W"}, {Target: NameTargetThread + mx + "$e", Name: "T"},
	}}
	got := d.RoomNames()
	if len(got) != 2 || got[mx+"!a:x"] != "A" || got[native] != "Choir" {
		t.Errorf("RoomNames() = %v", got)
	}
}

// Absent tags and rail order take the starter's and are omitted on save; deleting all
// tags writes `tag = []`, which stays empty.
func TestAConfigWithoutTagsHasTheStartersAndDeletingThemSticks(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("allow_token_file = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tag := range cfg.Tags {
		names = append(names, tag.Name)
	}
	if !slices.Contains(names, "Unread") || !slices.Contains(names, "DMs") || !slices.Contains(names, "All") || len(cfg.Display.Rail.Order) == 0 {
		t.Fatalf("tags %v, rail order %v: want the starter's", names, cfg.Display.Rail.Order)
	}
	if serr := Save(path, cfg); serr != nil {
		t.Fatal(serr)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "[[tag]]") || strings.Contains(string(body), "order") {
		t.Errorf("the starter's tags were written out:\n%s", body)
	}
	cfg.Tags = nil
	if serr := Save(path, cfg); serr != nil {
		t.Fatal(serr)
	}
	body, _ = os.ReadFile(path)
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "tag = []") || len(again.Tags) != 0 {
		t.Errorf("after deleting every tag: %d tags back\n%s", len(again.Tags), body)
	}
}
