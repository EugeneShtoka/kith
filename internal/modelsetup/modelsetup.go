// Package modelsetup derives the daemon's model layer from the config: the prompts
// the remote model runs and the local completion server's settings. Daemon-only, so
// the clients do not link the model clients or the process that serves a model.
package modelsetup

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/llamacpp"
	"github.com/EugeneShtoka/kith/internal/llamacpp/models"
	"github.com/EugeneShtoka/kith/internal/llm"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Default prompts. Short on purpose — a long instruction to a small local model is
// mostly noise, and these have to work on llama-3.1-8b as well as on something large.
const (
	defaultRewriteSystem = "You rewrite chat messages. " +
		"Apply the instruction to the draft and reply with the rewritten message only — " +
		"no preamble, no quotes, no explanation. Keep it in {language}."
	defaultRewritePrompt = "Instruction: {instruction}\n\nDraft:\n{draft}"
	// The summary is written to the person asking, which is what {me} is for.
	defaultSummarySystem = "You summarize a chat conversation for {me}, who is catching up. " +
		"Their own messages are marked 'You:'. Other people may refer to them by name — any of: {names} — " +
		"and anything addressed to those names is addressed to you, so write it as 'you'. " +
		"Answer in two parts, in this order. " +
		"Part one: if anything is waiting on them — asked of them, addressed to them, or needing their answer — " +
		"write the line 'Waiting on you:' and then one line per item as '- who — what they are waiting for'. " +
		"If there is genuinely nothing, write the single line 'Waiting on you: none' and nothing else for part one. " +
		"Part two: write the line 'Also discussed:' and then group by person — one line 'Name:' per person, " +
		"then one indented line '  - ' per distinct thing they raised. " +
		"Each person appears exactly once, however many things they raised. " +
		"Write plain text with no Markdown — no asterisks, no bold, no headings. " +
		"Write in {language}. Never invent anything that is not in the conversation."
	defaultSummaryPrompt = "Conversation:\n{context}"
	// The todo prompt asks for one line per thing owed, because the answer is a list
	// somebody scans rather than prose they read — and asks for nothing where there is
	// nothing, because a model given several quiet rooms will otherwise invent an
	// obligation to be helpful.
	defaultTodoSystem = "You read several chat conversations and list what {me} still owes people. " +
		"One bullet per outstanding thing, in the form 'room — who — what they are waiting for'. " +
		"Include only things actually asked of {me} and not yet answered by them; " +
		"if a conversation has none, leave it out entirely. " +
		"If there is nothing anywhere, reply with exactly: nothing outstanding. " +
		"Write plain text with no Markdown. Never invent an obligation. Write in {language}."
	defaultTodoPrompt = "Conversations:\n{context}"
)

// Default bounds. The completion ceiling is small for the same reason its prompt asks for
// few words — and because it is also the cost of every keystroke that triggers one.
const (
	defaultRewriteTokens = 800
	// A summary is read rather than typed into a sentence, so its ceiling is the one
	// place here that is about the answer's length rather than the model's thinking.
	defaultSummaryTokens = 1600
	defaultTodoTokens    = 1200
	// defaultEffort is the least thinking a reasoning model will do.
	defaultEffort = "low"

	// The thread namer.
	defaultThreadNameSystem = "You name conversations. You reply with the name and nothing else: " +
		"no quotes, no punctuation at the end, no explanation."
	defaultThreadNamePrompt = "Below is a thread from a chat. " +
		"Give it a name of at most six words that says what it is about — " +
		"specific enough that someone who was in it recognizes it in a list, " +
		"and written in {language}.\n\n{context}"
	// Twenty-four is about three times the six words asked for, which leaves room for a
	// model that counts differently without leaving room for a paragraph.
	defaultThreadNameTokens = 24
)

