package local

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/llamacpp"
	"github.com/EugeneShtoka/kith/internal/llamacpp/models"
)

// The local completion model answers ModelTask("complete"). Nothing leaves the
// machine, so room scope and the per-minute cap do not apply; a short timeout does.
// Off until weights are installed; spawned on first ask.

// completionModel is the running engine, if any.
type completionModel struct {
	mu sync.Mutex
	// predictor is nil when nothing is installed or the layer is off.
	predictor *llamacpp.Predictor
	// model is the weights' short name, for the marker.
	model string
	// settings built the predictor, so an install can swap the path.
	settings llamacpp.Settings
	// weights stamps the model file, so one replaced in place counts as new.
	weights weightsStamp
}

// weightsStamp tells one weights file from another at the same path.
type weightsStamp struct {
	size    int64
	modTime time.Time
}

// weightsOf stamps the file at path; the zero stamp when unreadable.
func weightsOf(path string) weightsStamp {
	info, err := os.Stat(path)
	if err != nil {
		return weightsStamp{}
	}
	return weightsStamp{size: info.Size(), modTime: info.ModTime()}
}

func (s *Service) modelDir() string { return filepath.Join(s.spell.home(), "models") }

// UseCompletionModel wires or disables the engine (startup and reload). A reload with
// the same settings and weights file keeps the running predictor, so an in-flight
// completion survives; otherwise the old server is stopped.
func (s *Service) UseCompletionModel(settings llamacpp.Settings, on bool) {
	weights := weightsOf(settings.Model)
	var next *llamacpp.Predictor
	name := ""
	if on {
		next = llamacpp.NewPredictor(settings)
		name = shortModelName(settings.Model)
	}
	s.completion.mu.Lock()
	previous := s.completion.predictor
	if on && previous != nil && settings == s.completion.settings && weights == s.completion.weights {
		s.completion.mu.Unlock()
		return
	}
	s.completion.predictor, s.completion.model, s.completion.settings = next, name, settings
	s.completion.weights = weights
	s.completion.mu.Unlock()
	if previous != nil {
		s.warnIf(context.Background(), previous.Close(), "stop the previous completion model")
	}
}

func (s *Service) closeCompletionModel() {
	s.completion.mu.Lock()
	predictor := s.completion.predictor
	s.completion.predictor = nil
	s.completion.mu.Unlock()
	if predictor != nil {
		s.warnIf(context.Background(), predictor.Close(), "stop the completion model")
	}
}

// modelReady is the engine to ask and its reported name, or false.
func (s *Service) modelReady() (*llamacpp.Predictor, string, bool) {
	s.completion.mu.Lock()
	predictor, name := s.completion.predictor, s.completion.model
	s.completion.mu.Unlock()
	if predictor == nil || !predictor.Available() {
		return nil, "", false
	}
	return predictor, name, true
}

// completeFromModel answers a completion, or refuses with a sentence naming the fix.
func (s *Service) completeFromModel(ctx context.Context, req domain.ModelRequest, settings ModelSettings) domain.ModelResult {
	result := domain.ModelResult{Endpoint: thisMachine}
	predictor, name, ok := s.modelReady()
	if !ok {
		result.Refusal = s.whyNoModel()
		return result
	}
	result.Model = name
	if strings.TrimSpace(req.Draft) == "" && !req.DryRun {
		result.Refusal = "nothing written to work from"
		return result
	}

	prompt := s.completionPrompt(ctx, req, settings)
	if req.DryRun {
		// The exact prompt, for `why`.
		result.Text = "To the model on this machine (" + name + "), over no network:\n\n" + prompt
		return result
	}

	options, err := predictor.NextWords(ctx, prompt, settings.Options)
	if err != nil {
		// A refusal, not an error: weights without the server program is an ordinary state.
		result.Refusal = err.Error()
		return result
	}
	result.Options = options
	if len(options) > 0 {
		result.Text = options[0]
	}
	return result
}

// whyNoModel says which half is missing: the model (downloadable) or the server.
func (s *Service) whyNoModel() string {
	s.completion.mu.Lock()
	predictor, settings := s.completion.predictor, s.completion.settings
	s.completion.mu.Unlock()
	switch {
	case predictor == nil:
		return "the completion model is switched off — see [complete.model] enabled"
	case settings.Model == "":
		return "no completion model installed — run `kith --add-model smollm2-360m`"
	default:
		return missingServer(settings.Command)
	}
}

// thisMachine replaces an endpoint URL (the port varies per spawn).
const thisMachine = "this machine"

// completionPrompt is the raw text the model continues: quoted context, then the
// draft. No chat template.
func (s *Service) completionPrompt(ctx context.Context, req domain.ModelRequest, settings ModelSettings) string {
	fields, _, err := s.modelFields(ctx, req, settings)
	if err != nil {
		return req.Draft
	}
	context := strings.TrimSpace(fields["{context}"])
	if context == "" {
		return req.Draft
	}
	return context + "\n" + req.Draft
}

func shortModelName(path string) string {
	if path == "" {
		return ""
	}
	if cut := strings.LastIndexByte(path, '/'); cut >= 0 {
		path = path[cut+1:]
	}
	return strings.TrimSuffix(path, ".gguf")
}

// InstallModel fetches the weights (unless present) and points the running layer at
// them, with no restart.
func (s *Service) InstallModel(ctx context.Context, tag string) error {
	dir := s.modelDir()
	path, ok := models.Installed(dir, tag)
	if !ok {
		if _, err := llamacpp.Install(ctx, llamacpp.DefaultClient(), tag, dir); err != nil {
			return fmt.Errorf("local: install model %s: %w", tag, err)
		}
		if path, ok = models.Installed(dir, tag); !ok {
			return fmt.Errorf("local: install model %s: it is not where it was written", tag)
		}
	}
	s.completion.mu.Lock()
	settings := s.completion.settings
	s.completion.mu.Unlock()
	settings.Model = path
	s.UseCompletionModel(settings, true)
	return nil
}

// DetectModel answers whether to offer the completion model: nothing when installed
// or off, a sentence when llama.cpp is missing, else a candidate. The daemon answers
// because it spawns llama-server, so its PATH is what matters.
func (s *Service) DetectModel(_ context.Context) (domain.ModelSuggestion, error) {
	s.completion.mu.Lock()
	predictor, settings := s.completion.predictor, s.completion.settings
	s.completion.mu.Unlock()
	if predictor == nil {
		return domain.ModelSuggestion{}, nil
	}
	if settings.Model != "" {
		return domain.ModelSuggestion{}, nil
	}
	if !onPath(settings.Command) {
		return domain.ModelSuggestion{Why: missingServer(settings.Command)}, nil
	}

	installable := models.Installable()
	if len(installable) == 0 {
		return domain.ModelSuggestion{}, nil
	}
	src, ok := models.SourceFor(installable[0])
	if !ok {
		return domain.ModelSuggestion{}, nil
	}
	return domain.ModelSuggestion{
		Offer: true,
		Candidate: domain.ModelCandidate{
			Tag: src.Tag, Name: src.Name, Bytes: src.Bytes(), Recall: src.Recall,
		},
	}, nil
}

func onPath(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func missingServer(command string) string {
	return command + " is not installed, and the completion model needs it — " +
		"Arch: pacman -S llama.cpp · Homebrew: brew install llama.cpp · " +
		"or build github.com/ggml-org/llama.cpp and name it in [complete.model] command"
}
