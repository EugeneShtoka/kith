package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/adrg/xdg"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/spell"
)

// Spell checking lives in the daemon because the engine is a subprocess. The text
// crosses rather than words, because the tokenizer (internal/spell) decides what a
// word is.

// SpellSettings is the [spell] configuration, translated by the caller.
type SpellSettings struct {
	// Enabled is `[spell] enabled`; off answers ErrSpellUnavailable.
	Enabled bool
	// Command is the engine to run, empty for hunspell.
	Command string
	// Dictionaries narrows which installed ones to load; empty means all.
	Dictionaries []string
	// FlagRare is `[spell] flag_rare_words`: also mark accepted words when a much commoner
	// word is one slip away (needs the language's frequency list).
	FlagRare bool
	// RareRatio is `[spell] rare_ratio`; zero means defaultRareRatio.
	RareRatio int
}

// defaultRareRatio is the measured default for `rare_ratio`.
const defaultRareRatio = 100

// spellRestarts is how many times a dead engine is restarted before giving up (a
// crash on a bad dictionary repeats; don't fork per keystroke).
const spellRestarts = 3

// spellTimeout bounds one CheckSpelling call; exceeding it kills the child.
const spellTimeout = 2 * time.Second

// spellMemoLimit caps the word→verdict memo; reaching it empties the memo.
const spellMemoLimit = 4096

// spellcheck is the engine as the backend holds it: started on first check, restarted
// on death up to spellRestarts.
type spellcheck struct {
	mu       sync.Mutex
	settings SpellSettings
	checker  *spell.Checker
	// starts counts engines started; gone means stop trying for this process.
	starts int
	gone   bool
	memo   map[string]spell.Verdict
	// rarity is the frequency data by script, loaded when the engine starts.
	rarity map[spell.Script]*spell.Rarity
	// allowed are words whose rare hint was dismissed.
	allowed *spell.WordFile
	// personal mirrors the personal dictionary so a word is not added twice (hunspell
	// appends without deduplicating).
	personal *spell.WordFile
	// dirs is where dictionaries are looked for (zero: the real search paths); dirs[0]
	// also holds the personal dictionary. Tests set their own.
	dirs []string
	// freqs is where frequency lists are looked for; empty means the real one.
	freqs string
	// log is the backend's logger (see UseLogger); nil is silent.
	log *slog.Logger
	// data is kith's data directory ([storage] data_dir); empty is the default one.
	data string
}

// home is kith's data directory, where dictionaries and word counts are installed.
func (s *spellcheck) home() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.homeLocked()
}

// homeLocked is home with mu held.
func (s *spellcheck) homeLocked() string {
	if s.data != "" {
		return s.data
	}
	return filepath.Join(xdg.DataHome, "kith")
}

// logger is s.log or a silent one. Caller holds mu.
func (s *spellcheck) logger() *slog.Logger {
	if s.log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.log
}

// lookIn is where to search for dictionaries.
func (s *spellcheck) lookIn() []string {
	if len(s.dirs) > 0 {
		return s.dirs
	}
	return spell.SearchPaths(s.homeLocked())
}

// personalDic is where "add to dictionary" writes.
func (s *spellcheck) personalDic() string {
	return filepath.Join(s.lookIn()[0], "personal.dic")
}

// freqDir is where frequency lists are read from (zero: kith's data directory).
func (s *spellcheck) freqDir() string {
	if s.freqs != "" {
		return s.freqs
	}
	return spell.FreqDir(s.homeLocked())
}

// UseSpell sets the spelling settings. Wired at startup.
func (s *Service) UseSpell(settings SpellSettings) {
	s.spell.mu.Lock()
	defer s.spell.mu.Unlock()
	s.spell.settings = settings
}

// CheckSpelling returns the words in text the engine does not accept, plus rare-word
// hints when enabled. Verdicts are memoized per word.
func (s *Service) CheckSpelling(ctx context.Context, text string) ([]domain.Misspelling, error) {
	words := spell.Words(text)
	if len(words) == 0 {
		// Nothing checkable (e.g. only a URL): no process needed.
		return nil, nil
	}
	checker, err := s.spell.engine()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, spellTimeout)
	defer cancel()

	out := make([]domain.Misspelling, 0, 4)
	for _, w := range words {
		verdict, err := s.spell.verdict(ctx, checker, w.Text)
		if err != nil {
			return nil, err
		}
		if verdict.OK {
			// Accepted; maybe still a rare-word slip.
			if hint, rare := s.spell.rare(w); rare {
				out = append(out, domain.Misspelling{
					Word: w.Text, Start: w.Start, End: w.End,
					Suggestions: hint.Alternatives, Rare: true, Certain: hint.Certain,
				})
			}
			continue
		}
		out = append(out, domain.Misspelling{
			Word: w.Text, Start: w.Start, End: w.End, Suggestions: verdict.Suggestions,
		})
	}
	return out, nil
}

