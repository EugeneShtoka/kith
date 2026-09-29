package matrix

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// stubEngine gives a test a directory holding a dictionary pair, its canned answers,
// and a symlink to the shared stub engine. The script is written once per package
// (writing and exec'ing it per test races into ETXTBSY, golang/go#22315); per-test
// answers are found through DICPATH, which production sets to the dictionary dir.
func stubEngine(t *testing.T, answers map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"en_US.aff", "en_US.dic"} {
		write(t, filepath.Join(dir, name), "0\n")
	}
	var lines strings.Builder
	for word, reply := range answers {
		lines.WriteString(word + "\t" + reply + "\n")
	}
	write(t, filepath.Join(dir, answerFile), lines.String())
	if err := os.Symlink(stubEnginePath(t), filepath.Join(dir, "stubhunspell")); err != nil {
		t.Fatalf("link stub engine: %v", err)
	}
	return dir
}

// answerFile holds "word<TAB>reply" lines; no entry means correctly spelled.
const answerFile = "answers"

// stubScript speaks the ispell pipe protocol. Command lines (`*word`, `@word`, `#`)
// are logged and, as in the real protocol, answered with nothing.
const stubScript = `#!/bin/sh
dir=${DICPATH%%:*}
echo '@(#) International Ispell Version 3.1.20 (but really a test stub)'
while IFS= read -r line; do
  case "$line" in
    '*'*|'@'*|'#'*) printf '%s\n' "$line" >> "$dir/` + commandLog + `" ;;
    *)
      word=${line#^}
      reply=''
      while IFS='	' read -r w r; do
        if [ "$w" = "$word" ]; then reply=$r; break; fi
      done < "$dir/` + answerFile + `"
      if [ -n "$reply" ]; then echo "$reply"; else echo '*'; fi
      echo ''
      ;;
  esac
done
`

// stubEngineOnce writes the package's one stub engine (os.MkdirTemp: it outlives the
// first test).
var stubEngineOnce = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "kith-stub-engine")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "stubhunspell")
	if err := os.WriteFile(path, []byte(stubScript), 0o700); err != nil {
		return "", err
	}
	return path, nil
})

// stubEnginePath is the shared engine, written once.
func stubEnginePath(t *testing.T) string {
	t.Helper()
	path, err := stubEngineOnce()
	if err != nil {
		t.Fatalf("write stub engine: %v", err)
	}
	return path
}

// commandLog is where the stub records the command lines it was sent.
const commandLog = "commands"

// checking returns a backend wired to a stub engine in its own directory.
func checking(t *testing.T, answers map[string]string) *InProc {
	t.Helper()
	dir := stubEngine(t, answers)
	b := New(nil)
	b.UseSpell(SpellSettings{Enabled: true, Command: filepath.Join(dir, "stubhunspell")})
	b.spell.dirs = []string{dir}
	t.Cleanup(b.spell.stop)
	return b
}

//nolint:misspell // deliberate: the inputs a spellchecker exists to catch
const (
	typoReceive = "recieve"
	wrongReply  = "& recieve 3 0: receive, relieve, reprieve"
)

// Teaching a word tells the engine and drops the memoized verdict.
func TestLearnWordTellsTheEngineAndForgetsTheVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		forever bool
		want    []string
	}{
		{
			name:    "forever adds it and saves at once",
			forever: true,
			want:    []string{"*" + typoReceive, "#"},
		},
		{
			name:    "this session only just ignores it",
			forever: false,
			want:    []string{"@" + typoReceive},
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			b := checking(t, map[string]string{typoReceive: wrongReply})
			if _, err := b.CheckSpelling(t.Context(), typoReceive); err != nil {
				t.Fatalf("CheckSpelling: %v", err)
			}
			if _, ok := b.spell.remembered(typoReceive); !ok {
				t.Fatal("the verdict was not memoized, so there is nothing to forget")
			}
			if err := b.LearnWord(t.Context(), typoReceive, c.forever); err != nil {
				t.Fatalf("LearnWord: %v", err)
			}
			if _, ok := b.spell.remembered(typoReceive); ok {
				t.Error("the memo still holds a verdict for a word the engine has been taught")
			}
			if got := waitForCommands(t, b.spell.dirs[0], len(c.want)); !slices.Equal(got, c.want) {
				t.Errorf("the engine was sent %v, want %v", got, c.want)
			}
		})
	}
}

