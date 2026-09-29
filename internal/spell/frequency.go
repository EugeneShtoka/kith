package spell

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Word frequency lists, the data behind rare-word hints: some dictionaries (Hspell)
// accept nearly every string grammar can generate, so a typo landing on a legal form
// is invisible without counts. Lists are pinned, checksummed and never automatic like
// dictionaries, but pruned below frequencyMinCount and rewritten sorted by word so a
// lookup is a binary search over one blob.

// FreqSource is a frequency list that can be installed, named by the dictionary tag it
// belongs to.
type FreqSource struct {
	// Tag is the dictionary tag (he_IL, not he); tags may share one upstream file.
	Tag  string
	Lang string
	List File

	// Words and Kept are the entries and bytes surviving pruning, per the generator.
	Words int
	Kept  int64
}

// Bytes is the download size.
func (s FreqSource) Bytes() int64 { return s.List.Size + frequencyLicense.Size }

// Disk is the installed size.
func (s FreqSource) Disk() int64 { return s.Kept + frequencyLicense.Size }

// ErrUnknownFrequencies is returned for a tag the frequency manifest does not carry.
var ErrUnknownFrequencies = errors.New("spell: no frequency list for that language")

// FreqInstallable lists the frequency lists that can be fetched, sorted.
func FreqInstallable() []string {
	out := make([]string, 0, len(frequencySources))
	for tag := range frequencySources {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// FreqSourceFor returns the manifest entry for a tag.
func FreqSourceFor(tag string) (FreqSource, bool) {
	s, ok := frequencySources[tag]
	return s, ok
}

// FreqPin describes where frequency lists come from, for the confirmation prompt.
func FreqPin() (repo, commit, date string) {
	return frequencyRepo, frequencyCommit, frequencyDate
}

// FreqDir is where installed lists live; beside hunspell/, which must not see them.
func FreqDir(dataDir string) string { return filepath.Join(dataDir, "frequency") }

// FreqFile is one tag's list in the list directory.
func FreqFile(dir, tag string) string { return filepath.Join(dir, tag+freqExt) }

// dictionaryAccepts is the measured share of random three-letter strings each
// dictionary accepts. Unmeasured languages are still installable, just not recommended.
var dictionaryAccepts = map[string]float64{
	"he_IL": 0.23,
	"en_US": 0.08,
	"en_GB": 0.08,
}

// acceptsTooMuch is the share above which a dictionary cannot catch typos alone.
const acceptsTooMuch = 0.15

// Accepts is how much of its language's three-letter space the dictionary accepts, and
// whether that is enough to want counts. ok is false for a language nobody has measured.
func Accepts(tag string) (share float64, needs, ok bool) {
	share, ok = dictionaryAccepts[tag]
	return share, share >= acceptsTooMuch, ok
}

// FreqInstalled lists the tags that have a list on disk in dir, sorted.
func FreqInstalled(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // no directory yet: nothing installed
	}
	var out []string
	for _, e := range entries {
		if name := e.Name(); !e.IsDir() && strings.HasSuffix(name, freqExt) {
			out = append(out, strings.TrimSuffix(name, freqExt))
		}
	}
	sort.Strings(out)
	return out
}

const freqExt = ".freq"

// InstallFreq downloads one frequency list into dir, pruned, via a temp file and
// rename: a truncated list would make every word past the cut look rare.
func InstallFreq(ctx context.Context, client *http.Client, tag, dir string) (FreqSource, error) {
	src, ok := frequencySources[tag]
	if !ok {
		return FreqSource{}, fmt.Errorf("%w: %q", ErrUnknownFrequencies, tag)
	}
	return src, installFreq(ctx, client, frequencyBase, src, frequencyLicense, dir)
}

// installFreq is InstallFreq with the origin and entry decided, for tests.
func installFreq(ctx context.Context, client *http.Client, base string, src FreqSource, lic File, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("spell: make %s: %w", dir, err)
	}

	// License fetched first (cheap refusal) and written last (never a lone license).
	var licBody []byte
	if lic.Path != "" {
		body, err := fetchVerified(ctx, client, base, lic)
		if err != nil {
			return fmt.Errorf("spell: %s: %w", lic.Path, err)
		}
		licBody = body
	}

	entries, total, err := fetchList(ctx, client, base, src.List)
	if err != nil {
		return fmt.Errorf("spell: %s: %w", src.List.Path, err)
	}
	if err := writeList(filepath.Join(dir, src.Tag+freqExt), src, entries, total); err != nil {
		return err
	}
	if licBody == nil {
		return nil
	}
	path := filepath.Join(dir, src.Tag+".LICENSE")
	if err := os.WriteFile(path, licBody, 0o644); err != nil { // #nosec G306 -- a license is world-readable text
		return fmt.Errorf("spell: write %s: %w", path, err)
	}
	return nil
}