// rare asks the frequency list about an accepted word. Unmemoized: it is cheap.
func (s *spellcheck) rare(w spell.Word) (spell.Hint, bool) {
	s.mu.Lock()
	r, ok := s.rarity[w.Script]
	allowed := s.allowed
	ratio := s.settings.RareRatio
	s.mu.Unlock()
	if !ok || allowed.Has(w.Text) {
		return spell.Hint{}, false
	}
	if ratio <= 0 {
		ratio = defaultRareRatio
	}
	return r.Rare(w.Text, ratio)
}

// allowList is where dismissed hints are written.
func (s *spellcheck) allowList() string {
	return filepath.Join(s.lookIn()[0], "rare-ok.txt")
}

// AllowRareWord records that a word is not worth hinting about, permanently.
func (s *Service) AllowRareWord(_ context.Context, word string) error {
	s.spell.mu.Lock()
	if s.spell.allowed == nil {
		s.spell.allowed = spell.LoadWordFile(s.spell.allowList())
	}
	allowed := s.spell.allowed
	s.spell.mu.Unlock()
	if err := allowed.Add(word); err != nil {
		return fmt.Errorf("local: allow rare word: %w", err)
	}
	return nil
}

// loadRarity reads the frequency list of every installed dictionary that has one,
// keyed by script. A script claimed by two dictionaries is left out: a German word
// measured against English counts would always look rare.
func loadRarity(log *slog.Logger, avail spell.Availability, dir string) map[spell.Script]*spell.Rarity {
	type found struct {
		tag    string
		aff    spell.Affix
		script spell.Script
	}
	var dicts []found
	claims := make(map[spell.Script]int, len(avail.Dictionaries))
	for _, d := range avail.Dictionaries {
		aff, err := spell.ReadAffix(filepath.Join(d.Dir, d.Tag+".aff"))
		if err != nil {
			// The dictionary loaded for checking, so an unreadable .aff is worth saying.
			log.Warn("rare-word hints: read affix file failed", "dictionary", d.Tag, "err", err)
			continue
		}
		dicts = append(dicts, found{tag: d.Tag, aff: aff, script: aff.Script()})
		claims[aff.Script()]++
	}

	out := make(map[spell.Script]*spell.Rarity, len(dicts))
	for _, d := range dicts {
		if d.script == spell.Other || claims[d.script] > 1 {
			continue
		}
		freq, err := spell.LoadFreq(spell.FreqFile(dir, d.tag))
		if err != nil {
			// Not installed is the usual case (`kith --add-frequencies`); anything
			// else is a broken list.
			level := slog.LevelWarn
			if errors.Is(err, fs.ErrNotExist) {
				level = slog.LevelDebug
			}
			log.Log(context.Background(), level, "rare-word hints: load frequency list failed", "dictionary", d.tag, "err", err)
			continue
		}
		out[d.script] = spell.NewRarity(freq, d.aff)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// reloadRarity re-reads the frequency lists after one is installed. It rescans: a new
// list can change which scripts are unambiguous.
func (s *spellcheck) reloadRarity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.settings.FlagRare {
		return
	}
	avail, _ := spell.Look(s.settings.Command, s.settings.Dictionaries, s.lookIn())
	s.rarity = loadRarity(s.logger(), avail, s.freqDir())
}

// LearnWord teaches the engine a word: forever writes the personal dictionary,
// otherwise it is accepted for this engine's life. The memo forgets the word either
// way, or it would stay underlined.
func (s *Service) LearnWord(_ context.Context, word string, forever bool) error {
	if word == "" {
		return nil
	}
	checker, err := s.spell.engine()
	if err != nil {
		return err
	}
	// A failed write means the engine is gone; retire it so the next check restarts.
	fail := func(err error) error {
		s.spell.retire(checker)
		return fmt.Errorf("%w: %w", api.ErrSpellUnavailable, err)
	}
	if forever && s.spell.taught(word) {
		// Already in the personal dictionary; hunspell would duplicate it.
		s.spell.forget(word)
		return nil
	}
	learn := checker.Ignore
	if forever {
		learn = checker.Add
	}
	if err := learn(word); err != nil {
		return fail(err)
	}
	if forever {
		// Save now: a killed daemon must not lose the word.
		if err := checker.Save(); err != nil {
			return fail(err)
		}
		s.spell.noteTaught(word)
	}
	s.spell.forget(word)
	return nil
}

// taught reports whether the personal dictionary already holds a word.
func (s *spellcheck) taught(word string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.personal.Has(word)
}

// noteTaught records a word hunspell just wrote to the personal dictionary.
func (s *spellcheck) noteTaught(word string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.personal.Remember(word)
}

// forget drops one word from the memo.
func (s *spellcheck) forget(word string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.memo, word)
}

