package setup

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Scripts refuses a `[[command]]` block this client cannot honor.
func Scripts(dev config.Device) error {
	seen := map[string]bool{}
	for i, script := range dev.UserCommands {
		name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(script.Name), "/"))
		if name == "" {
			return fmt.Errorf("command[%d]: needs a name — the command, without its slash", i)
		}
		if seen[name] {
			return fmt.Errorf("command: %q is configured twice", name)
		}
		seen[name] = true
		if err := scriptNeeds(name, script.Needs); err != nil {
			return err
		}
		if err := scriptSecrets(name, script.Secrets); err != nil {
			return err
		}
		if _, ok := domain.ParseScriptOutput(script.Output); !ok {
			return fmt.Errorf("command %q: output %q is not one of %s",
				name, script.Output, strings.Join(domain.OutputVocabulary(), ", "))
		}
	}
	return nil
}

// secretName is what a secret may be called: it becomes part of a variable's name.
var secretName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// scriptSecrets refuses a secret name that cannot be a variable's, or one given twice.
func scriptSecrets(name string, secrets []string) error {
	for i, secret := range secrets {
		if !secretName.MatchString(secret) {
			return fmt.Errorf("command %q: secret %q needs a name of lower-case letters, digits and _ (it is read as $%s)",
				name, secret, config.SecretEnv(secret))
		}
		if slices.Contains(secrets[:i], secret) {
			return fmt.Errorf("command %q: secret %q is listed twice", name, secret)
		}
	}
	return nil
}

// scriptNeeds refuses a need that is not in the vocabulary, naming the whole of it.
func scriptNeeds(name string, needs []string) error {
	for _, entry := range needs {
		if _, ok := domain.ParseNeed(entry); !ok {
			return fmt.Errorf("command %q: %q is not something this client can give a script — try one of %s (n is 1 to %d)",
				name, entry, strings.Join(domain.NeedVocabulary(), ", "), domain.MaxHistoryNeed)
		}
	}
	return nil
}