// freqEntry is one word and how often the corpus saw it.
type freqEntry struct {
	word  string
	count int32
}

// fetchList downloads and verifies a list, parsing as it streams, and returns the
// entries worth keeping plus the count before pruning.
func fetchList(ctx context.Context, client *http.Client, base string, file File) ([]freqEntry, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+file.Path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("fetch: %s", resp.Status)
	}

	// One byte over the limit so "too long" is detectable.
	sum := sha256.New()
	counted := &countingReader{r: io.LimitReader(resp.Body, file.Size+1)}
	entries, total, err := pruneLines(io.TeeReader(counted, sum))
	if err != nil {
		return nil, 0, err
	}
	if err := verify(file, counted.n, sum); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// verify checks size and SHA-256 against the manifest.
func verify(file File, read int64, sum hash.Hash) error {
	if read != file.Size {
		return fmt.Errorf("%w: %d bytes, expected %d", ErrCorrupt, read, file.Size)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != file.SHA256 {
		return fmt.Errorf("%w: sha256 %s, expected %s", ErrCorrupt, got, file.SHA256)
	}
	return nil
}

// countingReader counts bytes passed through, for the size check.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err //nolint:wrapcheck // a pass-through reader must return io.EOF as it is
}

// pruneLines reads `<word> <count>` lines, keeping those at or above the threshold.
// Malformed lines are skipped.
func pruneLines(r io.Reader) ([]freqEntry, int, error) {
	var out []freqEntry
	total := 0
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		word, count, ok := splitEntry(scan.Bytes())
		if !ok {
			continue
		}
		total++
		if count >= frequencyMinCount {
			out = append(out, freqEntry{word: string(word), count: count})
		}
	}
	if err := scan.Err(); err != nil {
		return nil, 0, fmt.Errorf("read: %w", err)
	}
	return out, total, nil
}

// splitEntry pulls one word and its count out of a line without allocating. A word
// has no space, so header comments fail to parse on their own and `#hash 9` is still
// an entry.
func splitEntry(line []byte) (word []byte, count int32, ok bool) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	sp := bytes.LastIndexByte(line, ' ')
	if sp <= 0 || sp == len(line)-1 || bytes.IndexByte(line[:sp], ' ') >= 0 {
		return nil, 0, false
	}
	for _, c := range line[sp+1:] {
		if c < '0' || c > '9' {
			return nil, 0, false
		}
		count = count*10 + int32(c-'0')
		if count < 0 {
			return nil, 0, false // overflow
		}
	}
	return line[:sp], count, true
}

// freqHeader describes an installed list; its lines never parse as entries.
func freqHeader(src FreqSource, kept, total int) string {
	repo, commit, date := FreqPin()
	return fmt.Sprintf(
		"# kith frequency list — %s (%s)\n"+
			"# source: %s %s\n"+
			"# commit: %s (%s)\n"+
			"# license: MIT for upstream's code, CC-BY-SA-4.0 for these counts;\n"+
			"#          they are counted from the OpenSubtitles2018 corpus.\n"+
			"# kept: %d of %d entries, seen %d times or more, sorted by word\n"+
			"# format: <word> <count>, one per line\n",
		src.Tag, src.Lang, repo, src.List.Path, commit, date, kept, total, frequencyMinCount)
}

// writeList writes entries sorted by word, with a provenance/license header (as
// CC-BY-SA asks), via a .part file and rename.
func writeList(path string, src FreqSource, entries []freqEntry, total int) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].word < entries[j].word })

	part := path + ".part"
	f, err := os.Create(part) // #nosec G304 -- an install target under kith's data directory
	if err != nil {
		return fmt.Errorf("spell: write %s: %w", part, err)
	}
	// bufio.Writer keeps the first error for Flush.
	w := bufio.NewWriterSize(f, 1<<16)
	_, _ = w.WriteString(freqHeader(src, len(entries), total))
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%s %d\n", e.word, e.count)
	}
	if err := w.Flush(); err != nil {
		_ = f.Close() // the flush error is the report; the .part is overwritten next time
		return fmt.Errorf("spell: write %s: %w", part, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("spell: write %s: %w", part, err)
	}
	if err := os.Rename(part, path); err != nil {
		return fmt.Errorf("spell: install %s: %w", path, err)
	}
	return nil
}

