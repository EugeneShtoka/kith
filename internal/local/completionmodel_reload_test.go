package local

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/llamacpp"
)

// A config reload that leaves the completion model as it was keeps the running
// predictor (a completion in flight is not cut off); changed settings, weights
// replaced at the same path, or turning it off, replace it.
func TestAReloadKeepsAnUnchangedCompletionModel(t *testing.T) {
	t.Parallel()
	weights := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(weights, []byte("weights"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := llamacpp.Settings{Command: "llama-server", Model: weights, Context: 512}

	for _, tc := range []struct {
		name   string
		reload func(t *testing.T) (llamacpp.Settings, bool)
		kept   bool
	}{
		{"the same settings", func(*testing.T) (llamacpp.Settings, bool) { return settings, true }, true},
		{"another context size", func(*testing.T) (llamacpp.Settings, bool) {
			s := settings
			s.Context = 1024
			return s, true
		}, false},
		{"weights replaced at the same path", func(t *testing.T) (llamacpp.Settings, bool) {
			later := time.Now().Add(time.Hour)
			if err := os.WriteFile(weights, []byte("other weights"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(weights, later, later); err != nil {
				t.Fatal(err)
			}
			return settings, true
		}, false},
		{"turned off", func(*testing.T) (llamacpp.Settings, bool) { return settings, false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := New(nil, nil)
			b.UseCompletionModel(settings, true)
			before := b.completion.predictor
			b.UseCompletionModel(tc.reload(t))
			if kept := b.completion.predictor == before; kept != tc.kept {
				t.Errorf("predictor kept = %v, want %v", kept, tc.kept)
			}
		})
	}
}
