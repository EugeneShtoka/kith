package setup

import (
	"fmt"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Scripts refuses a `[[commands.script]]` block this client cannot honor.
func Scripts(cfg config.Config) error {
	seen := map[string]bool{}
	for i, script := range cfg.Commands.Scripts {
		name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(script.Name), "/"))
		if name == "" {
			return fmt.Errorf("commands.script[%d]: needs a name — the command, without its slash", i)
		}
		if seen[name] {
			return fmt.Errorf("commands.script: %q is configured twice", name)
		}
		seen[name] = true
		if err := scriptNeeds(name, script.Needs); err != nil {
			return err
		}
		if _, ok := domain.ParseScriptOutput(script.Output); !ok {
			return fmt.Errorf("commands.script %q: output %q is not one of %s",
				name, script.Output, strings.Join(domain.OutputVocabulary(), ", "))
		}
	}
	return nil
}

// scriptNeeds refuses a need that is not in the vocabulary, naming the whole of it.
func scriptNeeds(name string, needs []string) error {
	for _, entry := range needs {
		if _, ok := domain.ParseNeed(entry); !ok {
			return fmt.Errorf("commands.script %q: %q is not something this client can give a script — try one of %s (n is 1 to %d)",
				name, entry, strings.Join(domain.NeedVocabulary(), ", "), domain.MaxHistoryNeed)
		}
	}
	return nil
}
