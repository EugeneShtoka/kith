package spell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

//nolint:misspell // deliberate: these are the inputs a spellchecker exists to catch
const (
	typoReceive = "recieve"
	typoThe     = "teh"
)

// stubArg marks the re-invocation and says how the stub should behave.
const stubArg = "spellstub:"

// TestSpellStubEngine is the stub. It skips unless it was started as one, which is
// what lets it live in the ordinary test file and still be a program.
func TestSpellStubEngine(t *testing.T) {
	mode := stubMode()
	if mode == "" {
		t.Skip("not running as the stub engine")
	}
	speakISpell(mode)
}

func stubMode() string {
	for _, arg := range os.Args {
		if mode, ok := strings.CutPrefix(arg, stubArg); ok {
			return mode
		}
	}
	return ""
}

// speakISpell is a minimal engine: a banner, then one canned answer per line.
func speakISpell(mode string) {
	fmt.Println("@(#) International Ispell Version 3.1.20 (but really a test stub)")
	if mode == "die" {
		os.Exit(0)
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		line := in.Text()
		// Commands produce no output at all, which is why Add and Ignore do not wait
		// for one. Echoing them to stderr lets a test see they arrived.
		if strings.HasPrefix(line, "*") || strings.HasPrefix(line, "@") || line == "#" {
			fmt.Fprintln(os.Stderr, "command:", line)
			continue
		}
		switch mode {
		case "silent":
			// Sleeps rather than blocking forever: an empty select parks the only
			// goroutine, the runtime calls that a deadlock and kills the process —
			// which would make this stub *exit* when the point of it is to hang.
			time.Sleep(time.Hour)
		case "wrong":
			fmt.Println("& " + typoReceive + " 3 0: receive, relieve, reprieve")
		case "none":
			fmt.Println("# xyzzy 0")
		default:
			fmt.Println("*")
		}
		fmt.Println()
	}
	os.Exit(0)
}

