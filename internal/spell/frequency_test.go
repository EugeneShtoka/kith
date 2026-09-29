package spell

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upstreamList is upstream's shape (descending counts), with entries on both sides of
// the pruning threshold and out of alphabetical order.
const upstreamList = "the 900\nzebra 40\nalpha 12\nbeta 3\ngamma 2\ndelta 1\n"

// upstreamLicense stands in for the license that travels with the data.
const upstreamLicense = "MIT + CC-BY-SA-4.0\n"

func freqFixture(t *testing.T, list, lic []byte) (string, FreqSource, File, *http.Client) {
	t.Helper()

	files := map[string][]byte{
		"content/2018/xx/xx_full.txt": list,
		"LICENSE":                     lic,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path[1:]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	src := FreqSource{
		Tag:  "xx_XX",
		Lang: "xx",
		List: File{
			Path:   "content/2018/xx/xx_full.txt",
			SHA256: sum(list),
			Size:   int64(len(list)),
		},
		Words: 3,
		Kept:  int64(len("the 900\nzebra 40\nalpha 12\nbeta 3\n")),
	}
	license := File{Path: "LICENSE", SHA256: sum(lic), Size: int64(len(lic))}
	return srv.URL + "/", src, license, srv.Client()
}

func TestInstallFreqPrunesSortsAndKeepsTheLicense(t *testing.T) {
	t.Parallel()

	base, src, lic, client := freqFixture(t, []byte(upstreamList), []byte(upstreamLicense))
	dir := t.TempDir()

	if err := installFreq(t.Context(), client, base, src, lic, dir); err != nil {
		t.Fatalf("installFreq: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "xx_XX.freq"))
	if err != nil {
		t.Fatalf("read installed list: %v", err)
	}
	var head, data []string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(body), "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			head = append(head, line)
			continue
		}
		data = append(data, line)
	}

	// Pruned at the threshold and sorted by word, not by count: `gamma 2` and
	// `delta 1` are below it, and `the` sorts after `beta` however often it was said.
	want := []string{"alpha 12", "beta 3", "the 900", "zebra 40"}
	if strings.Join(data, "|") != strings.Join(want, "|") {
		t.Errorf("installed list = %q, want %q", data, want)
	}
	// The header is what lets a file found on disk say what it is, which is both what
	// CC-BY-SA asks of a derivative and what somebody finding megabytes in their data
	// directory is owed.
	joined := strings.Join(head, "\n")
	for _, must := range []string{"xx_XX", frequencyRepo, frequencyCommit, "CC-BY-SA-4.0", "4 of 6"} {
		if !strings.Contains(joined, must) {
			t.Errorf("header %q does not mention %q", joined, must)
		}
	}
	if got, err := os.ReadFile(filepath.Join(dir, "xx_XX.LICENSE")); err != nil || string(got) != upstreamLicense {
		t.Errorf("license = %q, %v; want %q", got, err, upstreamLicense)
	}
	// Nothing half-written left over: the pruned copy is renamed into place, so a
	// `.part` surviving the install would be a list that is short and looks complete.
	if _, err := os.Stat(filepath.Join(dir, "xx_XX.freq.part")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a .part file survived the install: %v", err)
	}
}

// The check the pinning exists for, and the part that matters: nothing is written. A
// truncated frequency list is worse than none — every word past the cut looks infinitely
// rare, which is exactly the shape that produces false marks.
func TestInstallFreqRefusesAndWritesNothingOnAHashMismatch(t *testing.T) {
	t.Parallel()

	base, src, lic, client := freqFixture(t, []byte(upstreamList), []byte(upstreamLicense))
	src.List.SHA256 = strings.Repeat("0", 64)
	dir := t.TempDir()

	err := installFreq(t.Context(), client, base, src, lic, dir)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("installFreq = %v, want ErrCorrupt", err)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("read dir: %v", rerr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "xx_XX.freq") {
			t.Errorf("%s was written despite the mismatch", e.Name())
		}
	}
}

// Size is checked as well as the hash, and separately: a server that answers with more
// bytes than the manifest allows is stopped by the limit rather than read to the end and
// then hashed.
func TestInstallFreqRefusesAListThatIsNotTheRecordedSize(t *testing.T) {
	t.Parallel()

	base, src, lic, client := freqFixture(t, []byte(upstreamList), []byte(upstreamLicense))
	src.List.Size = int64(len(upstreamList)) - 8
	dir := t.TempDir()

	if err := installFreq(t.Context(), client, base, src, lic, dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("installFreq = %v, want ErrCorrupt", err)
	}
}

