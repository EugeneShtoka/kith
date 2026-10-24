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

// ModelDir is where installed weights live, beside the dictionaries in kith's own
// data directory.
func ModelDir(dataDir string) string { return filepath.Join(dataDir, "models") }

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

// ModelDurations refuses misspelled durations, which the OrDefault helpers would
// silently replace with defaults.
func ModelDurations(cfg config.CompleteModel) error {
	for _, field := range []struct {
		key     string
		spelled string
		// zeroOK: an idle window of "0s" means never stop the server.
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
	// Checked before orInt, which treats negatives as unset; `recent = -1` is a mistake.
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
		case domain.BareMatrixID(target):
			return fmt.Errorf("setup: %w", domain.BareMatrixError("display.name", target))
		case domain.IsRoomID(target):
		case strings.HasPrefix(target, config.NameTargetRoom),
			strings.HasPrefix(target, config.NameTargetSpace),
			strings.HasPrefix(target, config.NameTargetThread):
		default:
			return fmt.Errorf(
				"display.name: %q is not a thing that can be named — write room:, space: or "+
					"thread: in front of it, or give a room's ID (a tag is named in its [[tag]])",
				target)
		}
	}
	return nil
}
