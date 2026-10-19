package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/cmdtemplates"
	"github.com/EugeneShtoka/kith/internal/config"
)

// A command can declare secrets (`secrets = ["client_secret"]`). kith asks for each in a
// masked prompt, keeps it in the OS keyring and hands it to the script as
// $KITH_SECRET_CLIENT_SECRET: never on argv, never in the config. No keyring means a
// command with secrets cannot be set up or run; there is no plaintext fallback.
//
// Settings → Advanced → Commands → Add from template writes one of cmdtemplates' scripts
// and its block, asking for its secrets first: nothing is written until every secret is
// kept, and an existing command of that name is never overwritten.

// Secrets is the OS keyring as commands use it.
type Secrets interface {
	// Secret is the value kept under ref; ok false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	// StoreSecret keeps value under ref.
	StoreSecret(ref, value string) error
}

// errNoKeyring is a command's secrets with nowhere to keep them.
var errNoKeyring = errors.New("there is no keyring here to keep its secrets in")

// WithSecrets wires in the keyring; without it a command with secrets refuses.
func (m Model) WithSecrets(s Secrets) Model {
	m.secrets = s
	return m
}

// secretWalk is a command's secrets being asked for, one prompt each: a template's before
// it is written (tmpl set), or one secret of a configured command.
type secretWalk struct {
	command string
	names   []string // what is asked, in order
	values  []string // what was typed, so far
	tmpl    *cmdtemplates.Template
}

// commandsTable is Settings' list of [[command]] blocks.
const commandsTable = recordPrefix + "command"

// templateRow is the commands list's row for adding one from a template.
func templateRow(t config.RecordTable) setting {
	return setting{
		key: commandsTable + "#template", group: commandsTable, kind: settingOpen,
		label: "Add from template", doc: t.Doc,
		show: func(config.Config) string { return "" },
		open: func(m Model) (Model, tea.Cmd) { return m.openCommandTemplates(), nil },
	}
}

// openCommandTemplates lists the templates, each with its line on what it needs.
func (m Model) openCommandTemplates() Model {
	all, err := cmdtemplates.All()
	if err != nil {
		return m.sayErr("no templates", err)
	}
	items := make([]pickerItem, 0, len(all))
	for i := range all {
		t := &all[i]
		items = append(items, pickerItem{label: "/" + t.Block.Name, detail: t.About, value: t.Name, match: t.Name + " " + t.Block.Name})
	}
	m.picker = newPicker(pickerCommandTemplate, items)
	return m
}

// chooseCommandTemplate starts adding one: refused when the name is taken, or when it has
// secrets and there is no keyring.
func (m Model) chooseCommandTemplate(name string) (Model, tea.Cmd) {
	all, err := cmdtemplates.All()
	if err != nil {
		return m.sayErr("no templates", err), nil
	}
	at := slices.IndexFunc(all, func(t cmdtemplates.Template) bool { return t.Name == name })
	if at < 0 {
		return m, nil
	}
	tmpl := all[at]
	page := m.settingsIn(commandsTable, commandsTable+"#template")
	command := "/" + tmpl.Block.Name
	if _, taken := m.conf.base.CommandFor(command); taken {
		return page.say("a command is already called " + command + " — nothing written"), nil
	}
	if _, taken := m.userCommand(command); taken {
		return page.say(command + " is already in your commands folder — nothing written"), nil
	}
	if len(tmpl.Block.Secrets) > 0 && m.secrets == nil {
		return page.say(command + ": " + errNoKeyring.Error()), nil
	}
	page.choosing.secret = secretWalk{command: tmpl.Block.Name, names: tmpl.Block.Secrets, tmpl: &tmpl}
	return page.nextSecret()
}

// setCommandSecret asks for one secret of a configured command, replacing what is kept.
func (m Model) setCommandSecret(command, name string) (Model, tea.Cmd) {
	if m.secrets == nil {
		return m.say("/" + command + ": " + errNoKeyring.Error()), nil
	}
	m.choosing.secret = secretWalk{command: command, names: []string{name}}
	return m.nextSecret()
}

