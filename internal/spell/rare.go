package spell

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Rare-word hints: flag a word the engine accepted when a far commoner word is one
// slip away (e.g. Hebrew `נוכן` for `נכון`). A comparison, never a floor, so rare but
// correct words stay unmarked. A hint is not a verdict and offers a short list, since
// the intended word can be rarer than the typed one. Data comes from the frequency list
// and the dictionary's own .aff (TRY, MAP).

// rareNeighbourFloor is the minimum count for a neighbor to matter; low enough to keep
// `שולם` (563) as a hint for `שלום`.
const rareNeighbourFloor = 500

// rareAlternatives is how many words a hint offers.
const rareAlternatives = 3

// Affix is the part of a .aff file this package reads.
type Affix struct {
	// Try is hunspell's TRY alphabet, what a missing keystroke is put back from.
	Try []rune
	// Map is hunspell's MAP sets of confusable letters; members may be multi-letter
	// (`MAP ß(ss)`).
	Map [][]string
}

// ReadAffix reads TRY and MAP out of a dictionary's affix file.
func ReadAffix(path string) (Affix, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- an installed dictionary's affix file
	if err != nil {
		return Affix{}, fmt.Errorf("spell: read %s: %w", path, err)
	}
	return parseAffix(string(data)), nil
}

func parseAffix(text string) Affix {
	var aff Affix
	for line := range strings.SplitSeq(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		// Trailing comments do occur on these lines: `MAP אע # for English`.
		if i := strings.IndexByte(value, '#'); i >= 0 {
			value = value[:i]
		}
		value = strings.TrimSpace(value)
		switch key {
		case "TRY":
			aff.Try = []rune(value)
		case "MAP":
			// The first MAP line is a count.
			if _, err := strconv.Atoi(value); err == nil {
				continue
			}
			if set := mapSet(value); len(set) > 1 {
				aff.Map = append(aff.Map, set)
			}
		}
	}
	return aff
}

// mapSet splits one MAP value into members; a parenthesised run is one member.
func mapSet(value string) []string {
	var out []string
	for i := 0; i < len(value); {
		if value[i] == '(' {
			if end := strings.IndexByte(value[i:], ')'); end > 1 {
				out = append(out, value[i+1:i+end])
				i += end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		out = append(out, string(r))
		i += size
	}
	return out
}

// Script is the writing system of the TRY alphabet, or Other.
func (a Affix) Script() Script { return scriptOf(string(a.Try)) }

// TagScript is the writing system of the dictionary tag in dir, or Other.
func TagScript(tag, dir string) Script {
	aff, err := ReadAffix(filepath.Join(dir, tag+".aff"))
	if err != nil {
		return Other // an unreadable affix file is reported when the dictionary loads
	}
	return aff.Script()
}

// Rarity is one language's answer to "did you mean a much commoner word".
type Rarity struct {
	freq    *Frequencies
	try     []rune
	confuse [][]string
	clitics string
	script  Script
}

// NewRarity pairs a loaded frequency list with its dictionary's affix data.
func NewRarity(freq *Frequencies, aff Affix) *Rarity {
	if freq == nil {
		return nil
	}
	r := &Rarity{freq: freq, try: aff.Try, confuse: aff.Map}
	if src, ok := FreqSourceFor(freq.Tag()); ok {
		r.clitics = cliticPrefixes[src.Lang]
	}
	// From the declared alphabet, else from the list's own words.
	r.script = aff.Script()
	if r.script == Other && freq.Len() > 0 {
		r.script = scriptOf(freq.word(freq.Len() / 2))
	}
	return r
}

// Tag is the dictionary tag this rarity belongs to.
func (r *Rarity) Tag() string {
	if r == nil || r.freq == nil {
		return ""
	}
	return r.freq.Tag()
}

// Prefix is the commonest words extending a partly typed one, most common first.
func (r *Rarity) Prefix(prefix string, max int) []Completion {
	if r == nil {
		return nil
	}
	return r.freq.Prefix(prefix, max)
}

// Script is the writing system this rarity answers for.
func (r *Rarity) Script() Script {
	if r == nil {
		return Other
	}
	return r.script
}

// cliticPrefixes are letters that attach as grammar, not a slip: a word and the same
// word with a clitic are both legal. Excluded by result, not position. Hebrew only.
var cliticPrefixes = map[string]string{
	"he": "ושהבכלמ",
}

// Hint is what the rule found for one word the engine accepted.
type Hint struct {
	// Alternatives are commoner words one slip away, commonest first, at most three.
	Alternatives []string
	// Certain means replacing without asking is safe: the corpus has never seen the
	// typed word and exactly one candidate passed. Uniqueness alone is not enough —
	// real words mistyped into other real words exist.
	Certain bool
}

// Rare reports whether an accepted word is probably not meant, and the alternatives.
// ratio is how many times commoner the neighbor must be.
func (r *Rarity) Rare(word string, ratio int) (Hint, bool) {
	if r == nil || r.freq == nil || ratio <= 0 || word == "" {
		return Hint{}, false
	}
	mine := r.freq.Count(word)
	// Fast path: nothing can be ratio times commoner.
	if mine > 0 && int64(mine)*int64(ratio) > int64(r.freq.Max()) {
		return Hint{}, false
	}

	type alt struct {
		word  string
		count int
	}
	var kept []alt
	for _, c := range r.candidates(word) {
		if c == word || r.clitic(c, word) {
			continue
		}
		n := r.freq.Count(c)
		if n < rareNeighbourFloor || (mine > 0 && int64(n) < int64(ratio)*int64(mine)) {
			continue
		}
		kept = append(kept, alt{word: c, count: n})
	}
	if len(kept) == 0 {
		return Hint{}, false
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].count != kept[j].count {
			return kept[i].count > kept[j].count
		}
		return kept[i].word < kept[j].word
	})

	// Dedupe before counting: generators reach one word by several routes.
	var unique []string
	seen := make(map[string]bool, len(kept))
	for _, a := range kept {
		if !seen[a.word] {
			seen[a.word] = true
			unique = append(unique, a.word)
		}
	}
	hint := Hint{
		Alternatives: unique[:min(len(unique), rareAlternatives)],
		Certain:      mine == 0 && len(unique) == 1,
	}
	return hint, true
}