// verdict answers for one word, from the memo when it can.
func (s *spellcheck) verdict(ctx context.Context, c *spell.Checker, word string) (spell.Verdict, error) {
	if v, ok := s.remembered(word); ok {
		return v, nil
	}
	v, err := c.Check(ctx, word)
	if err != nil {
		// No answer means the engine died; retire it so the next call restarts.
		s.retire(c)
		return spell.Verdict{}, fmt.Errorf("%w: %w", api.ErrSpellUnavailable, err)
	}
	s.remember(word, v)
	return v, nil
}

// engine is the running checker, started on first call or after a death.
func (s *spellcheck) engine() (*spell.Checker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checker != nil && s.checker.Live() {
		return s.checker, nil
	}
	if s.gone || !s.settings.Enabled {
		return nil, api.ErrSpellUnavailable
	}
	if s.starts >= spellRestarts {
		s.gone = true
		return nil, fmt.Errorf("%w: the engine stopped %d times", api.ErrSpellUnavailable, s.starts)
	}
	s.starts++

	avail, _ := spell.Look(s.settings.Command, s.settings.Dictionaries, s.lookIn())
	if !avail.Ready() {
		// No engine or no dictionary: won't change while this process runs.
		s.gone = true
		return nil, fmt.Errorf("%w: %s", api.ErrSpellUnavailable, avail.Why(spell.Distro()))
	}
	// Create the personal dictionary's directory now: hunspell opens it at startup.
	personal := s.personalDic()
	if err := os.MkdirAll(filepath.Dir(personal), 0o700); err != nil {
		// Still checkable; only the "add to dictionary" half is lost, so say why.
		s.logger().Warn("personal dictionary unavailable", "err", err)
		personal = ""
	}
	s.personal = spell.LoadWordFile(personal)
	// Background: the child belongs to the process and is closed by Stop.
	checker, err := spell.Start(context.Background(), avail, personal)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", api.ErrSpellUnavailable, err)
	}
	s.checker = checker
	if s.settings.FlagRare {
		// After the engine: the same scan says which dictionaries are loaded.
		s.rarity = loadRarity(s.logger(), avail, s.freqDir())
		if s.allowed == nil {
			s.allowed = spell.LoadWordFile(s.allowList())
		}
	}
	return checker, nil
}

// retire drops a checker that has died, unless a later one has already replaced it.
func (s *spellcheck) retire(dead *spell.Checker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checker == dead {
		// The engine already died; Close only reaps it, and why it died is the
		// error the caller is returning.
		_ = s.checker.Close() // ignored: see above
		s.checker = nil
	}
}

// remembered is the memo lookup.
func (s *spellcheck) remembered(word string) (spell.Verdict, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.memo[word]
	return v, ok
}

// remember writes the memo.
func (s *spellcheck) remember(word string, v spell.Verdict) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memo == nil || len(s.memo) >= spellMemoLimit {
		s.memo = make(map[string]spell.Verdict, spellMemoLimit/4)
	}
	s.memo[word] = v
}

// stop ends the engine. Idempotent, because Stop is.
func (s *spellcheck) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checker != nil {
		if err := s.checker.Close(); err != nil {
			s.logger().Debug("stop spell checker failed", "err", err)
		}
		s.checker = nil
	}
	s.gone = true
}
