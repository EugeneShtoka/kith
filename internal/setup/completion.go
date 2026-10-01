package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/llamacpp/models"
)

// Turning `[complete.model]` into a running predictor's settings.

// ModelDir is where installed weights live: kith's own directory, beside the
// dictionaries and the frequency lists, for the same reasons — no root is needed,
// nothing else owns it, and what this client installed is what this client reads.
func ModelDir(dataHome string) string { return filepath.Join(dataHome, "kith", "models") }

// ModelNames refuses a `model` that cannot ever resolve to weights.
func ModelNames(cfg config.CompleteModel) error {
	named := strings.TrimSpace(cfg.Model)
	switch {
	case named == "":
		return nil
	case !strings.ContainsRune(named, filepath.Separator) && !strings.HasSuffix(named, ".gguf"):
		if _, ok := models.SourceFor(named); ok {
			return nil
		}
		return fmt.Errorf(
			"complete.model.model: %q is not a model this client knows and not a path to one.\n"+
				"        `kith --add-model list` names the ones it can install; a path to a .gguf\n"+
				"        of your own works too. (If this was the *remote* model's name, that setting\n"+
				"        moved to [assist] model when completion stopped going over the network.)",
			named)
	}
	if info, err := os.Stat(named); err != nil || info.IsDir() {
		return fmt.Errorf(
			"complete.model.model: %q is not a file this machine has.\n"+
				"        It names weights to run — a .gguf on disk — rather than a model on a\n"+
				"        server. (If this was the *remote* model's name, that setting moved to\n"+
				"        [assist] model when completion stopped going over the network.)",
			named)
	}
	return nil
}

// ModelDurations refuses a duration somebody wrote wrongly, which is the half of the
// config contract the OrDefault helpers cannot keep: they resolve, and resolving
// quietly is how a setting somebody believes they changed stays unchanged.
func ModelDurations(cfg config.CompleteModel) error {
	for _, field := range []struct {
		key     string
		spelled string
		// zeroOK marks the one duration where "0s" is an answer rather than an
		// omission: an idle window of zero means never stop the server.
		zeroOK bool
	}{
		{"complete.model.idle", cfg.Idle, true},
		{"complete.model.startup", cfg.Startup, false},
		{"complete.model.timeout", cfg.Timeout, false},
	} {
		spelled := strings.TrimSpace(field.spelled)
		if spelled == "" {
			continue
		}
		parsed, err := time.ParseDuration(spelled)
		if err != nil {
			return fmt.Errorf("%s: %q is not a duration (try \"10m\")", field.key, spelled)
		}
		if parsed < 0 {
			return fmt.Errorf("%s: %q must not be negative", field.key, spelled)
		}
		if parsed == 0 && !field.zeroOK {
			return fmt.Errorf("%s: %q leaves no time at all", field.key, spelled)
		}
	}
	return nil
}

// CompletionPick is what a completion may quote, resolved.
func CompletionPick(cfg config.CompleteModel) (domain.ContextPick, error) {
	// The raw values are checked before orInt sees them, because "0 or less means
	// unset" is right for an omitted key and wrong for `recent = -1`: one is a line
	// nobody wrote and the other is a line somebody wrote wrongly, and only the second
	// is worth refusing.
	if cfg.Recent < 0 || cfg.BeforeReply < 0 {
		return domain.ContextPick{}, fmt.Errorf(
			"complete.model: recent (%d) and before_reply (%d) are counts of messages, so they cannot be negative",
			cfg.Recent, cfg.BeforeReply)
	}
	pick := domain.ContextPick{
		Recent: orInt(cfg.Recent, domain.DefaultContextRecent),
		Gap:    domain.DefaultContextGap,
		Before: orInt(cfg.BeforeReply, domain.DefaultContextBefore),
	}
	if spelled := strings.TrimSpace(cfg.Gap); spelled != "" {
		gap, err := time.ParseDuration(spelled)
		if err != nil {
			return domain.ContextPick{}, fmt.Errorf(
				"complete.model.gap: %q is not a duration (try \"90m\")", spelled)
		}
		if gap < 0 {
			return domain.ContextPick{}, fmt.Errorf("complete.model.gap: %q must not be negative", spelled)
		}
		pick.Gap = gap
	}
	return pick, nil
}

// CompletionCount is how many continuations a completion offers, resolved and bounded.
func CompletionCount(cfg config.CompleteModel) int {
	n := orInt(cfg.Options, domain.DefaultCompletionOptions)
	if n > domain.MaxCompletionOptions {
		return domain.MaxCompletionOptions
	}
	return n
}

// NameTargets refuses a name whose target nothing could ever match.
func NameTargets(cfg config.Display) error {
	for _, entry := range cfg.Names {
		target := strings.TrimSpace(entry.Target)
		switch {
		case target == "":
			return fmt.Errorf("display.name: an entry names %q with no target", entry.Name)
		case domain.IsRoomID(target):
		case strings.HasPrefix(target, config.NameTargetRoom),
			strings.HasPrefix(target, config.NameTargetSpace),
			strings.HasPrefix(target, config.NameTargetGroup),
			strings.HasPrefix(target, config.NameTargetThread):
		default:
			return fmt.Errorf(
				"display.name: %q is not a thing that can be named — write room:, space:, "+
					"group: or thread: in front of it, or give a room's !id",
				target)
		}
	}
	return nil
}