// clitic reports whether candidate is word with a grammatical prefix glued on.
func (r *Rarity) clitic(candidate, word string) bool {
	if r.clitics == "" || len(candidate) <= len(word) || !strings.HasSuffix(candidate, word) {
		return false
	}
	first, size := utf8.DecodeRuneInString(candidate)
	return len(candidate)-len(word) == size && strings.ContainsRune(r.clitics, first)
}

// candidates are the words one slip away: a transposition, a missing letter (from TRY)
// or a MAP confusion. Extra letters are not generated. Mirrors
// internal/tui/autocorrect.go, which cannot share code (`tui-no-infra`). Duplicates
// are left in; deduping the few survivors is cheaper.
func (r *Rarity) candidates(word string) []string {
	w := []rune(word)
	out := make([]string, 0, (len(w)+1)*(len(r.try)+1))

	for i := 0; i+1 < len(w); i++ {
		w[i], w[i+1] = w[i+1], w[i]
		out = append(out, string(w))
		w[i], w[i+1] = w[i+1], w[i]
	}

	buf := make([]rune, len(w)+1)
	for i := 0; i <= len(w); i++ {
		copy(buf, w[:i])
		copy(buf[i+1:], w[i:])
		for _, c := range r.try {
			buf[i] = c
			out = append(out, string(buf))
		}
	}

	for _, set := range r.confuse {
		for _, from := range set {
			for at := 0; at+len(from) <= len(word); at++ {
				if word[at:at+len(from)] != from {
					continue
				}
				for _, to := range set {
					if to != from {
						out = append(out, word[:at]+to+word[at+len(from):])
					}
				}
			}
		}
	}
	return out
}

// Word files: rare-ok.txt holds words dismissed as fine (consulted before measuring);
// personal.dic is read to know what the engine already holds, since hunspell appends
// duplicates. They stay separate: rare-ok words are already in the dictionary.

// WordFile is a list of words on disk, one per line.
type WordFile struct {
	mu    sync.Mutex
	path  string
	words map[string]bool
}

// LoadWordFile reads one; a missing file is empty.
func LoadWordFile(path string) *WordFile {
	a := &WordFile{path: path, words: map[string]bool{}}
	data, err := os.ReadFile(path) // #nosec G304 -- a word file under kith's data directory
	if err != nil {
		// Absent is the normal first run. An unreadable file acts as an empty list
		// (a rare word is underlined, nothing is lost), and Add reports the I/O.
		return a
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if word := strings.TrimSpace(line); word != "" && !strings.HasPrefix(word, "#") {
			a.words[word] = true
		}
	}
	return a
}

// Has reports whether the file already holds this word.
func (a *WordFile) Has(word string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.words[word]
}

// Add records a word and appends it to the file; a known word is a no-op.
func (a *WordFile) Add(word string) error {
	if a == nil || word == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.words[word] {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return fmt.Errorf("spell: make %s: %w", filepath.Dir(a.path), err)
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("spell: open %s: %w", a.path, err)
	}
	if _, err := f.WriteString(word + "\n"); err != nil {
		_ = f.Close() // the write error is the report
		return fmt.Errorf("spell: write %s: %w", a.path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("spell: write %s: %w", a.path, err)
	}
	a.words[word] = true
	return nil
}

// Remember records a word without writing it (hunspell writes personal.dic itself).
func (a *WordFile) Remember(word string) {
	if a == nil || word == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.words[word] = true
}

// Len is how many words it holds.
func (a *WordFile) Len() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.words)
}
