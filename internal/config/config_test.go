package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestRequireAccount(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		cfg     Config
		wantErr bool
	}{
		"complete":              {Config{Homeserver: "https://x", User: "@a:x"}, false},
		"missing homeserver":    {Config{User: "@a:x"}, true},
		"missing user":          {Config{Homeserver: "https://x"}, true},
		"empty":                 {Config{}, true},
		"profile":               {Config{Profiles: []Profile{{Name: "work", Homeserver: "https://x", User: "@me:x"}}}, false},
		"profile no name":       {Config{Profiles: []Profile{{Homeserver: "https://x", User: "@me:x"}}}, true},
		"profile no user":       {Config{Profiles: []Profile{{Name: "work", Homeserver: "https://x"}}}, true},
		"profile no homeserver": {Config{Profiles: []Profile{{Name: "work", User: "@me:x"}}}, true},
		"profile bad name":      {Config{Profiles: []Profile{{Name: "a/b", Homeserver: "https://x", User: "@me:x"}}}, true},
	} {
		if err := tc.cfg.RequireAccount(); (err != nil) != tc.wantErr {
			t.Errorf("%s: RequireAccount() = %v, wantErr %v", name, err, tc.wantErr)
		}
	}
	if err := (Config{}).RequireAccount(); !errors.Is(err, ErrIncomplete) {
		t.Errorf("empty config = %v, want ErrIncomplete", err)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	cfg, err := loadText(t, `allow_token_file = true

[display]
max_name_length = 12
room_name_rules = false

[[display.space_rule]]
space = "Friends"
first_name_only = true

[[display.identity]]
alias = "Robin"
color = "#66ccff"
ids = ["@robin:x", "@robin_alt:x"]

[display.rail]
order  = ["dms", "home"]
hidden = ["Work"]

[notifications]
enabled = true
title = "{protocol} · {sender}"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := cfg.Display
	if cfg.Homeserver != "https://x" || !cfg.AllowTokenFile || d.MaxNameLength != 12 || d.ApplyRoomNameRules() {
		t.Errorf("top level / display = %+v", cfg)
	}
	if len(d.SpaceRules) != 1 || d.SpaceRules[0].Space != "Friends" || !d.SpaceRules[0].FirstNameOnly {
		t.Errorf("SpaceRules = %+v", d.SpaceRules)
	}
	if len(d.Identities) != 1 || d.Identities[0].Alias != "Robin" || len(d.Identities[0].IDs) != 2 {
		t.Errorf("Identities = %+v", d.Identities)
	}
	if len(d.Rail.Order) != 2 || d.Rail.Order[0] != "dms" || len(d.Rail.Hidden) != 1 {
		t.Errorf("Rail = %+v", d.Rail)
	}
	if !cfg.Notifications.Enabled || cfg.Notifications.TitleTemplate() != "{protocol} · {sender}" {
		t.Errorf("Notifications = %+v", cfg.Notifications)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("missing file loaded")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("homeserver = \"https://x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrIncomplete) {
		t.Errorf("incomplete config = %v, want ErrIncomplete", err)
	}
}

// A key nothing reads is refused rather than silently ignored: a misplaced or removed
// setting would otherwise look configured while doing nothing.
func TestLoadRefusesUnknownKeys(t *testing.T) {
	t.Parallel()

	_, err := loadText(t, "[notifications]\non = \"all\"\n\n[agent]\nsend = [\"!a:x\"]\n")
	if err == nil {
		t.Fatal("unknown keys were accepted")
	}
	for _, key := range []string{"notifications.on", "agent.send"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name %s", err, key)
		}
	}
}

func TestWriteDefaultIfMissing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "config.toml")

	created, err := WriteDefaultIfMissing(path)
	if err != nil || !created {
		t.Fatalf("first write: created=%v err=%v", created, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != Annotated() {
		t.Error("written file is not the annotated default")
	}
	if created2, err := WriteDefaultIfMissing(path); err != nil || created2 {
		t.Fatalf("second write: created=%v err=%v, want false/nil", created2, err)
	}
}

func TestNotificationTemplates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		n           Notifications
		title, body string
	}{
		{Notifications{}, DefaultTitleTemplate, DefaultBodyTemplate},
		{Notifications{Title: "[{protocol}] {sender}", Body: "{time} {body}"}, "[{protocol}] {sender}", "{time} {body}"},
		{Notifications{Title: "-", Body: "-"}, "", ""}, // "-" is an explicit empty
	} {
		if got := tc.n.TitleTemplate(); got != tc.title {
			t.Errorf("TitleTemplate(%q) = %q, want %q", tc.n.Title, got, tc.title)
		}
		if got := tc.n.BodyTemplate(); got != tc.body {
			t.Errorf("BodyTemplate(%q) = %q, want %q", tc.n.Body, got, tc.body)
		}
	}
}

func TestProfileResolution(t *testing.T) {
	t.Parallel()

	withProfiles := Config{Profiles: []Profile{
		{Name: "personal", Homeserver: "https://home.example", User: "@me:home.example"},
		{Name: "work", Homeserver: "https://work.example", User: "@me:work.example"},
	}}

	if got, err := withProfiles.Profile(""); err != nil || got.User != "@me:home.example" {
		t.Errorf("default = %q, %v; want the first profile", got.User, err)
	}
	if got, err := withProfiles.Profile("WORK"); err != nil || got.Homeserver != "https://work.example" {
		t.Errorf("work = %q, %v", got.Homeserver, err)
	}
	_, err := withProfiles.Profile("office")
	if err == nil || !strings.Contains(err.Error(), "personal") || !strings.Contains(err.Error(), "work") {
		t.Errorf("unknown profile: %v, want a refusal naming the profiles", err)
	}

	// --profile against a single-account config must not hand over the only account.
	single := Config{Homeserver: "https://home.example", User: "@me:home.example"}
	if _, err := single.Profile("work"); err == nil {
		t.Error("--profile against a config with no profiles should be refused")
	}
	if got, err := single.Profile(""); err != nil || got.User != "@me:home.example" {
		t.Errorf("single-account config = %q, %v", got.User, err)
	}

	both := withProfiles
	both.User, both.Homeserver = "@me:home.example", "https://home.example"
	if _, err := both.Profile(""); err == nil {
		t.Error("an account set both at the top level and in profiles should be refused")
	}
}

// Popup timeout and audio close_after share the config-wide sign convention: negative
// is never, 0 is immediate (for the popup: the daemon decides), positive is seconds.
func TestSecondsSignConvention(t *testing.T) {
	t.Parallel()

	seconds := func(i int) *int { return &i }
	for _, tc := range []struct {
		set               *int
		popup, closeAfter time.Duration
	}{
		{nil, DefaultTimeoutSeconds * time.Second, DefaultCloseAfterSeconds * time.Second},
		{seconds(-1), -1, -1},
		{seconds(-30), -1, -1},
		{seconds(0), 0, 0},
		{seconds(30), 30 * time.Second, 30 * time.Second},
	} {
		if got := (Notifications{Timeout: tc.set}).PopupTimeout(); got != tc.popup {
			t.Errorf("PopupTimeout(%v) = %v, want %v", tc.set, got, tc.popup)
		}
		if got := (Audio{CloseAfter: tc.set}).CloseDelay(); got != tc.closeAfter {
			t.Errorf("CloseDelay(%v) = %v, want %v", tc.set, got, tc.closeAfter)
		}
	}
}

// An explicit "0s" idle means never stop the server, unlike unset.
func TestModelIdleZeroMeansNever(t *testing.T) {
	t.Parallel()

	for idle, want := range map[string]time.Duration{
		"": DefaultLocalIdle, "0s": -1, "90s": 90 * time.Second, "ten minutes": DefaultLocalIdle,
	} {
		if got := (CompleteModel{Idle: idle}).IdleOrDefault(); got != want {
			t.Errorf("idle %q = %v, want %v", idle, got, want)
		}
	}
}

func TestScriptBlocks(t *testing.T) {
	t.Parallel()

	var cfg Config
	if _, err := toml.Decode(`
[[commands.script]]
name   = "save"
needs  = ["url", "message"]
output = "none"
`, &cfg); err != nil {
		t.Fatal(err)
	}
	got, ok := cfg.Commands.ScriptFor("/SAVE")
	if !ok || got.Output != "none" || len(got.Needs) != 2 {
		t.Errorf("ScriptFor(\"/SAVE\") = %+v, %v", got, ok)
	}
	if _, ok := cfg.Commands.ScriptFor("nothing"); ok {
		t.Error("a command with no block was reported as having one")
	}
}
