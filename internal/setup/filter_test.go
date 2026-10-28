package setup_test

import (
	"slices"
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
		"no name":             {spamTagged(with(func(f *config.Filter) { f.Name = "" })), "filter[0]"},
		"no tag":              {spamTagged(with(func(f *config.Filter) { f.Tag = "Nope" })), `"Nope"`},
		"says nothing":        {spamTagged(with(func(f *config.Filter) { f.Words = nil })), `"crypto"`},
		"a mistyped language": {spamTagged(with(func(f *config.Filter) { f.NotLang = []string{"en", "russian"} })), `not_lang: "russian"`},
		"an unknown family":   {spamTagged(with(func(f *config.Filter) { f.Lang = []string{"family:asian"} })), `lang: "family:asian"`},
		"an unknown region":   {spamTagged(with(func(f *config.Filter) { f.Lang = []string{"region:asia"} })), `lang: "region:asia"`},
		"an empty language":   {spamTagged(with(func(f *config.Filter) { f.Lang = []string{" "} })), "lang:"},
		"an unknown script":   {spamTagged(with(func(f *config.Filter) { f.Script = []string{"Chinese"} })), `script: "Chinese"`},
		"an unknown move":     {spamTagged(with(func(f *config.Filter) { f.Moves = []string{"sometimes"} })), `"sometimes"`},
		"a share above one":   {spamTagged(with(func(f *config.Filter) { f.Ratio = 1.5 })), "ratio"},
		"a negative floor":    {spamTagged(with(func(f *config.Filter) { f.Floor = -1 })), "floor"},
		"a window that isnt":  {spamTagged(with(func(f *config.Filter) { f.Window = "one week" })), "window"},
		"a spread of one":     {spamTagged(with(func(f *config.Filter) { f.Spread = 1 })), "spread"},
		"a negative spread":   {spamTagged(with(func(f *config.Filter) { f.Spread = -2 })), "spread"},
		"defined twice":       {spamTagged(crypto, with(func(f *config.Filter) { f.Name = "CRYPTO" })), `"CRYPTO"`},
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
		config.Filter{Name: "that number", Tag: "Spam", From: "matrix:@spam:x", Window: "48h", Ratio: 0.3, Floor: 2},
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
	if len(filters.Catching("hello", "matrix:@spam:x", domain.RoomFacts{})) != 1 {
		t.Error("a sender-only filter caught nothing from that sender")
	}
}

// The sender moves read beside the room moves; spread is three rooms unless set.
func TestSenderMovesRead(t *testing.T) {
	t.Parallel()
	filters, err := setup.Filters(spamTagged(
		config.Filter{Name: "runs", Tag: "Spam", Words: []string{"*bitcoin*"}, Moves: []string{"Sender_First", "sender_ratio", "sender_spread", "direct"}},
		config.Filter{Name: "wide", Tag: "Spam", Words: []string{"*bitcoin*"}, Moves: []string{"sender_spread"}, Spread: 5},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.FilterMoves{Direct: true, SenderFirst: true, SenderRatio: true, SenderSpread: true}
	if filters[0].Moves != want || filters[0].Spread != setup.DefaultFilterSpread || filters[1].Spread != 5 {
		t.Errorf("read %+v spread %d and %d, want %+v spread %d and 5", filters[0].Moves, filters[0].Spread, filters[1].Spread, want, setup.DefaultFilterSpread)
	}
	if filters[1].Moves.MovesRooms() || !filters[1].Moves.MarksSenders() {
		t.Errorf("a sender-only filter moves rooms or marks no one: %+v", filters[1].Moves)
	}
}

// Renaming a tag renames the filters and a device's keys naming it; deleting it drops
// them.
func TestFiltersAndKeysFollowTheirTag(t *testing.T) {
	t.Parallel()
	cfg := spamTagged(config.Filter{Name: "crypto", Tag: "spam", Words: []string{"x"}})
	cfg.Tags = append(cfg.Tags, config.Tag{Name: "Other"})
	cfg.Filters = append(cfg.Filters, config.Filter{Name: "other", Tag: "Other", Words: []string{"y"}})
	var dev config.Device
	dev.Keys.Tag = config.TagKeys{{Chord: "!", Tag: "SPAM"}, {Chord: "o", Tag: "Other"}}

	renamed, renamedKeys := setup.RenameTag(cfg, "Spam", "Junk"), setup.RenameTagOnDevice(dev, "Spam", "Junk").Keys
	if renamed.Filters[0].Tag != "Junk" || renamedKeys.Tag[0].Tag != "Junk" || renamed.Filters[1].Tag != "Other" {
		t.Errorf("after rename: filters %+v, keys %+v", renamed.Filters, renamedKeys.Tag)
	}
	deleted, deletedKeys := setup.DeleteTag(cfg, "spam"), setup.DeleteTagOnDevice(dev, "spam").Keys
	if len(deleted.Filters) != 1 || deleted.Filters[0].Name != "other" || len(deletedKeys.Tag) != 1 || deletedKeys.Tag[0].Chord != "o" {
		t.Errorf("after delete: filters %+v, keys %+v", deleted.Filters, deletedKeys.Tag)
	}
	if _, err := setup.Filters(deleted); err != nil {
		t.Errorf("a config with a tag deleted does not validate: %v", err)
	}
}

// A language alone is a filter; its families and regions expand, its scripts read in
// any case.
func TestALanguageAloneIsAFilter(t *testing.T) {
	t.Parallel()
	filters, err := setup.Filters(spamTagged(config.Filter{
		Name: "east asian", Tag: "Spam", Lang: []string{"region:asia-east"}, NotLang: []string{"EN"}, Script: []string{"han", "Kana"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	f := filters[0]
	if !slices.Contains(f.Lang, "ja") || !slices.Equal(f.NotLang, []string{"en"}) || !slices.Equal(f.Scripts, []string{"Han", "Kana"}) {
		t.Errorf("filter = lang %v, not_lang %v, scripts %v", f.Lang, f.NotLang, f.Scripts)
	}
	if !f.Catches("你好", "matrix:@x:y", domain.RoomFacts{ID: mx + "!r:x"}) {
		t.Error("the filter does not catch Chinese")
	}
}