// waitForCommands polls the stub's log until it holds want lines (commands get no
// reply to synchronize on).
func waitForCommands(t *testing.T, dir string, want int) []string {
	t.Helper()
	path := filepath.Join(dir, commandLog)
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		lines := strings.Fields(strings.TrimSpace(string(data)))
		if err == nil && len(lines) >= want {
			return lines
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine was sent %v, want %d command(s)", lines, want)
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLearnWordWithNoEngineSaysThereIsNothingToCheckWith(t *testing.T) {
	t.Parallel()

	b := New(nil)
	b.spell.dirs = []string{t.TempDir()} // no engine, no dictionaries
	b.UseSpell(SpellSettings{Enabled: true, Command: "definitely-not-an-engine"})
	t.Cleanup(b.spell.stop)

	if err := b.LearnWord(t.Context(), typoReceive, true); !errors.Is(err, api.ErrSpellUnavailable) {
		t.Errorf("LearnWord = %v, want ErrSpellUnavailable", err)
	}
}

// Offsets are byte offsets into the text that went in (Hebrew makes bytes != runes).
func TestCheckSpellingReportsWhereTheWordIs(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"I " + typoReceive + " mail", "שלום " + typoReceive + " עולם"} {
		b := checking(t, map[string]string{typoReceive: wrongReply})
		got, err := b.CheckSpelling(t.Context(), text)
		if err != nil {
			t.Fatalf("CheckSpelling: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("found %v, want one misspelling", got)
		}
		if covered := text[got[0].Start:got[0].End]; covered != typoReceive {
			t.Errorf("the range covers %q, want %q", covered, typoReceive)
		}
		if got[0].Word != typoReceive {
			t.Errorf("word = %q, want %q", got[0].Word, typoReceive)
		}
		if len(got[0].Suggestions) != 3 || got[0].Suggestions[0] != "receive" {
			t.Errorf("suggestions = %q, want the engine's three", got[0].Suggestions)
		}
	}
}

// A repeated word is asked about once (memoized).
func TestCheckSpellingAsksAboutAWordOnce(t *testing.T) {
	t.Parallel()

	b := checking(t, map[string]string{typoReceive: wrongReply})
	text := typoReceive + " and " + typoReceive + " again"
	got, err := b.CheckSpelling(t.Context(), text)
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %v, want both occurrences marked", got)
	}
	b.spell.mu.Lock()
	remembered := len(b.spell.memo)
	b.spell.mu.Unlock()
	if remembered != 3 { // the typo, "and", "again"
		t.Errorf("memo holds %d words, want one entry per distinct word", remembered)
	}
}

// A draft with no words starts no engine (the engine here does not exist).
func TestCheckSpellingSkipsADraftWithNoWordsInIt(t *testing.T) {
	t.Parallel()

	b := New(nil)
	b.UseSpell(SpellSettings{Enabled: true, Command: "definitely-not-a-real-engine"})
	got, err := b.CheckSpelling(t.Context(), "https://example.com/a/b :tada: @me:x")
	if err != nil {
		t.Errorf("a draft of a URL, a shortcode and an MXID returned %v", err)
	}
	if got != nil {
		t.Errorf("found %v in a draft with no words in it", got)
	}
}

func TestCheckSpellingHonorsTheSetting(t *testing.T) {
	t.Parallel()

	dir := stubEngine(t, nil)
	b := New(nil)
	b.UseSpell(SpellSettings{Enabled: false, Command: filepath.Join(dir, "stubhunspell")})
	b.spell.dirs = []string{dir}

	if _, err := b.CheckSpelling(t.Context(), "hello there"); !errors.Is(err, api.ErrSpellUnavailable) {
		t.Errorf("err = %v, want ErrSpellUnavailable", err)
	}
}

// A missing engine is decided once, not re-probed on every check.
func TestNoEngineIsDecidedOnce(t *testing.T) {
	t.Parallel()

	b := New(nil)
	b.UseSpell(SpellSettings{Enabled: true, Command: "definitely-not-a-real-engine"})
	b.spell.dirs = []string{t.TempDir()}

	for i := range 3 {
		if _, err := b.CheckSpelling(t.Context(), "hello there"); !errors.Is(err, api.ErrSpellUnavailable) {
			t.Fatalf("call %d: err = %v, want ErrSpellUnavailable", i, err)
		}
	}
	b.spell.mu.Lock()
	starts, gone := b.spell.starts, b.spell.gone
	b.spell.mu.Unlock()
	if starts != 1 || !gone {
		t.Errorf("tried %d times and gone = %v; a missing engine is not going to appear", starts, gone)
	}
}

// A dying engine is restarted up to spellRestarts times.
func TestADeadEngineIsRestartedButNotForever(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"en_US.aff", "en_US.dic"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("0\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	script := "#!/bin/sh\necho '@(#) International Ispell Version 3.1.20 (but really a test stub)'\nexit 0\n"
	engine := filepath.Join(dir, "dyinghunspell")
	if err := os.WriteFile(engine, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub engine: %v", err)
	}

	b := New(nil)
	b.UseSpell(SpellSettings{Enabled: true, Command: engine})
	b.spell.dirs = []string{dir}
	t.Cleanup(b.spell.stop)

	var last error
	for range 5 {
		_, last = b.CheckSpelling(t.Context(), "hello there")
	}
	if !errors.Is(last, api.ErrSpellUnavailable) {
		t.Errorf("err = %v, want ErrSpellUnavailable once it has given up", last)
	}
	b.spell.mu.Lock()
	starts := b.spell.starts
	b.spell.mu.Unlock()
	if starts != spellRestarts {
		t.Errorf("started %d engines over five checks, want the cap of %d", starts, spellRestarts)
	}
}

func TestStopEndsTheEngine(t *testing.T) {
	t.Parallel()

	b := checking(t, nil)
	if _, err := b.CheckSpelling(t.Context(), "hello there"); err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	b.spell.mu.Lock()
	live := b.spell.checker != nil && b.spell.checker.Live()
	b.spell.mu.Unlock()
	if !live {
		t.Fatal("no engine was started")
	}
	b.spell.stop()
	b.spell.mu.Lock()
	stopped := b.spell.checker == nil
	b.spell.mu.Unlock()
	if !stopped {
		t.Error("the engine outlived Stop")
	}
}

// The zero value is off: no subprocess without UseSpell.
func TestSpellIsOffUntilItIsConfigured(t *testing.T) {
	t.Parallel()

	var got []domain.Misspelling
	got, err := New(nil).CheckSpelling(context.Background(), "hello there")
	if !errors.Is(err, api.ErrSpellUnavailable) {
		t.Errorf("err = %v, want ErrSpellUnavailable", err)
	}
	if got != nil {
		t.Errorf("an unconfigured backend found %v", got)
	}
}
