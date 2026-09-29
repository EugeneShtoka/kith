package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"

	"github.com/EugeneShtoka/kith/internal/spell"
)

// frequencyDir holds installed frequency lists, outside the hunspell directory.
func frequencyDir() string {
	return spell.FreqDir(filepath.Join(xdg.DataHome, "kith"))
}

// addFrequencies fetches one list and reports what it did.
func addFrequencies(tag string) error {
	dir := frequencyDir()
	if tag == "list" || tag == "?" {
		listFrequencies(dir)
		return nil
	}
	src, ok := spell.FreqSourceFor(tag)
	if !ok {
		return fmt.Errorf("no frequency list for %q — try one of: %s\n"+
			"(or `kith --add-frequencies list` to see them with their sizes)",
			tag, strings.Join(spell.FreqInstallable(), ", "))
	}

	repo, commit, date := spell.FreqPin()
	fmt.Fprintf(os.Stderr,
		"kith: fetching word counts for %s (%.1f MB) from %s\n"+
			"        pinned at %s (%s), checked against the SHA-256 it had then — a\n"+
			"        mismatch is refused rather than installed.\n"+
			"        Kept: %d words, %.1f MB, in %s\n",
		tag, float64(src.Bytes())/(1<<20), repo, commit[:12], date,
		src.Words, float64(src.Disk())/(1<<20), dir)

	installed, err := spell.InstallFreq(context.Background(), spell.DefaultClient(), tag, dir)
	switch {
	case errors.Is(err, spell.ErrCorrupt):
		return fmt.Errorf("refused: %w", err)
	case err != nil:
		return fmt.Errorf("install %s word counts: %w", tag, err)
	}
	fmt.Fprintf(os.Stderr, "kith: installed word counts for %s — %d words in %s\n",
		installed.Tag, installed.Words, dir)
	fmt.Fprintln(os.Stderr, "        Turn the check on with `flag_rare_words = true` under [spell].")
	return nil
}

// listFrequencies shows what can be installed and what already is.
func listFrequencies(dir string) {
	have := make(map[string]bool)
	for _, tag := range spell.FreqInstalled(dir) {
		have[tag] = true
	}

	repo, commit, date := spell.FreqPin()
	fmt.Printf("Word-frequency lists kith can install, from %s pinned at %s (%s):\n\n",
		repo, commit[:12], date)
	fmt.Printf("    %-8s %9s  %9s  %s\n", "tag", "download", "on disk", "words kept")
	for _, tag := range spell.FreqInstallable() {
		src, _ := spell.FreqSourceFor(tag)
		mark := " "
		if have[tag] {
			mark = "✓"
		}
		fmt.Printf("  %s %-8s %6.1f MB  %6.1f MB  %9d\n",
			mark, tag, float64(src.Bytes())/(1<<20), float64(src.Disk())/(1<<20), src.Words)
	}
	fmt.Printf("\n✓ = already installed. Install one with:  kith --add-frequencies he_IL\n")
	fmt.Printf("They are counts over a real corpus, for the languages whose dictionaries accept\n")
	fmt.Printf("too much to catch a typo — `[spell] flag_rare_words` reads them.\n")
}
