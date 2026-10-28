package config

import (
	"strings"
	"time"
)

// Commands is [commands] in the bridge's config: where the slash commands you write
// yourself live.
type Commands struct {
	Dir string `toml:"dir"` // empty is the `commands` folder beside the config
}

// DeviceCommands is [commands] in device.toml: how long a command may run here.
type DeviceCommands struct {
	Timeout int `toml:"timeout"` // seconds; 0 is DefaultCommandTimeout
}

// Command is one [[command]]: a command's settings.
type Command struct {
	Name   string   `toml:"name"`   // the file name, without the slash
	Needs  []string `toml:"needs"`  // message, url, history:n
	Output string   `toml:"output"` // compose (default), send, status, pager, clipboard, none
	Keys   string   `toml:"keys"`   // bindings, written as in [keys]
	In     []string `toml:"in"`     // place vocabulary; empty is everywhere
	// Secrets are the names of the keyring values it is given, as $KITH_SECRET_<NAME>.
	Secrets []string `toml:"secrets"`
}

// SecretEnv is the variable a command reads its secret name from.
func SecretEnv(name string) string { return "KITH_SECRET_" + strings.ToUpper(name) }

// SecretRef is where the keyring keeps a command's secret.
func SecretRef(command, name string) string { return "command|" + commandName(command) + "|" + name }

// CommandFor returns a command's block, matched case-insensitively without the slash.
func (d Device) CommandFor(command string) (Command, bool) {
	command = commandName(command)
	for _, block := range d.UserCommands {
		if commandName(block.Name) == command {
			return block, true
		}
	}
	return Command{}, false
}

// commandName is a command name normalized for matching.
func commandName(name string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
}

// DefaultCommandTimeout is how long a user command may run by default.
const DefaultCommandTimeout = 15 * time.Second

// RunTimeout is how long a command may run: the configured seconds, else the default.
func (c DeviceCommands) RunTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultCommandTimeout
	}
	return time.Duration(c.Timeout) * time.Second
}
