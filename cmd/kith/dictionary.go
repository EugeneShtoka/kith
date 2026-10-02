package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/EugeneShtoka/kith/internal/spell"
)

// addDictionary fetches one dictionary into kith's data directory (spell.SearchPaths
// looks in its hunspell/ first) and reports what it did.
func addDictionary(data, tag string) error {
	dir := filepath.Join(data, "hunspell")
	if tag == "list" || tag == "?" {
		listDictionaries(data, dir)
		return nil
	}
	src, ok := spell.SourceFor(tag)
	if !ok {
		return fmt.Errorf("no dictionary %q — try one of: %s\n"+
			"(or `kith --add-dictionary list` to see them with their sizes)",
			tag, strings.Join(spell.Installable(), ", "))
	}

	repo, commit, date := spell.Pin()
	fmt.Fprintf(os.Stderr,
		"kith: fetching %s (%.1f MB) from %s\n"+
			"        pinned at %s (%s), and every file is checked against the SHA-256 it\n"+
			"        had then — a mismatch is refused rather than installed.\n"+
			"        Into: %s\n",
		tag, float64(src.Bytes())/(1<<20), repo, commit[:12], date, dir)

	installed, err := spell.Install(context.Background(), spell.DefaultClient(), tag, dir)
	switch {
	case errors.Is(err, spell.ErrCorrupt):
		return fmt.Errorf("refused: %w", err)
	case err != nil:
		return fmt.Errorf("install %s: %w", tag, err)
	}
	fmt.Fprintf(os.Stderr, "kith: installed %s — %d files in %s\n",
		installed.Tag, filesWritten(installed), dir)
	fmt.Fprintln(os.Stderr, "        Nothing else to do: kith loads every dictionary it finds.")
	return nil
}

// filesWritten counts an install's files: .aff, .dic and an optional license.
func filesWritten(src spell.Source) int {
	if src.License.Path != "" {
		return 3
	}
	return 2
}

// listDictionaries shows what can be installed and what already is.
func listDictionaries(data, dir string) {
	avail, _ := spell.Look("", nil, spell.SearchPaths(data))
	have := make(map[string]bool, len(avail.Dictionaries))
	for _, d := range avail.Dictionaries {
		have[d.Tag] = true
	}

	repo, commit, date := spell.Pin()
	fmt.Printf("Dictionaries kith can install, from %s pinned at %s (%s):\n\n", repo, commit[:12], date)
	for _, tag := range spell.Installable() {
		src, _ := spell.SourceFor(tag)
		mark := " "
		if have[tag] {
			mark = "✓"
		}
		fmt.Printf("  %s %-8s %5.1f MB\n", mark, tag, float64(src.Bytes())/(1<<20))
	}
	fmt.Printf("\n✓ = already installed. Install one with:  kith --add-dictionary he_IL\n")
	fmt.Printf("Installed dictionaries live in %s\n", dir)

	var elsewhere []string
	for _, d := range avail.Dictionaries {
		if !strings.HasPrefix(d.Dir, dir) {
			elsewhere = append(elsewhere, d.Tag+" ("+d.Dir+")")
		}
	}
	if len(elsewhere) > 0 {
		fmt.Printf("\nAlready on this system, and used as-is:\n  %s\n", strings.Join(elsewhere, "\n  "))
	}
}
