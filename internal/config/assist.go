package config

import (
	"strings"
	"time"
)

// Assist is [assist]: the remote model that answers questions about your conversations.
type Assist struct {
	Endpoint         string      `toml:"endpoint"` // OpenAI-compatible chat completions URL; empty off
	Model            string      `toml:"model"`
	KeyRef           string      `toml:"key_ref"` // keyring entry name, never the key
	Rooms            []string    `toml:"rooms"`   // place vocabulary allow list
	Except           []string    `toml:"except"`  // deny list, used when Rooms is empty
	Encrypted        bool        `toml:"encrypted"`
	Budget           int         `toml:"budget"`         // tokens; 0 DefaultModelBudget
	MaxPerMinute     int         `toml:"max_per_minute"` // 0 DefaultModelPerMinute
	Timeout          string      `toml:"timeout"`        // a duration; empty DefaultModelTimeout
	Rewrite          ModelPrompt `toml:"rewrite"`
	Summary          ModelPrompt `toml:"summary"`
	ThreadName       ModelPrompt `toml:"thread_name"`
	Todo             ModelPrompt `toml:"todo"`
	TodoRooms        int         `toml:"todo_rooms"`
	NameThreads      bool        `toml:"name_threads"`
	NameThreadsAfter int         `toml:"name_threads_after"` // replies; 0 DefaultNameThreadsAfter
}

// ModelPrompt is one [assist.*] task: its instruction and the bounds on its answer.
type ModelPrompt struct {
	System      string   `toml:"system"` // empty is the built-in default
	Prompt      string   `toml:"prompt"` // empty is the built-in default
	MaxTokens   int      `toml:"max_tokens"`
	Temperature float64  `toml:"temperature"`
	Effort      string   `toml:"reasoning_effort"`
	Model       string   `toml:"model"`  // empty is [assist] model
	Budget      int      `toml:"budget"` // tokens; 0 is [assist] budget
	Stop        []string `toml:"stop"`
}

// AssistEnabled reports whether there is an endpoint to ask at all.
func (c Assist) AssistEnabled() bool { return strings.TrimSpace(c.Endpoint) != "" }

// BudgetOrDefault is how much conversation may be quoted, defaulting to a short one.
func (c Assist) BudgetOrDefault() int {
	if c.Budget <= 0 {
		return DefaultModelBudget
	}
	return c.Budget
}

// TimeoutOrDefault bounds one request.
func (c Assist) TimeoutOrDefault() time.Duration {
	return durationOr(c.Timeout, DefaultModelTimeout)
}

// RateOrDefault is how many requests a minute may go out.
func (c Assist) RateOrDefault() int {
	if c.MaxPerMinute <= 0 {
		return DefaultModelPerMinute
	}
	return c.MaxPerMinute
}

// Model-layer defaults.
const (
	DefaultModelBudget    = 1500
	DefaultModelTimeout   = 4 * time.Second
	DefaultModelPerMinute = 20
	// DefaultNameThreadsAfter is how many replies a thread carries before it is named.
	DefaultNameThreadsAfter = 2
)

// NameThreadsAfterOrDefault is how many replies a thread needs before it is named.
func (c Assist) NameThreadsAfterOrDefault() int {
	if c.NameThreadsAfter > 0 {
		return c.NameThreadsAfter
	}
	return DefaultNameThreadsAfter
}

// Agent is [agent.*]: what `kith-mcp`, an assistant attached to this account, may read
// and write.
type Agent struct {
	Read  AgentRead  `toml:"read"`
	Write AgentWrite `toml:"write"`
}

// AgentRead is [agent.read]: the rooms an assistant may read.
type AgentRead struct {
	Rooms     []string `toml:"rooms"`  // place vocabulary; empty shares nothing
	Except    []string `toml:"except"` // subtracted from Rooms
	Encrypted bool     `toml:"encrypted"`
}

// AgentWrite is [agent.write]: where an assistant may draft, and where it may send.
type AgentWrite struct {
	Rooms     []string `toml:"rooms"`  // intersected with what Read allows
	Except    []string `toml:"except"` // deny list, used when Rooms is empty
	Encrypted bool     `toml:"encrypted"`
	Send      []string `toml:"send"`     // where it posts unreviewed
	Cooldown  string   `toml:"cooldown"` // a duration
}
