package spell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dictDir writes .aff/.dic pairs for tags, plus any stray files named, and returns
// the directory.
func dictDir(t *testing.T, tags []string, stray ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tag := range tags {
		for _, ext := range []string{".aff", ".dic"} {
			if err := os.WriteFile(filepath.Join(dir, tag+ext), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range stray {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// scan wants both halves of a pair, prefers the earlier directory for a tag, and
// ignores missing directories.
func TestScan(t *testing.T) {
	t.Parallel()

	mine := dictDir(t, []string{"en_US", "he_IL"}, "ru_RU.aff", "orphan.dic")
	system := dictDir(t, []string{"en_US", "fr_FR"})
	got := scan([]string{filepath.Join(mine, "nope"), mine, "/definitely/not/here", system})
	var tags []string
	for _, d := range got {
		tags = append(tags, d.Tag)
		if d.Tag == "en_US" && d.Dir != mine {
			t.Errorf("en_US came from %q, want the earlier directory %q", d.Dir, mine)
		}
	}
	if strings.Join(tags, ",") != "en_US,he_IL,fr_FR" {
		t.Errorf("tags = %v, want en_US,he_IL,fr_FR", tags)
	}
}

// Look narrows to the named dictionaries and reports missing ones; with none named it
// takes everything found.
func TestLook(t *testing.T) {
	t.Parallel()

	dir := dictDir(t, []string{"en_US", "he_IL", "ru_RU"})
	avail, missing := Look("hunspell", []string{"en_US", "de_DE"}, []string{dir})
	if got := avail.Tags(); len(got) != 1 || got[0] != "en_US" {
		t.Errorf("tags = %v, want just en_US", got)
	}
	if len(missing) != 1 || missing[0] != "de_DE" {
		t.Errorf("missing = %v, want de_DE", missing)
	}

	avail, missing = Look("hunspell", nil, []string{dir})
	if len(missing) != 0 || len(avail.Tags()) != 3 {
		t.Errorf("unnamed: tags = %v, missing = %v; want all three, none missing", avail.Tags(), missing)
	}
}

// Ready needs both an engine and a dictionary.
func TestReadyNeedsAnEngineAndADictionary(t *testing.T) {
	t.Parallel()

	dir := dictDir(t, []string{"en_US"})
	if (Availability{Engine: Engine{Path: "/usr/bin/hunspell"}}).Ready() {
		t.Error("an engine with no dictionaries is not ready")
	}
	avail, _ := Look("definitely-not-a-real-binary", nil, []string{dir})
	if avail.Ready() {
		t.Error("dictionaries with no engine are not ready")
	}
}

// Why names the missing half, with distro advice or the searched paths.
func TestWhySaysWhichHalfIsMissing(t *testing.T) {
	t.Parallel()

	dir := dictDir(t, []string{"en_US"})

	noEngine, _ := Look("definitely-not-a-real-binary", nil, []string{dir})
	why := noEngine.Why("arch")
	if !strings.Contains(why, "was not found") {
		t.Errorf("why = %q, want it to name the missing engine", why)
	}
	if !strings.Contains(why, "pacman -S") {
		t.Errorf("why = %q, want the command for this distro", why)
	}

	noDicts := Availability{
		Engine:   Engine{Command: "hunspell", Path: "/usr/bin/hunspell"},
		Searched: []string{"/usr/share/hunspell", "/usr/share/myspell"},
	}
	why = noDicts.Why("arch")
	if !strings.Contains(why, "no dictionaries") {
		t.Errorf("why = %q, want it to say the dictionaries are missing", why)
	}
	if !strings.Contains(why, "/usr/share/hunspell") {
		t.Errorf("why = %q, want it to say where it searched", why)
	}
}

// Why is silent when ready.
func TestWhyIsSilentWhenReady(t *testing.T) {
	t.Parallel()

	ready := Availability{
		Engine:       Engine{Command: "hunspell", Path: "/usr/bin/hunspell"},
		Dictionaries: []Dictionary{{Tag: "en_US"}},
	}
	if why := ready.Why("arch"); why != "" {
		t.Errorf("why = %q, want silence", why)
	}
}

func TestInstallHintFollowsTheDistro(t *testing.T) {
	t.Parallel()

	for distro, want := range map[string]string{
		"arch":     "pacman -S",
		"ubuntu":   "apt install",
		"fedora":   "dnf install",
		"alpine":   "apk add",
		"darwin":   "brew install",
		"":         "your package manager",
		"plan9":    "your package manager",
		"opensuse": "zypper install",
	} {
		if got := installHint(distro, "hunspell"); !strings.Contains(got, want) {
			t.Errorf("installHint(%q) = %q, want it to mention %q", distro, got, want)
		}
	}
}

func TestOsReleaseID(t *testing.T) {
	t.Parallel()

	const file = "NAME=\"Arch Linux\"\nPRETTY_NAME=\"Arch Linux\"\nID=arch\nBUILD_ID=rolling\n"
	if got := osReleaseID(file); got != "arch" {
		t.Errorf("ID = %q, want arch", got)
	}
	if got := osReleaseID("ID=\"ubuntu\"\n"); got != "ubuntu" {
		t.Errorf("quoted ID = %q, want ubuntu", got)
	}
	if got := osReleaseID("NAME=whatever\n"); got != "" {
		t.Errorf("ID = %q, want empty when there is none", got)
	}
}

// SearchPaths puts kith's own directory first, then DICPATH. Not parallel (Setenv).
func TestSearchPathsPutOursFirst(t *testing.T) {
	t.Setenv("DICPATH", "/custom/dicts")
	got := SearchPaths("/home/someone/.local/share/kith")
	if len(got) < 3 {
		t.Fatalf("got %v, want at least ours, DICPATH and a system directory", got)
	}
	if !strings.HasSuffix(got[0], filepath.Join("kith", "hunspell")) {
		t.Errorf("first path = %q, want kith's own", got[0])
	}
	if got[1] != "/custom/dicts" {
		t.Errorf("second path = %q, want DICPATH", got[1])
	}
}
