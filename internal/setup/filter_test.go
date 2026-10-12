package setup_test

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// spamTagged is a config with a Spam tag and filters.
func spamTagged(filters ...config.Filter) config.Config {
	return config.Config{Tags: []config.Tag{{Name: "Spam"}}, Filters: filters}
}

// Filters that cannot work are refused rather than silently skipped.
func TestFiltersRefuseWhatCannotWork(t *testing.T) {
	t.Parallel()
	crypto := config.Filter{Name: "crypto", Tag: "Spam", Words: []string{"*bitcoin*"}}
	with := func(change func(*config.Filter)) config.Filter {
		f := crypto
		change(&f)
		return f
	}
	for name, tc := range map[string]struct {
		cfg  config.Config
		want string
	}{
		"no name":            {spamTagged(with(func(f *config.Filter) { f.Name = "" })), "needs a name"},
		"no tag":             {spamTagged(with(func(f *config.Filter) { f.Tag = "Nope" })), `"Nope" names no [[tag]]`},
		"says nothing":       {spamTagged(with(func(f *config.Filter) { f.Words = nil })), "neither words nor a sender"},
		"an unknown move":    {spamTagged(with(func(f *config.Filter) { f.Moves = []string{"sometimes"} })), `"sometimes"`},
		"a share above one":  {spamTagged(with(func(f *config.Filter) { f.Ratio = 1.5 })), "ratio"},
		"a negative floor":   {spamTagged(with(func(f *config.Filter) { f.Floor = -1 })), "floor"},
		"a window that isnt": {spamTagged(with(func(f *config.Filter) { f.Window = "one week" })), "window"},
		"defined twice":      {spamTagged(crypto, with(func(f *config.Filter) { f.Name = "CRYPTO" })), "defined twice"},
	} {
		if _, err := setup.Filters(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Filters = %v, want it refused mentioning %q", name, err, tc.want)
		}
	}
}

// Defaults: the two cheap moves, three in five over a week with five messages at least;
// a sender alone is a filter.
func TestFilterDefaults(t *testing.T) {
	t.Parallel()
	filters, err := setup.Filters(spamTagged(
		config.Filter{Name: "crypto", Tag: "spam", Words: []string{" *bitcoin* ", ""}},
		config.Filter{Name: "that number", Tag: "Spam", From: "@spam:x", Window: "48h", Ratio: 0.3, Floor: 2},
	))
	if err != nil {
		t.Fatal(err)
	}
	crypto, number := filters[0], filters[1]
	if want := (domain.FilterMoves{FirstMessage: true, Direct: true}); crypto.Moves != want {
		t.Errorf("moves = %+v, want %+v", crypto.Moves, want)
	}
	if crypto.Ratio != setup.DefaultFilterRatio || crypto.Floor != setup.DefaultFilterFloor || crypto.Window != setup.DefaultFilterWindow {
		t.Errorf("ratio numbers = %v/%d/%v, want the defaults", crypto.Ratio, crypto.Floor, crypto.Window)
	}
	if len(crypto.Words.Words) != 1 || crypto.Words.Words[0] != "*bitcoin*" {
		t.Errorf("words = %q, want them trimmed", crypto.Words.Words)
	}
	if number.Ratio != 0.3 || number.Floor != 2 || number.Window != 48*time.Hour {
		t.Errorf("set numbers = %v/%d/%v, want them kept", number.Ratio, number.Floor, number.Window)
	}
	if len(filters.Catching("hello", "@spam:x", domain.RoomFacts{})) != 1 {
		t.Error("a sender-only filter caught nothing from that sender")
	}
}

// Renaming a tag renames the filters and keys naming it; deleting it drops them.
func TestFiltersAndKeysFollowTheirTag(t *testing.T) {
	t.Parallel()
	cfg := spamTagged(config.Filter{Name: "crypto", Tag: "spam", Words: []string{"x"}})
	cfg.Tags = append(cfg.Tags, config.Tag{Name: "Other"})
	cfg.Filters = append(cfg.Filters, config.Filter{Name: "other", Tag: "Other", Words: []string{"y"}})
	cfg.Keys.Tag = config.TagKeys{{Chord: "!", Tag: "SPAM"}, {Chord: "o", Tag: "Other"}}

	renamed := setup.RenameTag(cfg, "Spam", "Junk")
	if renamed.Filters[0].Tag != "Junk" || renamed.Keys.Tag[0].Tag != "Junk" || renamed.Filters[1].Tag != "Other" {
		t.Errorf("after rename: filters %+v, keys %+v", renamed.Filters, renamed.Keys.Tag)
	}
	deleted := setup.DeleteTag(cfg, "spam")
	if len(deleted.Filters) != 1 || deleted.Filters[0].Name != "other" || len(deleted.Keys.Tag) != 1 || deleted.Keys.Tag[0].Chord != "o" {
		t.Errorf("after delete: filters %+v, keys %+v", deleted.Filters, deleted.Keys.Tag)
	}
	if _, err := setup.Filters(deleted); err != nil {
		t.Errorf("a config with a tag deleted does not validate: %v", err)
	}
}