func TestInstallFreqAndLoadRoundTrip(t *testing.T) {
	t.Parallel()

	base, src, lic, client := freqFixture(t, []byte(upstreamList), []byte(upstreamLicense))
	dir := t.TempDir()
	if err := installFreq(t.Context(), client, base, src, lic, dir); err != nil {
		t.Fatalf("installFreq: %v", err)
	}

	freq, err := LoadFreq(filepath.Join(dir, "xx_XX.freq"))
	if err != nil {
		t.Fatalf("LoadFreq: %v", err)
	}
	if freq.Tag() != "xx_XX" {
		t.Errorf("Tag = %q, want xx_XX", freq.Tag())
	}
	if freq.Len() != 4 {
		t.Errorf("Len = %d, want 4", freq.Len())
	}
	for word, want := range map[string]int{
		"the":   900,
		"zebra": 40,
		"alpha": 12,
		"beta":  3,
		// Below the threshold and never in the file: the list stops distinguishing
		// "rare" from "absent" at the cut, and 0 is the honest answer for both.
		"gamma": 0,
		"delta": 0,
		// Neither in the file nor anywhere near it.
		"aaaa": 0,
		"zzzz": 0,
		"":     0,
	} {
		if got := freq.Count(word); got != want {
			t.Errorf("Count(%q) = %d, want %d", word, got, want)
		}
	}
}

// A file that arrived out of order — an older install, or one somebody edited — is
// repaired rather than refused: refusing it would be a language silently unchecked for a
// reason the user cannot see.
func TestLoadFreqRepairsAnUnsortedFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "yy_YY.freq")
	const body = "# kith frequency list — yy_YY\nzebra 40\nalpha 12\nmid 7\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	freq, err := LoadFreq(path)
	if err != nil {
		t.Fatalf("LoadFreq: %v", err)
	}
	for word, want := range map[string]int{"alpha": 12, "mid": 7, "zebra": 40, "nope": 0} {
		if got := freq.Count(word); got != want {
			t.Errorf("Count(%q) = %d, want %d", word, got, want)
		}
	}
}

// `#` is a comment in the header and a word everywhere else. The lists are tokenized
// subtitles, so a token starting with one is unlikely and not impossible, and a file is
// allowed to contain its own data.
func TestLoadFreqTreatsHashAsAWordAfterTheHeader(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "zz_ZZ.freq")
	const body = "# header\n# more header\n#hash 9\nword 5\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	freq, err := LoadFreq(path)
	if err != nil {
		t.Fatalf("LoadFreq: %v", err)
	}
	if got := freq.Count("#hash"); got != 9 {
		t.Errorf("Count(#hash) = %d, want 9", got)
	}
	if got := freq.Count("word"); got != 5 {
		t.Errorf("Count(word) = %d, want 5", got)
	}
}

func TestLoadFreqReportsAMissingOrEmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := LoadFreq(filepath.Join(dir, "none.freq")); err == nil {
		t.Error("LoadFreq of a missing file = nil, want an error")
	}
	path := filepath.Join(dir, "empty.freq")
	if err := os.WriteFile(path, []byte("# nothing but a header\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadFreq(path); !errors.Is(err, errEmptyList) {
		t.Errorf("LoadFreq of a header-only file = %v, want errEmptyList", err)
	}
}

// A malformed line is skipped rather than fatal: a list is data, not a protocol, and one
// odd line in a corpus of a million is not a reason to refuse a language.
func TestPruneLinesSkipsWhatItCannotRead(t *testing.T) {
	t.Parallel()

	const junk = "good 10\nno-count\n \nbad count\nnegative -4\nhuge 99999999999999\nalso 4\n"
	entries, total, err := pruneLines(strings.NewReader(junk))
	if err != nil {
		t.Fatalf("pruneLines: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 — only the two readable lines count", total)
	}
	if len(entries) != 2 || entries[0].word != "good" || entries[1].word != "also" {
		t.Errorf("entries = %+v, want good and also", entries)
	}
}