func stub(t *testing.T, mode string) *Checker {
	t.Helper()
	c, err := start(t.Context(), os.Args[0], []string{"-test.run=TestSpellStubEngine", stubArg + mode}, nil)
	if err != nil {
		t.Fatalf("start stub: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// onceEngine writes a stub engine script once per package, at package init: exec'ing
// a file written while another test forks races into ETXTBSY (golang/go#22315), and
// nothing forks before the tests start.
func onceEngine(script string) func(*testing.T) string {
	var path string
	var err error
	if stubMode() == "" { // a stub child answers as the engine and writes none
		path, err = writeEngine(script)
	}
	return func(t *testing.T) string {
		t.Helper()
		if err != nil {
			t.Fatalf("write fake engine: %v", err)
		}
		return path
	}
}

// writeEngine puts script in a fresh directory as an executable.
func writeEngine(script string) (string, error) {
	dir, err := os.MkdirTemp("", "kith-spell-stub")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "fake-engine")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// echoDicPathEngine answers every word as a misspelling whose "suggestion" is the
// DICPATH it was handed, which is how a test reads the child's environment back.
var echoDicPathEngine = onceEngine("#!/bin/sh\n" +
	"echo '@(#) International Ispell Version 3.1.20 (but really a test stub)'\n" +
	"while read -r _; do printf '& x 1 0: %s\\n\\n' \"$DICPATH\"; done\n")

// correctEngine answers every word as correctly spelled.
var correctEngine = onceEngine(
	"#!/bin/sh\necho 'ispell stub'\nwhile read -r _; do echo '*'; echo ''; done\n")

// The engine is told where the dictionaries are via DICPATH; otherwise it exits silently.
func TestStartTellsTheEngineWhereTheDictionariesAre(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	engine := echoDicPathEngine(t)
	ours, system := filepath.Join(dir, "ours"), filepath.Join(dir, "system")
	c, err := Start(t.Context(), Availability{
		Engine: Engine{Command: "fake-engine", Path: engine},
		// Two dictionaries in one directory and one in another: the child should be
		// told each directory once, in the order Look found them.
		Dictionaries: []Dictionary{
			{Tag: "en_US", Dir: ours},
			{Tag: "he_IL", Dir: ours},
			{Tag: "ru_RU", Dir: system},
		},
	}, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.Check(t.Context(), "anything")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := ours + string(os.PathListSeparator) + system
	if len(got.Suggestions) != 1 || got.Suggestions[0] != want {
		t.Errorf("the engine was started with DICPATH=%q, want %q", got.Suggestions, want)
	}
}

// The personal dictionary exists before the engine does, and an existing one is left
// exactly as it was.
func TestStartCreatesThePersonalDictionaryAndKeepsWhatIsInIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fresh := filepath.Join(dir, "personal.dic")
	c := stubWithPersonal(t, fresh)
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the personal dictionary was not created: %v", err)
	}
	_ = c.Close()

	kept := filepath.Join(dir, "kept.dic")
	if err := os.WriteFile(kept, []byte("Shtoka\n"), 0o600); err != nil {
		t.Fatalf("write personal dictionary: %v", err)
	}
	c = stubWithPersonal(t, kept)
	t.Cleanup(func() { _ = c.Close() })
	data, err := os.ReadFile(kept)
	if err != nil || string(data) != "Shtoka\n" {
		t.Errorf("the existing dictionary became %q (%v); starting must not empty it", data, err)
	}
}

// stubWithPersonal starts the stub engine with a personal dictionary at path.
func stubWithPersonal(t *testing.T, path string) *Checker {
	t.Helper()
	dir := t.TempDir()
	engine := correctEngine(t)
	c, err := Start(t.Context(), Availability{
		Engine:       Engine{Command: "fake-engine", Path: engine},
		Dictionaries: []Dictionary{{Tag: "en_US", Dir: dir}},
	}, path)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return c
}

// The banner has to be eaten at startup or it is read as the answer to the first word
// ever checked — which would make that word correct whatever it was.
func TestCheckerEatsTheBanner(t *testing.T) {
	t.Parallel()

	c := stub(t, "wrong")
	got, err := c.Check(t.Context(), typoReceive)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.OK {
		t.Fatal("the first word came back correct — the banner was read as its answer")
	}
	if len(got.Suggestions) != 3 || got.Suggestions[0] != "receive" {
		t.Errorf("suggestions = %q, want the three from the stub", got.Suggestions)
	}
}

func TestCheckerReadsEachKindOfAnswer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode string
		ok   bool
		sugs int
	}{
		{"ok", true, 0},
		{"wrong", false, 3},
		{"none", false, 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()

			got, err := stub(t, tc.mode).Check(t.Context(), "word")
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if got.OK != tc.ok {
				t.Errorf("OK = %v, want %v", got.OK, tc.ok)
			}
			if len(got.Suggestions) != tc.sugs {
				t.Errorf("suggestions = %q, want %d", got.Suggestions, tc.sugs)
			}
		})
	}
}

// Several checks in a row share one pipe, so the answers must not run together: each
// one has to stop at its own blank line.
func TestCheckerKeepsAnswersApart(t *testing.T) {
	t.Parallel()

	c := stub(t, "wrong")
	for i := range 5 {
		got, err := c.Check(t.Context(), typoReceive)
		if err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
		if len(got.Suggestions) != 3 {
			t.Fatalf("check %d: suggestions = %q, want three every time", i, got.Suggestions)
		}
	}
}

