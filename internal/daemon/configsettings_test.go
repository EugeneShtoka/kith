package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// settingsWorld is kithd over a config file holding the home and work profiles, checked
// before a write by check (setup.Validate unless a test says otherwise).
type settingsWorld struct {
	t    *testing.T
	c    backendv1connect.BackendServiceClient
	h    *harness
	path string

	mu    sync.Mutex
	check func(config.Config) error
}

var settingsProfiles = []config.Profile{{Name: "home", Include: []string{"*"}}, {Name: "work", Include: []string{"group"}}}

func settingsServed(t *testing.T, cfg config.Config) *settingsWorld {
	t.Helper()
	cfg.Profiles = settingsProfiles
	w := &settingsWorld{t: t, path: filepath.Join(t.TempDir(), "config.toml"), check: setup.Validate}
	if err := config.Save(w.path, cfg); err != nil {
		t.Fatal(err)
	}
	n, err := daemon.NewNotifications(cfg, scopeOnly{&fakeBackend{Nop: nopWithChannels()}})
	if err != nil {
		t.Fatal(err)
	}
	w.h = serve(t, &fakeBackend{Nop: nopWithChannels()}, func(d *daemon.Daemon) {
		d.Notifications = n
		d.Config = daemon.NewConfigFile(w.path)
		d.CheckConfig = func(_ context.Context, cfg config.Config) error {
			w.mu.Lock()
			check := w.check
			w.mu.Unlock()
			return check(cfg)
		}
	})
	w.c = daemon.Client(w.h.path)
	return w
}

func (w *settingsWorld) refuseWith(check func(config.Config) error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.check = check
}

