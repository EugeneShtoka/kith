package matrix

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/spell"
)

// Dictionary detection and install: the languages are counted from the cached corpus
// rather than asked. Lives here (cache, filesystem, network), not in the TUI.

// dictionaryDir is where fetched dictionaries land (searched first by spell.SearchPaths).
func dictionaryDir() string { return filepath.Join(xdg.DataHome, "kith", "hunspell") }

// searchPaths is where dictionaries are looked for.
func searchPaths() []string { return spell.SearchPaths(filepath.Join(xdg.DataHome, "kith")) }

// DetectLanguages counts the cached corpus and returns the dictionaries worth
// installing, already filtered to installable and not yet installed.
func (b *InProc) DetectLanguages(ctx context.Context) (domain.SpellSuggestion, error) {
	avail, _ := spell.Look("", nil, searchPaths())
	if avail.Engine.Path == "" {
		// No engine: the fix is a package manager, so explain and offer nothing.
		return domain.SpellSuggestion{Why: avail.Why(spell.Distro())}, nil
	}
	if b.cache == nil {
		return domain.SpellSuggestion{}, nil
	}

	me := ""
	if b.client != nil {
		me = b.client.UserID.String()
	}
	var counts spell.Counts
	if err := b.cache.EachMessageBody(ctx, me, counts.Add); err != nil {
		return domain.SpellSuggestion{}, fmt.Errorf("matrix: read corpus: %w", err)
	}

	installed := make(map[string]bool, len(avail.Dictionaries))
	for _, d := range avail.Dictionaries {
		installed[d.Tag] = true
	}

	var out domain.SpellSuggestion
	shares := make(map[string]float64, len(avail.Dictionaries))
	for _, c := range counts.Candidates() {
		shares[c.Tag] = c.Share
		src, ok := spell.SourceFor(c.Tag)
		if !ok || installed[c.Tag] {
			continue
		}
		out.Candidates = append(out.Candidates, domain.LanguageCandidate{
			Tag:    c.Tag,
			Script: c.Script.String(),
			Words:  c.Words,
			Share:  c.Share,
			Bytes:  src.Bytes(),
		})
	}
	out.Frequencies = b.frequencyCandidates(avail, shares)
	return out, nil
}

// frequencyCandidates are the word-count lists worth offering for installed
// dictionaries; only when the rare-word check is on.
func (b *InProc) frequencyCandidates(avail spell.Availability, shares map[string]float64) []domain.FrequencyCandidate {
	b.spell.mu.Lock()
	on := b.spell.settings.FlagRare
	dir := b.spell.freqDir()
	b.spell.mu.Unlock()
	if !on {
		return nil
	}

	have := make(map[string]bool)
	for _, tag := range spell.FreqInstalled(dir) {
		have[tag] = true
	}
	var out []domain.FrequencyCandidate
	for _, d := range avail.Dictionaries {
		src, ok := spell.FreqSourceFor(d.Tag)
		if !ok || have[d.Tag] {
			continue
		}
		accepts, needs, _ := spell.Accepts(d.Tag)
		out = append(out, domain.FrequencyCandidate{
			Tag:         d.Tag,
			Script:      spell.TagScript(d.Tag, d.Dir).String(),
			Share:       shares[d.Tag],
			Bytes:       src.Bytes(),
			Disk:        src.Disk(),
			Words:       int64(src.Words),
			Recommended: needs,
			Accepts:     accepts,
		})
	}
	return out
}

// InstallFrequencies fetches one language's word counts and reloads them, so the
// rare-word check works without a restart.
func (b *InProc) InstallFrequencies(ctx context.Context, tag string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	b.spell.mu.Lock()
	dir := b.spell.freqDir()
	b.spell.mu.Unlock()
	if _, err := spell.InstallFreq(ctx, client, tag, dir); err != nil {
		return fmt.Errorf("matrix: install word counts for %s: %w", tag, err)
	}
	b.spell.reloadRarity()
	return nil
}

// InstallDictionary fetches one dictionary, verifying it against the pinned hash.
func (b *InProc) InstallDictionary(ctx context.Context, tag string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	if _, err := spell.Install(ctx, client, tag, dictionaryDir()); err != nil {
		return fmt.Errorf("matrix: install dictionary %s: %w", tag, err)
	}
	return nil
}
