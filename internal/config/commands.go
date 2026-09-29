package config

import (
	"strings"
	"time"
)

// Commands is [commands]: the slash commands you write yourself.
type Commands struct {
	Dir     string   `toml:"dir"`     // empty is the `commands` folder beside the config
	Timeout int      `toml:"timeout"` // seconds; 0 is DefaultCommandTimeout
	Scripts []Script `toml:"script"`
}

// Script is one [[commands.script]]: a command's settings.
type Script struct {
	Name   string   `toml:"name"`   // the file name, without the slash
	Needs  []string `toml:"needs"`  // message, url, history:n
	Output string   `toml:"output"` // compose (default), send, status, pager, clipboard, none
	Keys   string   `toml:"keys"`   // bindings, written as in [keys]
}

// ScriptFor returns a command's block, matched without the slash and
// case-insensitively.
func (c Commands) ScriptFor(command string) (Script, bool) {
	command = commandName(command)
	for _, script := range c.Scripts {
		if commandName(script.Name) == command {
			return script, true
		}
	}
	return Script{}, false
}

// commandName is a command name normalized for matching.
func commandName(name string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
}

// DefaultCommandTimeout is how long a user command may run when nothing says otherwise.
const DefaultCommandTimeout = 15 * time.Second

// RunTimeout is how long a command may run: the configured seconds, else the default.
func (c Commands) RunTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultCommandTimeout
	}
	return time.Duration(c.Timeout) * time.Second
}