// Frequencies is one language's counts: one word blob, offsets and counts, searched
// by binary search — far smaller than a map for ~456k entries.
type Frequencies struct {
	tag    string
	blob   string
	offs   []int32
	counts []int32
	// max lets the rare check skip ordinary words without generating candidates.
	max int32
}

// LoadFreq reads an installed list.
func LoadFreq(path string) (*Frequencies, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- an installed list under kith's data directory
	if err != nil {
		return nil, fmt.Errorf("spell: read %s: %w", path, err)
	}
	// Offsets are int32.
	if int64(len(data)) > math.MaxInt32 {
		return nil, fmt.Errorf("spell: %s: %w", path, errListTooLarge)
	}
	f := parseFreq(strings.TrimSuffix(filepath.Base(path), freqExt), data)
	if f.Len() == 0 {
		return nil, fmt.Errorf("spell: %s: %w", path, errEmptyList)
	}
	return f, nil
}

var (
	errEmptyList    = errors.New("no entries")
	errListTooLarge = errors.New("frequency list is larger than 2 GB")
)

// parseFreq builds the table from the file's bytes.
func parseFreq(tag string, data []byte) *Frequencies {
	f := &Frequencies{tag: tag}
	var blob strings.Builder
	blob.Grow(len(data) / 2)
	f.offs = append(f.offs, 0)

	sorted := true
	prev := ""
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		word, count, ok := splitEntry(line)
		if !ok {
			continue
		}
		blob.Write(word)
		f.offs = append(f.offs, int32(blob.Len())) // #nosec G115 -- LoadFreq refuses a file that cannot fit int32 offsets
		f.counts = append(f.counts, count)
		if count > f.max {
			f.max = count
		}
		if w := string(word); w < prev {
			sorted = false
		} else {
			prev = w
		}
	}
	f.blob = blob.String()
	if !sorted {
		f.resort()
	}
	return f
}

// resort orders a list that was not written sorted (e.g. hand-edited).
func (f *Frequencies) resort() {
	order := make([]int, f.Len())
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return f.word(order[i]) < f.word(order[j]) })

	var blob strings.Builder
	blob.Grow(len(f.blob))
	offs := make([]int32, 1, len(f.offs))
	counts := make([]int32, 0, len(f.counts))
	for _, i := range order {
		blob.WriteString(f.word(i))
		offs = append(offs, int32(blob.Len())) // #nosec G115 -- the blob is the one parseFreq already bounded
		counts = append(counts, f.counts[i])
	}
	f.blob, f.offs, f.counts = blob.String(), offs, counts
}

// Tag is the dictionary tag this list belongs to.
func (f *Frequencies) Tag() string { return f.tag }

// Len is how many words it holds.
func (f *Frequencies) Len() int { return len(f.counts) }

// Max is the commonest count in the list.
func (f *Frequencies) Max() int {
	if f == nil {
		return 0
	}
	return int(f.max)
}

// Count is how often the corpus saw a word; 0 for never or below the pruning threshold.
func (f *Frequencies) Count(word string) int {
	if f == nil || word == "" {
		return 0
	}
	i := sort.Search(f.Len(), func(i int) bool { return f.word(i) >= word })
	if i < f.Len() && f.word(i) == word {
		return int(f.counts[i])
	}
	return 0
}

// Completion is one word the list offers for a prefix, with the count that ranked it.
type Completion struct {
	Word  string
	Count int
}

// Prefix is up to max of the commonest words beginning with prefix (excluding prefix
// itself), for completion below the cache tier.
func (f *Frequencies) Prefix(prefix string, max int) []Completion {
	if f == nil || prefix == "" || max <= 0 {
		return nil
	}
	i := sort.Search(f.Len(), func(i int) bool { return f.word(i) >= prefix })
	// The whole run is scanned: the file is ordered by word, the answer by count.
	var found []Completion
	for ; i < f.Len(); i++ {
		word := f.word(i)
		if !strings.HasPrefix(word, prefix) {
			break
		}
		if word == prefix {
			continue
		}
		found = append(found, Completion{Word: word, Count: int(f.counts[i])})
	}
	sort.SliceStable(found, func(a, b int) bool { return found[a].Count > found[b].Count })
	if len(found) > max {
		found = found[:max]
	}
	return found
}

// word is the i-th word, as a slice of the blob rather than a copy of it.
func (f *Frequencies) word(i int) string { return f.blob[f.offs[i]:f.offs[i+1]] }
