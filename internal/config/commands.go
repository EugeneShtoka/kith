package config

import (
	"strings"
	"time"
)

// Commands is [commands]: where the slash commands you write yourself live, and how long they run.
type Commands struct {
	Dir     string `toml:"dir"`     // empty is the `commands` folder beside the config
	Timeout int    `toml:"timeout"` // seconds; 0 is DefaultCommandTimeout
}

// Command is one [[command]]: a command's settings.
type Command struct {
	Name   string   `toml:"name"`   // the file name, without the slash
	Needs  []string `toml:"needs"`  // message, url, history:n
	Output string   `toml:"output"` // compose (default), send, status, pager, clipboard, none
	Keys   string   `toml:"keys"`   // bindings, written as in [keys]
	In     []string `toml:"in"`     // place vocabulary; empty is everywhere
}

// CommandFor returns a command's block, matched case-insensitively without the slash.
func (c Config) CommandFor(command string) (Command, bool) {
	command = commandName(command)
	for _, block := range c.UserCommands {
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
func (c Commands) RunTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultCommandTimeout
	}
	return time.Duration(c.Timeout) * time.Second
}
