package modelsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/llamacpp"
	"github.com/EugeneShtoka/kith/internal/llamacpp/models"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The assist tasks are the three a small local model cannot do.
func TestModelTasksAreTheOnesAModelIsAskedFor(t *testing.T) {
	t.Parallel()

	tasks := ModelTasks(config.Assist{})
	if _, ok := tasks[domain.ModelComplete]; ok {
		t.Error("a completion task survives, so a prompt exists for a request nothing makes")
	}
	for _, name := range []string{domain.ModelSummary, domain.ModelTodo, domain.ModelRewrite} {
		if _, ok := tasks[name]; !ok {
			t.Errorf("no %s task, so 'no prompt configured' could mean two different things", name)
		}
	}
	// The summary's prompt is the one with something to get wrong: it is written *to*
	// the person asking, which is what {me} and {names} are for.
	summary := tasks[domain.ModelSummary]
	for _, want := range []string{"{me}", "{names}"} {
		if !strings.Contains(summary.System, want) {
			t.Errorf("default summary system prompt = %q, want %q in it", summary.System, want)
		}
	}
	if !strings.Contains(summary.Prompt, "{context}") {
		t.Errorf("default summary prompt = %q, want the conversation in it", summary.Prompt)
	}

	// And what the config says wins, which is the whole point of the prompts living there.
	mine := ModelTasks(config.Assist{
		Rewrite: config.ModelPrompt{System: "be brief", Prompt: "{draft}", MaxTokens: 9, Temperature: 0.3},
	})
	got := mine[domain.ModelRewrite]
	if got.System != "be brief" || got.Prompt != "{draft}" || got.MaxTokens != 9 || got.Temperature != 0.3 {
		t.Errorf("configured task = %+v, want the config's own wording and bounds", got)
	}
}

// weights writes a file of exactly the size the manifest records, which is what
// Installed checks: a GGUF of the right name and the wrong length is a server that
// starts, fails to parse it, and never becomes healthy.
func weights(t *testing.T, dataHome, tag string) string {
	t.Helper()
	src, ok := models.SourceFor(tag)
	if !ok {
		t.Fatalf("no source for %s", tag)
	}
	dir := setup.ModelDir(dataHome)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, src.Filename())
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Truncate(src.File.Size); err != nil {
		t.Fatal(err)
	}
	return path
}

// An empty `model` means "whichever is installed", which is what makes `--add-model`
// the whole of the setup: a download that then needed a config edit to take effect
// would be a second step nobody was told about.
func TestTheModelIsWhicheverIsInstalled(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	want := weights(t, home, models.Installable()[0])

	got, on := CompletionModel(config.CompleteModel{}, home)
	if !on {
		t.Fatal("the layer is off for an untouched config")
	}
	if got.Model != want {
		t.Errorf("model = %q, want %q", got.Model, want)
	}
}

// Somebody who built their own GGUF should not have to move it into this client's
// directory to use it — so a path that points at a real file is taken as written.
func TestAPathIsTakenAsWritten(t *testing.T) {
	t.Parallel()

	mine := filepath.Join(t.TempDir(), "qwen2.5-1.5b-instruct-q4_k_m.gguf")
	if err := os.WriteFile(mine, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, on := CompletionModel(config.CompleteModel{Model: mine}, t.TempDir())
	if !on || got.Model != mine {
		t.Errorf("model = %q (on=%v), want the path as written", got.Model, on)
	}
}

func TestTheSettingsCarryTheProcessAndItsDurations(t *testing.T) {
	t.Parallel()

	got, on := CompletionModel(config.CompleteModel{
		Command: "/opt/llama.cpp/llama-server",
		Threads: 6, Context: 2048, Port: 8099,
		Idle: "2m", Startup: "5s", Timeout: "300ms",
	}, t.TempDir())
	if !on {
		t.Fatal("the layer is off")
	}
	want := llamacpp.Settings{
		Command: "/opt/llama.cpp/llama-server",
		Threads: 6, Context: 2048, Port: 8099,
		Idle: 2 * time.Minute, Startup: 5 * time.Second, Timeout: 300 * time.Millisecond,
	}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

// With nothing usable installed the layer is still on (the offer notices that) but
// resolves no model; enabled = false turns it off.
func TestCompletionModelWithNothingInstalled(t *testing.T) {
	t.Parallel()

	for _, named := range []string{"", models.Installable()[0], "openai/gpt-oss-20b", "/no/such/model.gguf", t.TempDir()} {
		got, on := CompletionModel(config.CompleteModel{Model: named}, t.TempDir())
		if !on || got.Model != "" {
			t.Errorf("%q: model = %q, on = %v; want on with nothing found", named, got.Model, on)
		}
	}
	off := false
	if _, on := CompletionModel(config.CompleteModel{Enabled: &off}, t.TempDir()); on {
		t.Error("the layer is on with enabled = false")
	}
}