func (w *settingsWorld) settings(profile string) (*v1.ConfigSettingsResponse, error) {
	r, err := w.c.ConfigSettings(w.t.Context(), inProfile(profile, &v1.ConfigSettingsRequest{}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (w *settingsWorld) read() *v1.ConfigSettingsResponse {
	w.t.Helper()
	r, err := w.settings("home")
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *settingsWorld) set(profile, base, path string, value *v1.ConfigValue) (*v1.SetConfigSettingResponse, error) {
	r, err := w.c.SetConfigSetting(w.t.Context(), inProfile(profile, &v1.SetConfigSettingRequest{
		BaseRevision: base, Path: path, Value: value,
	}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (w *settingsWorld) bytes() []byte {
	w.t.Helper()
	data, err := os.ReadFile(w.path)
	if err != nil {
		w.t.Fatal(err)
	}
	return data
}

// file is the config the file holds, as config.Encode writes it.
func (w *settingsWorld) file() string {
	w.t.Helper()
	cfg, err := config.Decode(string(w.bytes()))
	if err != nil {
		w.t.Fatal(err)
	}
	return config.Encode(cfg)
}

// handEdit writes cfg into the file as an editor would, a comment making it a new
// revision even where it changes no key.
func (w *settingsWorld) handEdit(cfg config.Config, step int) {
	w.t.Helper()
	if err := os.WriteFile(w.path, fmt.Appendf([]byte(config.Encode(cfg)), "# by hand, %d\n", step), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func flag(b bool) *v1.ConfigValue { return &v1.ConfigValue{Value: &v1.ConfigValue_Flag{Flag: b}} }
func number(n int64) *v1.ConfigValue {
	return &v1.ConfigValue{Value: &v1.ConfigValue_Number{Number: n}}
}
func text(s string) *v1.ConfigValue { return &v1.ConfigValue{Value: &v1.ConfigValue_Text{Text: s}} }
func list(entries ...string) *v1.ConfigValue {
	return &v1.ConfigValue{Value: &v1.ConfigValue_List{List: &v1.ConfigList{Entries: entries}}}
}

// typedOf is a value as config.SetProperty takes it.
func typedOf(v *v1.ConfigValue) any {
	switch x := v.GetValue().(type) {
	case *v1.ConfigValue_Flag:
		return x.Flag
	case *v1.ConfigValue_Number:
		return int(x.Number)
	case *v1.ConfigValue_Text:
		return x.Text
	case *v1.ConfigValue_List:
		return slices.Clone(x.List.GetEntries())
	}
	return nil
}

// valueFor is a value the setting takes, drawn by rng: sometimes none (cleared).
func valueFor(rng *rand.Rand, s *v1.ConfigSetting) *v1.ConfigValue {
	if rng.IntN(5) == 0 {
		return nil
	}
	switch s.GetKind() {
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_TOGGLE:
		return flag(rng.IntN(2) == 0)
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_NUMBER:
		n := s.GetLeast() + rng.Int64N(8)
		if s.Most != nil {
			n = min(n, s.GetMost())
		}
		return number(n)
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_CHOICE:
		return text(s.GetChoices()[rng.IntN(len(s.GetChoices()))].GetValue())
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_DURATION:
		return text([]string{"30s", "2m", "1h30m"}[rng.IntN(3)])
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_TEXT:
		return text([]string{"", "-_", "DD.MM.YYYY", "YYYY年M月D日"}[rng.IntN(4)])
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_LIST:
		return [](*v1.ConfigValue){list("a"), list("a", "b"), list("b")}[rng.IntN(3)]
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_UNSPECIFIED:
	}
	return nil
}

// writeOf is cfg with path set to v; whether that changes it.
func writeOf(t *testing.T, cfg config.Config, path string, v any) (config.Config, bool) {
	t.Helper()
	next := cfg.Clone()
	if err := next.SetProperty(path, v); err != nil {
		t.Fatalf("SetProperty(%s, %#v) = %v", path, v, err)
	}
	was, wasSet, _ := cfg.PropertyValue(path)
	now, nowSet, _ := next.PropertyValue(path)
	return next, wasSet != nowSet || !reflect.DeepEqual(was, now)
}

// configChanges is every ConfigChanged frame's revision, in order.
func configChanges(t *testing.T, w *settingsWorld) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := w.c.ConfigChanged(ctx, connect.NewRequest(&v1.ConfigChangedRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for stream.Receive() {
			out <- stream.Msg().GetRevision()
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline := time.Now().Add(5 * time.Second)
	for w.h.streams.ConfigSubscribers() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("ConfigChanged never subscribed")
		}
		time.Sleep(time.Millisecond)
	}
	return out
}

func nextChange(t *testing.T, changes <-chan string) string {
	t.Helper()
	select {
	case rev := <-changes:
		return rev
	case <-time.After(5 * time.Second):
		t.Fatal("no ConfigChanged frame")
	}
	return ""
}

// Writers of the config in any order: two windows, each setting one key over the
// settings as it read them; the TUI's settings screen writing its whole config over the
// file as it read it (UpdateConfig); and hand edits, valid or not, between any two
// steps. A window's write lands only on the file it read, then changes its key alone,
// and only to a config kithd would run with; a stale one is ABORTED and anything
// refused writes nothing. Every write that lands, and only those, is told on
// ConfigChanged, in order; one changing nothing writes nothing.
func TestTheConfigIsSetOneKeyAtATimeOverWhatWasRead(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	seen := map[string]int{}
	note := func(outcome string) {
		mu.Lock()
		defer mu.Unlock()
		seen[outcome]++
	}
	t.Cleanup(func() {
		for _, outcome := range []string{"stale", "as it is", "does not check", "landed", "tui stale", "tui does not check", "tui landed", "hand edit"} {
			if seen[outcome] == 0 {
				t.Errorf("no step was %q: the generator lost a case", outcome)
			}
		}
	})
	for seed := range 40 {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(uint64(seed), 11))
			w := settingsServed(t, config.Config{})
			changes := configChanges(t, w)
			want, err := config.Decode(string(w.bytes()))
			if err != nil {
				t.Fatal(err)
			}
			var windows [2]*v1.ConfigSettingsResponse
			for i := range windows {
				windows[i] = w.read()
			}
			got, err := w.c.GetConfig(t.Context(), connect.NewRequest(&v1.GetConfigRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			tui := got.Msg
			for step := range 40 {
				before, revision := w.bytes(), w.read().GetRevision()
				switch rng.IntN(7) {
				case 0, 1:
					windows[rng.IntN(2)] = w.read()
				case 2, 3:
					shown := windows[rng.IntN(2)]
					s := shown.GetSettings()[rng.IntN(len(shown.GetSettings()))]
					v := valueFor(rng, s)
					next, changed := writeOf(t, want, s.GetPath(), typedOf(v))
					answer, err := w.set([]string{"home", "work", ""}[rng.IntN(3)], shown.GetRevision(), s.GetPath(), v)
					switch {
					case shown.GetRevision() != revision:
						note("stale")
						if connect.CodeOf(err) != connect.CodeAborted {
							t.Fatalf("step %d: %s set over a file changed since it was read = %v, want ABORTED", step, s.GetPath(), err)
						}
					case !changed:
						note("as it is")
						if err != nil || answer.GetRevision() != revision {
							t.Fatalf("step %d: %s set as it is = (%v, %v), want nothing written", step, s.GetPath(), answer, err)
						}
					case setup.Validate(next) != nil:
						note("does not check")
						if connect.CodeOf(err) != connect.CodeInvalidArgument {
							t.Fatalf("step %d: %s = %v makes a config that does not check (%v), but = %v", step, s.GetPath(), v, setup.Validate(next), err)
						}
					default:
						note("landed")
						if err != nil {
							t.Fatalf("step %d: %s = %v over the file as read = %v", step, s.GetPath(), v, err)
						}
						want = next
						if !reflect.DeepEqual(typedOf(answer.GetSetting().GetValue()), typedOf(v)) && v != nil && typedOf(v) != "" {
							t.Errorf("step %d: %s answered %v, want %v", step, s.GetPath(), answer.GetSetting().GetValue(), v)
						}
						if rev := nextChange(t, changes); rev != answer.GetRevision() || rev == revision {
							t.Fatalf("step %d: ConfigChanged told %s, the write answered %s (was %s)", step, rev, answer.GetRevision(), revision)
						}
					}
				case 4:
					got, err := w.c.GetConfig(t.Context(), connect.NewRequest(&v1.GetConfigRequest{}))
					if err != nil {
						t.Fatal(err)
					}
					tui = got.Msg
				case 5:
					cfg, err := config.Decode(tui.GetConfig())
					if err != nil {
						t.Fatal(err)
					}
					settings := w.read().GetSettings()
					s := settings[rng.IntN(len(settings))]
					next, _ := writeOf(t, cfg, s.GetPath(), typedOf(valueFor(rng, s)))
					if rng.IntN(3) == 0 {
						next, _ = writeOf(t, next, "log.level", []string{"debug", "info", "loud"}[rng.IntN(3)])
					}
					answer, err := w.c.UpdateConfig(t.Context(), connect.NewRequest(&v1.UpdateConfigRequest{
						BaseRevision: tui.GetRevision(), Config: config.Encode(next),
					}))
					switch {
					case tui.GetRevision() != revision:
						note("tui stale")
						if connect.CodeOf(err) != connect.CodeAborted {
							t.Fatalf("step %d: the TUI's stale write = %v, want ABORTED", step, err)
						}
					case setup.Validate(next) != nil:
						note("tui does not check")
						if connect.CodeOf(err) != connect.CodeInvalidArgument {
							t.Fatalf("step %d: the TUI's write that does not check = %v", step, err)
						}
					default:
						note("tui landed")
						if err != nil {
							t.Fatalf("step %d: the TUI's write = %v", step, err)
						}
						want = next
						if rev := nextChange(t, changes); rev != answer.Msg.GetRevision() {
							t.Fatalf("step %d: ConfigChanged told %s, the TUI's write answered %s", step, rev, answer.Msg.GetRevision())
						}
					}
				case 6:
					note("hand edit")
					settings := w.read().GetSettings()
					s := settings[rng.IntN(len(settings))]
					v := typedOf(valueFor(rng, s))
					if s.GetPath() == "codes.max_length" && rng.IntN(2) == 0 {
						v = 2 // below min_length's default: a hand edit that does not check
					}
					want, _ = writeOf(t, want, s.GetPath(), v)
					w.handEdit(want, step)
				}
				if file := w.file(); file != config.Encode(want) {
					t.Fatalf("step %d: the file holds\n%s\nwant\n%s", step, file, config.Encode(want))
				}
				if after := w.bytes(); !slices.Equal(after, before) && w.read().GetRevision() == revision {
					t.Fatalf("step %d: the file changed but its revision did not", step)
				}
			}
			select {
			case rev := <-changes:
				t.Errorf("ConfigChanged told %s, which no write that landed made", rev)
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}

// Windows and the TUI writing at once over one revision: exactly one lands, whichever,
// and the file is that one's.
func TestWritersOverOneRevisionLandOnce(t *testing.T) {
	t.Parallel()
	for round := range 10 {
		w := settingsServed(t, config.Config{})
		base := w.read().GetRevision()
		var mu sync.Mutex
		var won []string
		var wg sync.WaitGroup
		for i := range 6 {
			wg.Go(func() {
				var err error
				name := fmt.Sprintf("window %d", i)
				switch i {
				case 0, 1, 2:
					_, err = w.set("home", base, "notifications.max_per_room", number(int64(10+i)))
				case 3:
					_, err = w.set("work", base, "display.deleted.keep", flag(true))
				default:
					name = fmt.Sprintf("tui %d", i)
					cfg := config.Config{Profiles: settingsProfiles}
					cfg.Notifications.MaxPerRoom = 10 + i
					_, err = w.c.UpdateConfig(t.Context(), connect.NewRequest(&v1.UpdateConfigRequest{BaseRevision: base, Config: config.Encode(cfg)}))
				}
				switch {
				case err == nil:
					mu.Lock()
					won = append(won, name)
					mu.Unlock()
				case connect.CodeOf(err) != connect.CodeAborted:
					t.Error(err)
				}
			})
		}
		wg.Wait()
		cfg, err := config.Decode(string(w.bytes()))
		if err != nil || len(won) != 1 {
			t.Fatalf("round %d: %v landed (%v)", round, won, err)
		}
		n := cfg.Notifications.MaxPerRoom
		switch winner := won[0]; {
		case winner == "window 3":
			if !cfg.Display.Deleted.Keep() || n != 0 {
				t.Errorf("round %d: %s landed, the file has keep %v, max_per_room %d", round, winner, cfg.Display.Deleted.Keep(), n)
			}
		case fmt.Sprintf("window %d", n-10) != winner && fmt.Sprintf("tui %d", n-10) != winner:
			t.Errorf("round %d: %s landed, the file has max_per_room %d", round, winner, n)
		}
	}
}

// Every property of the bridge's file: one ConfigSettings lists is set by a value of its
// kind, and nothing else is, whatever the value; nor is a table, a list's element, a
// record table's field, a device setting, or a path spelled otherwise. The groups the
// owner kept for the TUI and the file are refused by name, so a change to the list that
// lets one through fails here too.
func TestOnlyTheListedSettingsAreSet(t *testing.T) {
	t.Parallel()
	w := settingsServed(t, config.Config{})
	w.refuseWith(func(config.Config) error { return nil }) // the check is not what this asks
	listed := map[string]*v1.ConfigSetting{}
	for _, s := range w.read().GetSettings() {
		listed[s.GetPath()] = s
	}
	sample := map[config.PropertyKind][]*v1.ConfigValue{
		config.PropertyBool: {flag(true)}, config.PropertyOptionalBool: {flag(false)},
		config.PropertyInt: {number(5)}, config.PropertyOptionalInt: {number(5)},
		config.PropertyFloat: {flag(true), number(1), text("0.5")}, config.PropertyOptionalFloat: {number(1)},
		config.PropertyText: {text("1m")}, config.PropertyList: {list("a")},
	}
	for _, p := range config.Properties() {
		if p.File != config.BridgeFile {
			continue
		}
		s, ok := listed[p.Path]
		if ok {
			var v *v1.ConfigValue
			if s.GetKind() == v1.ConfigSettingKind_CONFIG_SETTING_KIND_CHOICE {
				v = text(s.GetChoices()[len(s.GetChoices())-1].GetValue())
			} else {
				v = sample[p.Kind][0]
			}
			got, err := w.set("home", w.read().GetRevision(), p.Path, v)
			if err != nil || !reflect.DeepEqual(typedOf(got.GetSetting().GetValue()), typedOf(v)) {
				t.Errorf("%s = %v, listed, answered (%v, %v)", p.Path, v, got.GetSetting().GetValue(), err)
			}
			continue
		}
		for _, v := range append(sample[p.Kind], nil) {
			before := w.bytes()
			if _, err := w.set("home", w.read().GetRevision(), p.Path, v); connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Errorf("%s = %v, not listed, answered %v, want PERMISSION_DENIED", p.Path, v, err)
			}
			if !slices.Equal(before, w.bytes()) {
				t.Errorf("%s, refused, changed the file", p.Path)
			}
		}
	}
	for _, path := range []string{
		// What the owner keeps for the TUI, the CLI and the file.
		"spell.command", "complete.model.command", "complete.model.model", "complete.model.threads",
		"complete.model.port", "commands.dir", "phonebook.dir", "backup.dir", "backup.key", "backup.enabled",
		"remote.listen", "assist.endpoint", "assist.key_ref", "assist.rooms", "assist.except", "assist.encrypted",
		"assist.budget", "assist.summary.prompt", "agent.read.rooms", "agent.read.encrypted", "agent.write.rooms",
		"agent.write.send", "agent.write.cooldown", "allow_token_file", "log.level", "log.target", "bridge_contacts",
		"whatsapp.default", "telegram.archive.tag", "clipboard.auto_copy", "codes.include", "display.rail.order",
		// No property at all: tables, elements, record tables and their fields, other files' keys.
		"", "agent", "agent.read", "agent.read.rooms.0", "agent.read.rooms[0]", "assist.rooms[0]",
		"notifications.rule", "notifications.rule.0.show", "profile", "profile.0.include", "tag", "filter.0.tag",
		"whatsapp.account", "whatsapp.account.0.phone", "storage", "storage.data_dir", "gui.theme.mode",
		"gui.theme.light.accent", "keys.quit", "display.skin_tone", "notifications.enabled", "notifications.sound",
		"display", "display.unread.x", "Display.Unread", " display.unread", "display.unread ", "display..unread",
	} {
		if _, ok := listed[path]; ok {
			t.Errorf("%q is listed", path)
		}
		before := w.bytes()
		for _, v := range []*v1.ConfigValue{text("x"), list("x"), flag(true), number(1), nil} {
			if _, err := w.set("home", w.read().GetRevision(), path, v); connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Errorf("%q = %v answered %v, want PERMISSION_DENIED", path, v, err)
			}
		}
		if !slices.Equal(before, w.bytes()) {
			t.Errorf("%q, refused, changed the file", path)
		}
	}
}

// A value a setting does not take is refused and nothing is written: another kind, a
// number outside its bounds, a word not among its choices, a length of time that is
// not one or is negative, a list with an empty entry.
func TestAValueTheSettingDoesNotTakeIsRefused(t *testing.T) {
	t.Parallel()
	w := settingsServed(t, config.Config{})
	w.refuseWith(func(config.Config) error { return nil })
	kinds := []*v1.ConfigValue{flag(true), number(1), text("2m"), list("a")}
	for _, s := range w.read().GetSettings() {
		var bad []*v1.ConfigValue
		for _, v := range kinds {
			if reflect.TypeOf(v.GetValue()) != reflect.TypeOf(right(s).GetValue()) {
				bad = append(bad, v)
			}
		}
		switch s.GetKind() {
		case v1.ConfigSettingKind_CONFIG_SETTING_KIND_NUMBER:
			if s.Least != nil {
				bad = append(bad, number(s.GetLeast()-1))
			}
			if s.Most != nil {
				bad = append(bad, number(s.GetMost()+1))
			}
		case v1.ConfigSettingKind_CONFIG_SETTING_KIND_CHOICE:
			bad = append(bad, text("nonesuch"), text(""), text(strings.ToUpper(s.GetChoices()[0].GetValue())))
		case v1.ConfigSettingKind_CONFIG_SETTING_KIND_DURATION:
			bad = append(bad, text("soon"), text("-1m"), text("10"), text(""))
		case v1.ConfigSettingKind_CONFIG_SETTING_KIND_LIST:
			bad = append(bad, list("a", ""), list(" "))
		case v1.ConfigSettingKind_CONFIG_SETTING_KIND_TOGGLE, v1.ConfigSettingKind_CONFIG_SETTING_KIND_TEXT,
			v1.ConfigSettingKind_CONFIG_SETTING_KIND_UNSPECIFIED:
		}
		for _, v := range bad {
			before := w.bytes()
			if _, err := w.set("home", w.read().GetRevision(), s.GetPath(), v); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("%s = %v answered %v, want INVALID_ARGUMENT", s.GetPath(), v, err)
			}
			if !slices.Equal(before, w.bytes()) {
				t.Errorf("%s = %v, refused, changed the file", s.GetPath(), v)
			}
		}
		if _, err := w.set("home", w.read().GetRevision(), s.GetPath(), right(s)); err != nil {
			t.Errorf("%s = %v answered %v", s.GetPath(), right(s), err)
		}
	}
}

// right is a value s takes.
func right(s *v1.ConfigSetting) *v1.ConfigValue {
	switch s.GetKind() {
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_TOGGLE:
		return flag(true)
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_NUMBER:
		return number(max(s.GetLeast(), 1))
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_CHOICE:
		return text(s.GetChoices()[0].GetValue())
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_DURATION:
		return text("90s")
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_LIST:
		return list("a")
	case v1.ConfigSettingKind_CONFIG_SETTING_KIND_TEXT, v1.ConfigSettingKind_CONFIG_SETTING_KIND_UNSPECIFIED:
	}
	return text("x")
}

// A setting that leaves a config kithd would not run with (its own rules, or a
// network's judging its section) is refused in the check's words, and nothing is
// written or told.
func TestASettingKithdWouldNotRunWithIsNotWritten(t *testing.T) {
	t.Parallel()
	w := settingsServed(t, config.Config{})
	changes := configChanges(t, w)
	before := w.bytes()
	_, err := w.set("home", w.read().GetRevision(), "codes.digits", flag(false)) // require_digit stays on
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "require_digit") {
		t.Errorf("digits off while a digit is required = %v", err)
	}
	refusal := errors.New(`whatsapp.account[0] (home): the archive tag "Old" is no tag`)
	w.refuseWith(func(cfg config.Config) error {
		if cfg.Display.Deleted.Keep() {
			return refusal
		}
		return setup.Validate(cfg)
	})
	if _, err = w.set("work", w.read().GetRevision(), "display.deleted.keep", flag(true)); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), refusal.Error()) {
		t.Errorf("a network's refusal = %v", err)
	}
	if !slices.Equal(before, w.bytes()) {
		t.Error("a refused setting changed the file")
	}
	landed, err := w.set("home", w.read().GetRevision(), "display.deleted.mine", text("hide"))
	if err != nil {
		t.Fatal(err)
	}
	if rev := nextChange(t, changes); rev != landed.GetRevision() {
		t.Errorf("ConfigChanged told %s first, the write that landed answered %s", rev, landed.GetRevision())
	}
}

// The config is the bridge's: every profile reads the same settings and sets them for
// every other; a profile the config does not have is refused before anything is read.
func TestEveryProfileSetsTheBridgesSettings(t *testing.T) {
	t.Parallel()
	w := settingsServed(t, config.Config{})
	home := w.read()
	for _, profile := range []string{"work", ""} {
		got, err := w.settings(profile)
		if err != nil || !reflect.DeepEqual(got.GetSettings(), home.GetSettings()) || got.GetRevision() != home.GetRevision() {
			t.Errorf("profile %q reads other settings than home's (%v)", profile, err)
		}
	}
	set, err := w.set("work", home.GetRevision(), "display.time_format", text("12h"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range w.read().GetSettings() {
		if s.GetPath() == "display.time_format" && s.GetValue().GetText() != "12h" {
			t.Errorf("home reads %v after work set 12h", s.GetValue())
		}
	}
	before := w.bytes()
	if _, err := w.settings("gone"); err == nil {
		t.Error("a profile the config does not have read the settings")
	}
	if _, err := w.set("gone", set.GetRevision(), "display.time_format", text("24h")); err == nil || !slices.Equal(before, w.bytes()) {
		t.Errorf("a profile the config does not have set a setting (%v)", err)
	}
}

// What ConfigSettings serves: each setting's default typed by its kind, its value only
// where the file sets one, and default.toml's text.
func TestTheSettingsAreServedTyped(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	cfg.Display.TimeFormat = "12h"
	cfg.Spell.Dictionaries = []string{"en_US", "he_IL"}
	off := false
	cfg.Codes.Letters = &off
	w := settingsServed(t, cfg)
	by := map[string]*v1.ConfigSetting{}
	for _, s := range w.read().GetSettings() {
		by[s.GetPath()] = s
		if s.GetDoc() == "" || s.GetLabel() == "" || s.GetGroup() == "" {
			t.Errorf("%s is served without its doc, label or group", s.GetPath())
		}
	}
	for path, want := range map[string][2]any{
		"display.time_format":        {"24h", "12h"},
		"spell.dictionaries":         {[]string(nil), []string{"en_US", "he_IL"}},
		"codes.letters":              {true, false},
		"display.room_name_rules":    {true, nil}, // default.toml writes it commented out
		"codes.min_length":           {4, nil},
		"notifications.rate_window":  {"2m", nil},
		"notifications.max_per_room": {0, nil},
	} {
		s := by[path]
		if got := typedOf(s.GetFallback()); !reflect.DeepEqual(got, want[0]) {
			t.Errorf("%s's fallback = %#v, want %#v", path, got, want[0])
		}
		if got := typedOf(s.GetValue()); !reflect.DeepEqual(got, want[1]) {
			t.Errorf("%s = %#v, want %#v", path, got, want[1])
		}
	}
	if k := by["display.time_format"].GetKind(); k != v1.ConfigSettingKind_CONFIG_SETTING_KIND_CHOICE {
		t.Errorf("time_format is a %v", k)
	}
	if k := by["complete.model.idle"].GetKind(); k != v1.ConfigSettingKind_CONFIG_SETTING_KIND_DURATION {
		t.Errorf("complete.model.idle is a %v", k)
	}
}
