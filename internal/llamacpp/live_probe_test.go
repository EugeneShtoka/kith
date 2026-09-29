package llamacpp_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/llamacpp"
)

// TestLiveModel is an opt-in probe against a real llama-server and weights:
//
//	KITH_MODEL=~/.local/share/kith/models/smollm2-360m.gguf go test ./internal/llamacpp/ -run TestLiveModel -v
func TestLiveModel(t *testing.T) {
	model := os.Getenv("KITH_MODEL")
	if model == "" {
		t.Skip("KITH_MODEL not set")
	}
	p := llamacpp.NewPredictor(llamacpp.Settings{
		Command: "llama-server",
		Model:   model,
		Threads: 4,
		Context: 1024,
		Timeout: 5 * time.Second,
	})
	defer func() { _ = p.Close() }()
	if err := p.Why(); err != nil {
		t.Fatal(err)
	}

	// Drafts ending in a space, which is what the composer hands over at a word
	// boundary: the trailing space is the caller's, and stripping it is this package's
	// job rather than something every caller has to remember.
	for _, draft := range []string{
		"I think we should check the ",
		"let us meet ",
		"the deploy is ",
		"sorry, I was ",
		"have merged, will follow ",
	} {
		start := time.Now()
		got, err := p.NextWords(context.Background(), draft, 4)
		if err != nil {
			t.Fatalf("%q: %v", draft, err)
		}
		if len(got) == 0 {
			t.Errorf("%q: nothing offered", draft)
		}
		for _, word := range got {
			if strings.ContainsAny(word, " \t\n") {
				t.Errorf("%q: %q is not one word", draft, word)
			}
		}
		t.Logf("%8s  %-32q -> %v", time.Since(start).Round(time.Millisecond), draft, got)
	}
}
