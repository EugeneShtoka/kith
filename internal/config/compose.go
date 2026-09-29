package config

import (
	"os"
	"slices"
	"strings"
	"time"
)

// Composer is [composer]: writing a message.
type Composer struct {
	Editor     string `toml:"editor"`      // split on spaces; empty $VISUAL, $EDITOR, vi
	FileSuffix string `toml:"file_suffix"` // empty is ".md"
	Markdown   *bool  `toml:"markdown"`
}

// MarkdownEnabled reports whether the composer renders Markdown, defaulting to true
// for a config that does not mention it.
func (c Composer) MarkdownEnabled() bool { return enabled(c.Markdown) }

// defaultEditor is used when neither the config nor the environment names one. vi
// is the editor POSIX requires to exist.
const defaultEditor = "vi"

// EditorCommand is the program and arguments the draft is handed to, resolved through
// the config, then $VISUAL, then $EDITOR, then vi. It never returns an empty command.
func (c Composer) EditorCommand() []string {
	for _, candidate := range []string{c.Editor, os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if fields := strings.Fields(candidate); len(fields) > 0 {
			return fields
		}
	}
	return []string{defaultEditor}
}

// FileSuffixOrDefault is the temporary file's extension, always starting with a dot.
func (c Composer) FileSuffixOrDefault() string {
	suffix := strings.TrimSpace(c.FileSuffix)
	if suffix == "" {
		return ".md"
	}
	if !strings.HasPrefix(suffix, ".") {
		return "." + suffix
	}
	return suffix
}

// Spell is [spell]: the spellchecker.
type Spell struct {
	Enabled             *bool       `toml:"enabled"`
	Command             string      `toml:"command"`      // empty is hunspell
	Dictionaries        []string    `toml:"dictionaries"` // empty is every one installed
	Declined            []string    `toml:"declined"`
	FrequenciesDeclined []string    `toml:"frequencies_declined"`
	Underline           string      `toml:"underline"` // SpellUnderlines; empty is "curly"
	CheckBeforeSend     bool        `toml:"check_before_send"`
	Autocorrect         Autocorrect `toml:"autocorrect"` // AutocorrectModes; empty is off
	FlagRareWords       bool        `toml:"flag_rare_words"`
	RareUnderline       string      `toml:"rare_underline"` // RareUnderlines
	RareBeforeSend      bool        `toml:"rare_before_send"`
	RareRatio           int         `toml:"rare_ratio"` // 0 is the built-in default
}

// Autocorrect is `[spell] autocorrect`: which classes of word may be rewritten as you
// finish typing them.
type Autocorrect string

// Corrects reports whether autocorrect may rewrite the given class of word. An empty
// value — the key absent from the file — is "off", which is what it has always been.
func (s Spell) Corrects(mode string) bool {
	switch string(s.Autocorrect) {
	case AutocorrectAll:
		return mode == AutocorrectMisspellings || mode == AutocorrectRare
	case AutocorrectMisspellings, AutocorrectRare:
		return string(s.Autocorrect) == mode
	}
	return false
}

// FrequenciesDeclinedFor reports whether a language's word counts were offered and
// turned down.
func (s Spell) FrequenciesDeclinedFor(tag string) bool {
	return slices.Contains(s.FrequenciesDeclined, tag)
}

// SpellDeclined reports whether a dictionary was offered and turned down.
func (s Spell) SpellDeclined(tag string) bool { return slices.Contains(s.Declined, tag) }

// SpellEnabled reports whether checking is on, defaulting to true for a config
// written before the setting existed.
func (s Spell) SpellEnabled() bool { return enabled(s.Enabled) }

// EngineOrDefault is the configured engine, or hunspell.
func (s Spell) EngineOrDefault() string {
	if s.Command == "" {
		return "hunspell"
	}
	return s.Command
}

// Complete is [complete]: finishing a word from what has actually been said.
type Complete struct {
	Enabled *bool         `toml:"enabled"`
	Ghost   *bool         `toml:"ghost"`
	Ratio   int           `toml:"ratio"`   // 0 is DefaultCompleteRatio
	Scope   string        `toml:"scope"`   // CompleteScopes; empty is "room"
	Sources []string      `toml:"sources"` // CompleteSources; empty is both
	Model   CompleteModel `toml:"model"`
}

