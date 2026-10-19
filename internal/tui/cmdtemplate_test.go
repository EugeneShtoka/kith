package tui

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/cmdtemplates"
	"github.com/EugeneShtoka/kith/internal/config"
)

// fakeKeyring is a keyring that can be unreadable, or refuse to keep after a number of
// writes.
type fakeKeyring struct {
	mu         sync.Mutex
	kept       map[string]string
	unreadable bool
	keepsLeft  int // writes it takes before refusing; -1 never refuses
}

var errFakeKeyring = errors.New("the keyring is locked")

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{kept: map[string]string{}, keepsLeft: -1} }

func (k *fakeKeyring) Secret(ref string) (string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.unreadable {
		return "", false, errFakeKeyring
	}
	v, ok := k.kept[ref]
	return v, ok, nil
}

func (k *fakeKeyring) StoreSecret(ref, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keepsLeft == 0 {
		return errFakeKeyring
	}
	if k.keepsLeft > 0 {
		k.keepsLeft--
	}
	k.kept[ref] = value
	return nil
}

func (k *fakeKeyring) snapshot() map[string]string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]string, len(k.kept))
	maps.Copy(out, k.kept)
	return out
}

// secretModel is a model in Alpha with blocks configured, scripts written for each, and
// the keyring (nil for none); the composer is open.
func secretModel(t *testing.T, blocks []config.Command, scripts map[string]string, keyring Secrets) (Model, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range scripts {
		write(t, dir, name, body, 0o755)
	}
	m, _ := attaching(t)
	if keyring != nil {
		m = m.WithSecrets(keyring)
	}
	m = m.WithConfigFile(filepath.Join(home, "config.toml"),
		config.Config{Commands: config.Commands{Dir: dir, Timeout: 5}, UserCommands: blocks}).keptIn()
	return m, dir
}

// printsSecrets prints exactly the KITH_SECRET_ variables it was given.
const printsSecrets = "#!/bin/sh\nenv | grep '^KITH_SECRET_' | sort\necho done\n"

type keyringState int

const (
	keyringNone keyringState = iota
	keyringReadable
	keyringUnreadable
)

// A command runs only with every secret it declares, each under its own variable, and
// nothing else; otherwise it stops before running, naming the first missing one, and no
// value is ever shown. Generated: every declaration of up to two secrets, each kept or
// not, under each keyring state, with a decoy kept for an undeclared name and for another
// command.
func TestACommandRunsOnlyWithEverySecretItDeclares(t *testing.T) {
	t.Parallel()

	declarations := [][]string{nil, {"alpha"}, {"beta"}, {"alpha", "beta"}, {"beta", "alpha"}}
	for _, state := range []keyringState{keyringNone, keyringReadable, keyringUnreadable} {
		for d, declared := range declarations {
			for kept := range 1 << len(declared) {
				name := fmt.Sprintf("s%d%d", d, kept)
				keyring := newFakeKeyring()
				keyring.kept[config.SecretRef(name, "gamma")] = "decoy-undeclared"
				keyring.kept[config.SecretRef("other", "alpha")] = "decoy-other-command"
				want := map[string]string{}
				firstMissing := ""
				for i, secret := range declared {
					value := "v-" + name + "-" + secret
					if kept&(1<<i) != 0 {
						keyring.kept[config.SecretRef(name, secret)] = value
						want["KITH_SECRET_"+strings.ToUpper(secret)] = value
					} else if firstMissing == "" {
						firstMissing = secret
					}
				}
				var store Secrets
				switch state {
				case keyringReadable:
					store = keyring
				case keyringUnreadable:
					keyring.unreadable = true
					store = keyring
				case keyringNone:
				}
				runs := len(declared) == 0 || (state == keyringReadable && firstMissing == "")
				what := fmt.Sprintf("/%s declaring %v, kept mask %b, keyring %d", name, declared, kept, state)

				m, _ := secretModel(t, []config.Command{{Name: name, Secrets: declared}},
					map[string]string{name: printsSecrets}, store)
				m, _ = press(t, m, keyText("i"))
				m = runTyped(t, m, "/"+name)
				got := m.editorFor(fieldComposer).text

				if !runs {
					if strings.Contains(got, "done") {
						t.Errorf("%s ran", what)
					}
					if firstMissing != "" && state == keyringReadable && !strings.Contains(m.status(), firstMissing) {
						t.Errorf("%s: status = %q, want it to name %s", what, m.status(), firstMissing)
					}
					for _, value := range keyring.snapshot() {
						if strings.Contains(m.status(), value) {
							t.Errorf("%s: status %q shows a secret", what, m.status())
						}
					}
					continue
				}
				var printed []string
				for line := range strings.SplitSeq(got, "\n") {
					if strings.HasPrefix(line, "KITH_SECRET_") {
						printed = append(printed, line)
					}
				}
				var wanted []string
				for env, value := range want {
					wanted = append(wanted, env+"="+value)
				}
				slices.Sort(wanted)
				if !slices.Equal(printed, wanted) || !strings.Contains(got, "done") {
					t.Errorf("%s: the script saw %q, want %q", what, printed, wanted)
				}
			}
		}
	}
}

