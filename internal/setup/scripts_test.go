package setup_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func withScripts(scripts ...config.Script) config.Config {
	return config.Config{Commands: config.Commands{Scripts: scripts}}
}

// The whole reason this is checked at startup: a need this client does not understand
// is invisible at the moment it matters.
func TestAMistypedNeedIsRefusedWithTheVocabulary(t *testing.T) {
	t.Parallel()

	err := setup.Scripts(withScripts(config.Script{Name: "save", Needs: []string{"ulr"}}))

	if err == nil {
		t.Fatal("a need this client cannot supply was accepted")
	}
	if !strings.Contains(err.Error(), "save") || !strings.Contains(err.Error(), "ulr") {
		t.Errorf("error = %q, want it to name the command and the entry", err)
	}
	// Naming what *is* supported is the point.
	for _, name := range []string{"message", "url", "history:n"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %q, want it to offer %q", err, name)
		}
	}
}

func TestAnUnknownOutputIsRefused(t *testing.T) {
	t.Parallel()

	// Not "pager": that was this test's unknown sink until #92 made it a real one, and
	// the test noticing is the vocabulary check doing its job.
	err := setup.Scripts(withScripts(config.Script{Name: "save", Output: "printer"}))

	if err == nil || !strings.Contains(err.Error(), "printer") {
		t.Fatalf("error = %v, want it to refuse the sink by name", err)
	}
	// And the refusal offers the whole vocabulary, pager included.
	if !strings.Contains(err.Error(), "pager") {
		t.Errorf("error = %q, want it to offer every sink", err)
	}
}

// Two blocks for one command is a question with no answer: which one wins would
// depend on the order they were read in.
func TestOneBlockPerCommand(t *testing.T) {
	t.Parallel()

	err := setup.Scripts(withScripts(
		config.Script{Name: "save", Needs: []string{"url"}},
		config.Script{Name: "/SAVE", Output: "none"},
	))

	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("error = %v, want the duplicate reported", err)
	}
}

func TestABlockNeedsAName(t *testing.T) {
	t.Parallel()

	if err := setup.Scripts(withScripts(config.Script{Needs: []string{"url"}})); err == nil {
		t.Fatal("a nameless block was accepted")
	}
}