// nextSecret asks for the next secret, or keeps them all once none is left.
func (m Model) nextSecret() (Model, tea.Cmd) {
	walk := m.choosing.secret
	if len(walk.values) < len(walk.names) {
		return m.openPrompt(promptCommandSecret), nil
	}
	// The values leave the model here: only the command that keeps them holds them.
	m.choosing.secret = secretWalk{}
	return m.doing("keeping /" + walk.command + "'s secrets …"), m.keepSecretsCmd(walk)
}

// commandSecretLabel names the secret being asked for.
func (m Model) commandSecretLabel() string {
	walk := m.choosing.secret
	if len(walk.values) >= len(walk.names) {
		return ""
	}
	return fmt.Sprintf("/%s's %s (kept in the keyring): ", walk.command, walk.names[len(walk.values)])
}

func (m Model) submitCommandSecret(input string) (Model, tea.Cmd) {
	value := strings.TrimSpace(input)
	if value == "" {
		return m.openPrompt(promptCommandSecret).say("type it, or esc to stop — nothing is written until every one is"), nil
	}
	walk := m.choosing.secret
	walk.values = append(slices.Clip(walk.values), value)
	m.choosing.secret = walk
	return m.nextSecret()
}

func cancelCommandSecret(m Model) (Model, tea.Cmd) {
	m.choosing.secret = secretWalk{}
	return m.say("stopped — nothing written"), nil
}

// secretsKeptMsg is the keyring's answer, and for a template whether its script was
// written.
type secretsKeptMsg struct {
	command string
	tmpl    *cmdtemplates.Template
	err     error
}

// keepSecretsCmd keeps each secret, then writes a template's script: off the event loop,
// since the keyring is a D-Bus call and the folder a disk write. Any failure stops it
// before the script exists.
func (m Model) keepSecretsCmd(walk secretWalk) tea.Cmd {
	store := m.secrets
	dir, dirErr := m.commandsDir()
	return func() tea.Msg {
		done := secretsKeptMsg{command: walk.command, tmpl: walk.tmpl}
		if len(walk.names) > 0 && store == nil {
			done.err = errNoKeyring
			return done
		}
		for i, name := range walk.names {
			if err := store.StoreSecret(config.SecretRef(walk.command, name), walk.values[i]); err != nil {
				done.err = fmt.Errorf("keeping its %s: %w", name, err)
				return done
			}
		}
		if walk.tmpl != nil {
			done.err = writeTemplateScript(dir, dirErr, *walk.tmpl)
		}
		return done
	}
}

// writeTemplateScript writes the script, executable, refusing one already there.
func writeTemplateScript(dir string, dirErr error, tmpl cmdtemplates.Template) error {
	if dirErr != nil {
		return dirErr
	}
	if dir == "" {
		return errors.New("there is no commands folder to write it in")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- the user's own commands folder, listed like ~/bin
		return fmt.Errorf("make the commands folder: %w", err)
	}
	path := filepath.Join(dir, tmpl.Block.Name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755) // #nosec G302 G304 -- a script the user chose to add, executable by design
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s is already in your commands folder", tmpl.Block.Name)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", tmpl.Block.Name, err)
	}
	if _, err := f.Write(tmpl.Script); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write %s: %w", tmpl.Block.Name, err)
	}
	return f.Close() //nolint:wrapcheck // the path is in the error
}

// handleSecretsKept adds a template's block once its script is written, or says the
// secret was kept.
func (m Model) handleSecretsKept(msg secretsKeptMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("/"+msg.command, msg.err), nil
	}
	if msg.tmpl == nil {
		return m.say("kept /" + msg.command + "'s secret in the keyring"), m.checkCommandSecretsCmd()
	}
	cfg := m.conf.base.Clone()
	cfg.UserCommands = append(slices.Clip(cfg.UserCommands), msg.tmpl.Block)
	next, cmd := m.applyConfig(cfg, "added /"+msg.command+" from its template")
	return next.settingsIn(commandsTable, ""), tea.Batch(cmd, next.checkCommandSecretsCmd())
}