func TestFreqPathsAndInstalled(t *testing.T) {
	t.Parallel()

	data := t.TempDir()
	dir := FreqDir(data)
	if want := filepath.Join(data, "frequency"); dir != want {
		t.Errorf("FreqDir = %q, want %q", dir, want)
	}
	if got, want := FreqFile(dir, "he_IL"), filepath.Join(dir, "he_IL.freq"); got != want {
		t.Errorf("FreqFile = %q, want %q", got, want)
	}
	// A directory that does not exist yet is not an error — it is what a machine that
	// has never installed one looks like.
	if got := FreqInstalled(dir); got != nil {
		t.Errorf("FreqInstalled of a missing dir = %v, want nil", got)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"he_IL.freq", "en_US.freq", "en_US.LICENSE"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got := FreqInstalled(dir)
	if len(got) != 2 || got[0] != "en_US" || got[1] != "he_IL" {
		t.Errorf("FreqInstalled = %v, want [en_US he_IL]", got)
	}
}

func TestInstallFreqRejectsAnUnknownTag(t *testing.T) {
	t.Parallel()

	_, err := InstallFreq(t.Context(), DefaultClient(), "xx_XX", t.TempDir())
	if !errors.Is(err, ErrUnknownFrequencies) {
		t.Fatalf("InstallFreq = %v, want ErrUnknownFrequencies", err)
	}
}

// Every list is keyed by a tag that can actually be installed as a dictionary. The
// reverse is deliberately *not* asserted: a language with a dictionary and no counts
// simply gets no rare marks, which is the designed outcome rather than a gap.
func TestEveryFrequencyListBelongsToADictionary(t *testing.T) {
	t.Parallel()

	for _, tag := range FreqInstallable() {
		src, _ := FreqSourceFor(tag)
		if _, ok := SourceFor(tag); !ok {
			t.Errorf("%s has counts but no dictionary to check with them", tag)
		}
		if src.Lang == "" || src.List.SHA256 == "" || src.List.Size == 0 {
			t.Errorf("%s: incomplete manifest entry %+v", tag, src)
		}
		if src.Words == 0 || src.Kept == 0 {
			t.Errorf("%s: unmeasured manifest entry %+v", tag, src)
		}
		// What it downloads and what it keeps are different numbers, and the prompt
		// quotes both — the pruning is two thirds of the file.
		if src.Disk() >= src.Bytes() {
			t.Errorf("%s: kept %d bytes of a %d-byte download", tag, src.Disk(), src.Bytes())
		}
	}
	if repo, commit, date := FreqPin(); repo == "" || len(commit) != 40 || date == "" {
		t.Errorf("FreqPin = %q %q %q, want a real pin", repo, commit, date)
	}
}

// The completion tier: a prefix, and the commonest words in the language that extend it.
func TestFreqPrefix(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "zz_ZZ.freq")
	const body = "# kith frequency list — zz_ZZ\n" +
		"complete 900\ncompletion 40\ncompletely 120\ncompare 7\ncomp 3000\ndelta 5\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	freq, err := LoadFreq(path)
	if err != nil {
		t.Fatalf("LoadFreq: %v", err)
	}

	got := freq.Prefix("comp", 3)
	want := []string{"complete", "completely", "completion"}
	if len(got) != len(want) {
		t.Fatalf("Prefix(comp, 3) = %+v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].Word != w {
			t.Fatalf("Prefix(comp, 3) = %+v, want %v in that order", got, want)
		}
	}
	// "comp" is the commonest entry of all and is deliberately absent: it is what has
	// already been typed.
	for _, c := range got {
		if c.Word == "comp" {
			t.Fatalf("Prefix(comp, 3) offered the prefix itself: %+v", got)
		}
	}

	if got := freq.Prefix("comp", 0); got != nil {
		t.Errorf("Prefix(comp, 0) = %+v, want nothing", got)
	}
	if got := freq.Prefix("", 3); got != nil {
		t.Errorf("Prefix(empty) = %+v, want nothing", got)
	}
	if got := freq.Prefix("nothing", 3); got != nil {
		t.Errorf("Prefix(nothing) = %+v, want nothing", got)
	}
	var nilFreq *Frequencies
	if got := nilFreq.Prefix("comp", 3); got != nil {
		t.Errorf("Prefix on no list = %+v, want nothing", got)
	}
}