// How adding a template ends, step by step.
type templateCase struct {
	tmpl      string
	taken     string // "", "block" or "file": what already holds its name
	keyring   keyringState
	keepsLeft int // writes the keyring takes before refusing; -1 never
	typed     int // secrets typed before esc; -1 types them all
}

// Adding a template writes its script and block only when its name is free and every
// secret was typed and kept; anything else writes nothing, never touches an existing
// script, and never shows a typed secret. Generated over every template, each way its
// name can be taken, each keyring state, a keyring refusing at each write, and stopping
// at each secret.
func TestATemplateIsWrittenWholeOrNotAtAll(t *testing.T) {
	t.Parallel()

	all, err := cmdtemplates.All()
	if err != nil {
		t.Fatal(err)
	}
	var cases []templateCase
	for _, tmpl := range all {
		n := len(tmpl.Block.Secrets)
		for _, taken := range []string{"", "block", "file"} {
			for _, state := range []keyringState{keyringNone, keyringReadable} {
				for keeps := -1; keeps < n; keeps++ {
					for typed := -1; typed < n; typed++ {
						if (state == keyringNone || n == 0) && (keeps != -1 || typed != -1) {
							continue // nothing more to vary
						}
						cases = append(cases, templateCase{tmpl: tmpl.Name, taken: taken, keyring: state, keepsLeft: keeps, typed: typed})
					}
				}
			}
		}
	}
	for _, c := range cases {
		tmpl := all[slices.IndexFunc(all, func(t cmdtemplates.Template) bool { return t.Name == c.tmpl })]
		checkTemplateCase(t, tmpl, c)
	}
}

func checkTemplateCase(t *testing.T, tmpl cmdtemplates.Template, c templateCase) {
	t.Helper()
	what := fmt.Sprintf("%+v", c)
	name := tmpl.Block.Name
	var blocks []config.Command
	scripts := map[string]string{}
	const theirs = "#!/bin/sh\necho mine\n"
	switch c.taken {
	case "block":
		blocks = append(blocks, config.Command{Name: strings.ToUpper(name), Output: "none"})
	case "file":
		scripts[name] = theirs
	}
	keyring := newFakeKeyring()
	keyring.keepsLeft = c.keepsLeft
	var store Secrets
	if c.keyring == keyringReadable {
		store = keyring
	}
	m, dir := secretModel(t, blocks, scripts, store)

	m, cmd := m.chooseCommandTemplate(tmpl.Name)
	m = deliver(t, m, cmd)
	asked := m.prompt.kind == promptCommandSecret
	if asked && (c.taken != "" || c.keyring == keyringNone) {
		t.Errorf("%+v: secrets were asked for a template that cannot be added", c)
	}
	var typed []string
	for i := range tmpl.Block.Secrets {
		if m.prompt.kind != promptCommandSecret {
			break
		}
		if c.typed == i {
			m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			m = deliver(t, m, cmd)
			break
		}
		value := fmt.Sprintf("typed-%s-%d", name, i)
		typed = append(typed, value)
		m = typeInto(t, m, value)
		m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m = deliver(t, m, cmd)
	}

	n := len(tmpl.Block.Secrets)
	free := c.taken == ""
	keyringOK := n == 0 || (c.keyring == keyringReadable && (c.keepsLeft == -1 || c.keepsLeft >= n))
	allTyped := c.typed == -1 || n == 0
	want := free && keyringOK && allTyped

	script, readErr := os.ReadFile(filepath.Join(dir, name))
	_, configured := m.conf.base.CommandFor(name)
	switch {
	case c.taken == "file":
		if string(script) != theirs {
			t.Errorf("%s: an existing script was overwritten: %q", what, script)
		}
	case want:
		if readErr != nil || !bytes.Equal(script, tmpl.Script) {
			t.Errorf("%s: script = %q, %v, want the template's", what, script, readErr)
		}
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s: the script is not executable", what)
		}
		if !configured {
			t.Errorf("%s: no [[command]] block was added", what)
		}
		for i, secret := range tmpl.Block.Secrets {
			if got := keyring.snapshot()[config.SecretRef(name, secret)]; got != typed[i] {
				t.Errorf("%s: kept %s = %q, want what was typed", what, secret, got)
			}
		}
	default:
		if readErr == nil {
			t.Errorf("%s: a script was written", what)
		}
		if configured && c.taken != "block" {
			t.Errorf("%s: a block was added", what)
		}
	}
	if c.taken != "" && len(keyring.snapshot()) > 0 {
		t.Errorf("%s: secrets were kept for a name already taken: %v", what, keyring.snapshot())
	}
	if c.taken == "block" && len(m.conf.base.UserCommands) != 1 {
		t.Errorf("%s: blocks = %+v, want the existing one alone", what, m.conf.base.UserCommands)
	}
	if m.choosing.secret.values != nil {
		t.Errorf("%s: typed secrets are still held by the model", what)
	}
	if cfg, err := os.ReadFile(m.conf.path); err == nil {
		for _, v := range typed {
			if strings.Contains(string(cfg), v) {
				t.Errorf("%s: the config file holds a typed secret", what)
			}
		}
	}
	for _, v := range typed {
		if strings.Contains(m.status(), v) || strings.Contains(string(script), v) {
			t.Errorf("%s: a typed secret shows (status %q)", what, m.status())
		}
	}
}