// commandSecretsMsg is which of the configured commands' secrets the keyring holds.
type commandSecretsMsg struct{ kept map[string]bool }

// checkCommandSecretsCmd asks the keyring which declared secrets it holds, for Settings
// to show the missing ones. An unreadable keyring leaves them unknown.
func (m Model) checkCommandSecretsCmd() tea.Cmd {
	store := m.secrets
	var refs []string
	for _, block := range m.conf.base.UserCommands {
		for _, name := range block.Secrets {
			refs = append(refs, config.SecretRef(block.Name, name))
		}
	}
	if store == nil || len(refs) == 0 {
		return nil
	}
	return func() tea.Msg {
		kept := map[string]bool{}
		for _, ref := range refs {
			if _, ok, err := store.Secret(ref); err == nil {
				kept[ref] = ok
			}
		}
		return commandSecretsMsg{kept: kept}
	}
}

func (m Model) handleCommandSecrets(msg commandSecretsMsg) (Model, tea.Cmd) {
	m.commandSecrets = msg.kept
	return m, nil
}

// missingSecrets are a command's declared secrets the keyring was found not to hold.
func (m Model) missingSecrets(block config.Command) []string {
	var out []string
	for _, name := range block.Secrets {
		if kept, known := m.commandSecrets[config.SecretRef(block.Name, name)]; known && !kept {
			out = append(out, name)
		}
	}
	return out
}

// secretWords is a secret's row value: kept, missing, or not yet asked.
func (m Model) secretWords(command, name string) string {
	kept, known := m.commandSecrets[config.SecretRef(command, name)]
	switch {
	case m.secrets == nil:
		return "no keyring"
	case !known:
		return ""
	case kept:
		return "kept"
	}
	return "missing"
}

// commandSecretRows are a command's secrets, each set from a masked prompt.
func (m Model) commandSecretRows(i int, group string) []setting {
	if i < 0 || i >= len(m.conf.base.UserCommands) {
		return nil
	}
	block := m.conf.base.UserCommands[i]
	command := strings.TrimPrefix(strings.TrimSpace(block.Name), "/")
	rows := make([]setting, 0, len(block.Secrets))
	for _, name := range block.Secrets {
		words := m.secretWords(command, name)
		rows = append(rows, setting{
			key: group + ".secret." + name, group: group, kind: settingOpen,
			label: "Secret · " + name,
			doc:   "Kept in the keyring and given to the script as $" + config.SecretEnv(name) + ", never on its command line. Enter replaces it.",
			show:  func(config.Config) string { return words },
			open:  func(m Model) (Model, tea.Cmd) { return m.setCommandSecret(command, name) },
		})
	}
	return rows
}

// secretReader reads a command's declared secrets when called: off the event loop, since
// the keyring is a D-Bus call.
func (m Model) secretReader(command string) func() ([]string, error) {
	block, _ := m.conf.base.CommandFor(command)
	store := m.secrets
	return func() ([]string, error) { return commandSecretEnv(store, block) }
}

// commandSecretEnv is the declared secrets as the script's environment, read off the
// event loop; a missing one is an error naming it, so the script never runs with an
// empty variable.
func commandSecretEnv(store Secrets, block config.Command) ([]string, error) {
	if len(block.Secrets) == 0 {
		return nil, nil
	}
	if store == nil {
		return nil, errNoKeyring
	}
	env := make([]string, 0, len(block.Secrets))
	for _, name := range block.Secrets {
		value, ok, err := store.Secret(config.SecretRef(block.Name, name))
		if err != nil {
			return nil, fmt.Errorf("could not read its secret %s: %w", name, err)
		}
		if !ok {
			return nil, fmt.Errorf("its secret %s is not set — Settings → Advanced → Commands", name)
		}
		env = append(env, config.SecretEnv(name)+"="+value)
	}
	return env, nil
}
