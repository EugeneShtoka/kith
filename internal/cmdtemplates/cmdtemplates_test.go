package cmdtemplates_test

import (
	"bytes"
	"regexp"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/cmdtemplates"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Every template is a block the config accepts and a script that runs on its own, reading
// exactly the secrets its block declares.
func TestEveryTemplateIsWhole(t *testing.T) {
	t.Parallel()

	all, err := cmdtemplates.All()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tmpl := range all {
		names = append(names, tmpl.Name)
		if tmpl.About == "" || tmpl.Block.Name == "" {
			t.Errorf("%s: about %q, name %q, want both", tmpl.Name, tmpl.About, tmpl.Block.Name)
		}
		if err := setup.Scripts(config.Config{UserCommands: []config.Command{tmpl.Block}}); err != nil {
			t.Errorf("%s: the block is refused: %v", tmpl.Name, err)
		}
		if !bytes.HasPrefix(tmpl.Script, []byte("#!/bin/sh")) {
			t.Errorf("%s: the script has no #!/bin/sh line", tmpl.Name)
		}
		read := map[string]bool{}
		for _, m := range regexp.MustCompile(`KITH_SECRET_[A-Z0-9_]+`).FindAll(tmpl.Script, -1) {
			read[string(m)] = true
		}
		for _, secret := range tmpl.Block.Secrets {
			if !read[config.SecretEnv(secret)] {
				t.Errorf("%s: declares %s but never reads $%s", tmpl.Name, secret, config.SecretEnv(secret))
			}
			delete(read, config.SecretEnv(secret))
		}
		for env := range read {
			t.Errorf("%s: reads $%s, which its block does not declare", tmpl.Name, env)
		}
	}
	for _, want := range []string{"jitsi", "zoom", "meet", "standup", "who"} {
		if !slices.Contains(names, want) {
			t.Errorf("templates = %v, want %s among them", names, want)
		}
	}
}