// A secret is typed as dots.
func TestACommandSecretIsTypedAsDots(t *testing.T) {
	t.Parallel()

	m, _ := secretModel(t, nil, nil, newFakeKeyring())
	m, _ = m.chooseCommandTemplate("zoom")
	m = typeInto(t, m, "hunter2")
	if !m.loginSecret() || strings.Contains(m.promptValue(80), "hunter2") {
		t.Errorf("prompt = %q, want dots", m.promptValue(80))
	}
}

// Settings lists the templates from the commands list, and a command's missing secret
// shows on its row, and on its secret's row.
func TestSettingsShowsTemplatesAndMissingSecrets(t *testing.T) {
	t.Parallel()

	keyring := newFakeKeyring()
	keyring.kept[config.SecretRef("zoom", "client_id")] = "kept-id"
	m, _ := secretModel(t, []config.Command{{Name: "zoom", Secrets: []string{"client_id", "client_secret"}}}, nil, keyring)
	m = deliver(t, m, m.checkCommandSecretsCmd())

	t1, _ := recordTable(commandsTable)
	rows := m.recordRows(t1)
	if rows[0].show(m.conf.base) != "missing client_secret" {
		t.Errorf("row = %q, want the missing secret named", rows[0].show(m.conf.base))
	}
	if !slices.ContainsFunc(rows, func(s setting) bool { return s.label == "Add from template" }) {
		t.Error("the commands list has no Add from template row")
	}
	fields := m.recordFieldRows(t1, 0)
	shown := map[string]string{}
	for _, s := range fields {
		shown[s.label] = s.show(m.conf.base)
	}
	if shown["Secret · client_id"] != "kept" || shown["Secret · client_secret"] != "missing" {
		t.Errorf("secret rows = %v", shown)
	}

	m = m.openCommandTemplates()
	if m.picker.kind != pickerCommandTemplate || len(m.picker.all) < 5 {
		t.Fatalf("picker = %v with %d rows, want every template", m.picker.kind, len(m.picker.all))
	}
	for _, item := range m.picker.all {
		if item.detail == "" {
			t.Errorf("template %s shows no line on what it needs", item.label)
		}
	}
}

// Setting one secret of a configured command keeps it under that command.
func TestASecretIsSetFromItsRow(t *testing.T) {
	t.Parallel()

	keyring := newFakeKeyring()
	m, _ := secretModel(t, []config.Command{{Name: "zoom", Secrets: []string{"client_secret"}}}, nil, keyring)
	m, _ = m.setCommandSecret("zoom", "client_secret")
	m = typeInto(t, m, "s3cret")
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliver(t, m, cmd)
	if got := keyring.snapshot()[config.SecretRef("zoom", "client_secret")]; got != "s3cret" {
		t.Errorf("kept = %q, want what was typed", got)
	}
	if strings.Contains(m.status(), "s3cret") {
		t.Errorf("status %q shows the secret", m.status())
	}
}