// An engine that has stopped is a state, not an incident: the caller's move is to
// stop asking, and every later call has to say so rather than block.
func TestCheckerGoesDeadOnce(t *testing.T) {
	t.Parallel()

	c := stub(t, "die")
	if _, err := c.Check(t.Context(), "word"); !errors.Is(err, ErrDead) {
		t.Fatalf("Check on a dead engine = %v, want ErrDead", err)
	}
	if c.Live() {
		t.Error("Live is true after the engine exited")
	}
	if _, err := c.Check(t.Context(), "word"); !errors.Is(err, ErrDead) {
		t.Errorf("second Check = %v, want ErrDead again", err)
	}
	// Closing a dead checker is what a caller does on quit having already closed on
	// the error, and it must not be a second failure.
	if err := c.Close(); err != nil {
		t.Errorf("Close after death = %v, want nil", err)
	}
}

// An engine that stops answering is indistinguishable from one that is slow, and the
// composer cannot wait to find out which.
func TestCheckerGivesUpWhenTheEngineStopsAnswering(t *testing.T) {
	t.Parallel()

	c := stub(t, "silent")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Check(ctx, "word")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Check = %v, want the deadline", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Check waited %v — it should give up with the context", took)
	}
	if c.Live() {
		t.Error("an engine that stopped answering should be abandoned, not kept")
	}
}

// Add, Ignore and Save produce no reply by design; the test is that they neither
// block waiting for one nor poison the next check.
func TestCommandsDoNotWaitForAReply(t *testing.T) {
	t.Parallel()

	c := stub(t, "ok")
	if err := c.Add("kith"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := c.Ignore("hunspell"); err != nil {
		t.Fatalf("Ignore: %v", err)
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := c.Check(t.Context(), "word")
	if err != nil {
		t.Fatalf("Check after commands: %v", err)
	}
	if !got.OK {
		t.Error("a command's silence was read as the next word's answer")
	}
}

// Start refuses when there is nothing to check with, rather than launching an engine
// that would exit immediately with a message nobody sees.
func TestStartRefusesWithoutDictionaries(t *testing.T) {
	t.Parallel()

	_, err := Start(t.Context(), Availability{Engine: Engine{Path: "/usr/bin/hunspell"}}, "")
	if !errors.Is(err, ErrDead) {
		t.Errorf("Start with no dictionaries = %v, want ErrDead", err)
	}
}

// silentEngine starts and never prints its banner.
var silentEngine = onceEngine("#!/bin/sh\nexec sleep 60\n")

// An engine that never prints its banner fails the start instead of hanging it: the
// backend starts engines under its own lock, so every spell check would wait too. The
// context is long-lived, as the backend's is; a deadline would kill the engine anyway.
func TestAnEngineThatNeverSpeaksFailsTheStart(t *testing.T) {
	t.Parallel()
	if stubMode() != "" {
		t.Skip("runs only as the parent")
	}

	begun := time.Now()
	c, err := start(t.Context(), silentEngine(t), nil, nil)
	if err == nil {
		_ = c.Close()
		t.Fatal("start() succeeded with no banner")
	}
	if waited := time.Since(begun); waited > bannerTimeout+2*time.Second {
		t.Errorf("start() took %s, want about %s", waited, bannerTimeout)
	}
}

// An engine started through a wrapper script, whose child neither answers nor exits
// on end of input: the banner timeout still ends the start on time, and the child is
// killed with the wrapper rather than left running. (It once held the start, and the
// spell lock, for as long as it lived: only the wrapper was killed, and the reader
// waited on a pipe the child kept open.)
func TestAWrapperEngineThatNeverSpeaksStillTimesOut(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("a shell wrapper")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "hunspell")
	body := "#!/bin/sh\nsleep 30 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	// A parallel test's fork can hold the script's write descriptor until it execs:
	// "text file busy" is that, not the engine, so it is tried again.
	var err error
	for range 50 {
		if _, err = start(context.Background(), script, []string{"-a"}, nil); !errors.Is(err, syscall.ETXTBSY) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err == nil {
		t.Fatal("start succeeded without a banner")
	}
	if took := time.Since(began); took > bannerTimeout+2*time.Second {
		t.Errorf("start took %s; the banner timeout is %s", took, bannerTimeout)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the wrapper's child (pid %d) outlived the engine", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
