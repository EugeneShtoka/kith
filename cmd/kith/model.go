package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"

	"github.com/EugeneShtoka/kith/internal/llamacpp"
	"github.com/EugeneShtoka/kith/internal/llamacpp/models"
)

// modelDir holds installed weights; internal/setup points the daemon at the same place.
func modelDir() string { return filepath.Join(xdg.DataHome, "kith", "models") }

// addModel fetches one model and reports what it did.
func addModel(tag string) error {
	dir := modelDir()
	if tag == "list" || tag == "?" {
		listModels(dir)
		return nil
	}
	src, ok := models.SourceFor(tag)
	if !ok {
		return fmt.Errorf("no model %q — try one of: %s\n"+
			"(or `kith --add-model list` to see them with their sizes)",
			tag, strings.Join(models.Installable(), ", "))
	}
	if path, have := models.Installed(dir, tag); have {
		fmt.Fprintf(os.Stderr, "kith: %s is already installed at %s\n", tag, path)
		return nil
	}

	repo, commit, date := models.Pin()
	fmt.Fprintf(os.Stderr,
		"kith: fetching %s — %s (%.0f MB) from %s\n"+
			"        pinned at %s (%s), and checked against the SHA-256 it had then — a\n"+
			"        mismatch is refused rather than installed.\n"+
			"        Into: %s\n",
		tag, src.Name, float64(src.Bytes())/(1<<20), repo, commit[:12], date, dir)

	if _, err := llamacpp.Install(context.Background(), llamacpp.DefaultClient(), tag, dir); err != nil {
		if errors.Is(err, llamacpp.ErrCorrupt) {
			return fmt.Errorf("refused: %w", err)
		}
		return fmt.Errorf("install %s: %w", tag, err)
	}
	fmt.Fprintf(os.Stderr, "kith: installed %s in %s\n", tag, dir)
	fmt.Fprintln(os.Stderr, "        Nothing else to do: the daemon finds it on the next completion.")
	sayIfNoServer()
	return nil
}

// listModels shows what can be installed and what already is.
func listModels(dir string) {
	repo, commit, date := models.Pin()
	fmt.Printf("Models kith can install, from %s pinned at %s (%s):\n\n", repo, commit[:12], date)
	for _, tag := range models.Installable() {
		src, _ := models.SourceFor(tag)
		mark := " "
		if _, have := models.Installed(dir, tag); have {
			mark = "✓"
		}
		fmt.Printf("  %s %-14s %5.0f MB  %s\n", mark, tag, float64(src.Bytes())/(1<<20), src.Name)
		fmt.Printf("    %s · finishes the word you were typing %.0f%% of the time, in ~20ms, on the CPU\n",
			src.License, 100*src.Recall)
	}
	fmt.Printf("\n✓ = already installed. Install one with:  kith --add-model %s\n", models.Installable()[0])
	fmt.Printf("Installed models live in %s\n", dir)
	sayIfNoServer()
}

// sayIfNoServer warns when llama-server, which the weights need, is not on PATH.
func sayIfNoServer() {
	if _, err := exec.LookPath("llama-server"); err == nil {
		return
	}
	fmt.Fprintln(os.Stderr,
		"\nkith: llama-server is not on PATH, and the weights need it to run.\n"+
			"        Arch: pacman -S llama.cpp · Homebrew: brew install llama.cpp\n"+
			"        Debian/Ubuntu: build from github.com/ggml-org/llama.cpp\n"+
			"        A build of your own works too — name it in [complete.model] command.")
}