// ModelTasks are the prompts the model layer will run, by name, with the defaults
// filled in for anything the config left out.
func ModelTasks(cfg config.Assist) map[string]llm.Task {
	return map[string]llm.Task{
		domain.ModelRewrite: {
			Name:        domain.ModelRewrite,
			System:      or(cfg.Rewrite.System, defaultRewriteSystem),
			Prompt:      or(cfg.Rewrite.Prompt, defaultRewritePrompt),
			MaxTokens:   orInt(cfg.Rewrite.MaxTokens, defaultRewriteTokens),
			Temperature: cfg.Rewrite.Temperature,
			Effort:      or(cfg.Rewrite.Effort, defaultEffort),
			Model:       cfg.Rewrite.Model,
			Stop:        cfg.Rewrite.Stop,
		},
		domain.ModelTodo: {
			Name:        domain.ModelTodo,
			System:      or(cfg.Todo.System, defaultTodoSystem),
			Prompt:      or(cfg.Todo.Prompt, defaultTodoPrompt),
			MaxTokens:   orInt(cfg.Todo.MaxTokens, defaultTodoTokens),
			Temperature: cfg.Todo.Temperature,
			Effort:      or(cfg.Todo.Effort, defaultEffort),
			Model:       cfg.Todo.Model,
			Stop:        cfg.Todo.Stop,
		},
		domain.ModelThreadName: {
			Name:        domain.ModelThreadName,
			System:      or(cfg.ThreadName.System, defaultThreadNameSystem),
			Prompt:      or(cfg.ThreadName.Prompt, defaultThreadNamePrompt),
			MaxTokens:   orInt(cfg.ThreadName.MaxTokens, defaultThreadNameTokens),
			Temperature: cfg.ThreadName.Temperature,
			Effort:      or(cfg.ThreadName.Effort, defaultEffort),
			Model:       cfg.ThreadName.Model,
			// A newline ends it.
			Stop: orStrings(cfg.ThreadName.Stop, []string{"\n"}),
		},
		domain.ModelSummary: {
			Name:        domain.ModelSummary,
			System:      or(cfg.Summary.System, defaultSummarySystem),
			Prompt:      or(cfg.Summary.Prompt, defaultSummaryPrompt),
			MaxTokens:   orInt(cfg.Summary.MaxTokens, defaultSummaryTokens),
			Temperature: cfg.Summary.Temperature,
			Effort:      or(cfg.Summary.Effort, defaultEffort),
			Model:       cfg.Summary.Model,
			// No stop sequence: a summary is several lines by nature, and the newline
			// that ends a continuation would end this after its first bullet.
			Stop: cfg.Summary.Stop,
		},
	}
}

// CompletionModel derives the settings for the model that finishes your words, and
// reports whether it is switched on.
func CompletionModel(cfg config.CompleteModel, dataHome string) (llamacpp.Settings, bool) {
	if !cfg.ModelEnabled() {
		return llamacpp.Settings{}, false
	}
	model, _ := weightsFor(cfg.Model, setup.ModelDir(dataHome))
	return llamacpp.Settings{
		Command: cfg.CommandOrDefault(),
		Model:   model,
		Port:    cfg.Port,
		Threads: cfg.Threads,
		Context: cfg.Context,
		Idle:    cfg.IdleOrDefault(),
		Startup: cfg.StartupOrDefault(),
		Timeout: cfg.TimeoutOrDefault(),
	}, true
}

// weightsFor resolves what `model` names, in the three spellings it can take, and is
// false when nothing answers to it.
func weightsFor(named, dir string) (string, bool) {
	named = strings.TrimSpace(named)
	switch {
	case named == "":
		_, path, ok := models.Any(dir)
		return path, ok
	case strings.ContainsRune(named, filepath.Separator) || strings.HasSuffix(named, ".gguf"):
		if info, err := os.Stat(named); err != nil || info.IsDir() {
			return "", false
		}
		return named, true
	default:
		return models.Installed(dir, named)
	}
}

func or(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// orStrings is or for a list: an unset list takes the fallback, an empty one is a
// deliberate "none" and keeps it.
func orStrings(value, fallback []string) []string {
	if value == nil {
		return fallback
	}
	return value
}

func orInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