// CompleteModel is [complete.model]: the local language model that finishes words.
type CompleteModel struct {
	Enabled     *bool  `toml:"enabled"`
	Command     string `toml:"command"` // empty is llama-server
	Model       string `toml:"model"`   // a manifest tag or a .gguf path
	Threads     int    `toml:"threads"` // 0 is llama.cpp's default
	Context     int    `toml:"context"` // tokens; 0 is the default
	Port        int    `toml:"port"`    // loopback; 0 any free port
	Idle        string `toml:"idle"`    // a duration; empty DefaultLocalIdle, <= 0 never
	Startup     string `toml:"startup"` // a duration; empty DefaultLocalStartup
	Timeout     string `toml:"timeout"` // a duration; empty DefaultLocalTimeout
	Options     int    `toml:"options"` // words offered at once, 1-5
	Budget      int    `toml:"budget"`  // tokens of context
	Recent      int    `toml:"recent"`
	Gap         string `toml:"gap"` // a duration
	BeforeReply int    `toml:"before_reply"`
	Declined    bool   `toml:"declined"`
}

// ModelEnabled reports whether the completion model may run, defaulting to on: the
// weights being installed is the decision, and a second switch to flip after the
// download would be the same question asked twice.
func (c CompleteModel) ModelEnabled() bool { return enabled(c.Enabled) }

// CommandOrDefault is llama.cpp's server, defaulting to the name it installs as.
func (c CompleteModel) CommandOrDefault() string {
	if strings.TrimSpace(c.Command) == "" {
		return "llama-server"
	}
	return c.Command
}

// IdleOrDefault is how long the server may sit unused.
func (c CompleteModel) IdleOrDefault() time.Duration {
	spelled := strings.TrimSpace(c.Idle)
	if spelled == "" {
		return DefaultLocalIdle
	}
	parsed, err := time.ParseDuration(spelled)
	if err != nil {
		return DefaultLocalIdle
	}
	if parsed <= 0 {
		// Negative is what internal/llamacpp reads as "never stop it", and an explicit
		// zero in a config file means the same thing said the obvious way.
		return -1
	}
	return parsed
}

// StartupOrDefault is how long a spawn may take before it is called a failure.
func (c CompleteModel) StartupOrDefault() time.Duration {
	return durationOr(c.Startup, DefaultLocalStartup)
}

// TimeoutOrDefault bounds one prediction. A suggestion that arrives after the word has
// been typed is not a suggestion.
func (c CompleteModel) TimeoutOrDefault() time.Duration {
	return durationOr(c.Timeout, DefaultLocalTimeout)
}

// The local model's defaults: several times the observed load and answer times.
const (
	DefaultLocalIdle    = 10 * time.Minute
	DefaultLocalStartup = 20 * time.Second
	DefaultLocalTimeout = time.Second
)

// CompleteEnabled reports whether completion is on, defaulting to true for a config
// written before the setting existed.
func (c Complete) CompleteEnabled() bool { return enabled(c.Enabled) }

// GhostEnabled reports whether the inline suggestion draws.
func (c Complete) GhostEnabled() bool { return enabled(c.Ghost) }

// RatioOrDefault is the dominance the ghost requires, defaulting to three.
func (c Complete) RatioOrDefault() int {
	if c.Ratio <= 0 {
		return DefaultCompleteRatio
	}
	return c.Ratio
}

// ScopeOrDefault is the vocabulary's width, defaulting to the room — which is not the
// narrowest answer it sounds like: every scope keeps a plain count over the whole
// cache, so "room" means "weigh this room's words highest", not "look only here".
func (c Complete) ScopeOrDefault() string {
	if c.Scope == "" {
		// The constant, not the word: vocabulary.go exists to name every accepted
		// config string.
		return CompleteScopeRoom
	}
	return c.Scope
}

// DefaultCompleteRatio is the default ghost dominance ratio.
const DefaultCompleteRatio = 3
