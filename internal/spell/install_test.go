package spell

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fixture serves a set of paths and returns the base URL plus a Source describing
// them truthfully.
func fixture(t *testing.T, aff, dic, lic []byte) (string, Source, *http.Client) {
	t.Helper()

	files := map[string][]byte{
		"x/x.aff":      aff,
		"x/x.dic":      dic,
		"x/README.txt": lic,
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
	src := Source{
		Tag:     "xx_XX",
		Aff:     File{Path: "x/x.aff", SHA256: sum(aff), Size: int64(len(aff))},
		Dic:     File{Path: "x/x.dic", SHA256: sum(dic), Size: int64(len(dic))},
		License: File{Path: "x/README.txt", SHA256: sum(lic), Size: int64(len(lic))},
	}
	return srv.URL + "/", src, srv.Client()
}

func TestInstallWritesTheDictionaryAndItsLicense(t *testing.T) {
	t.Parallel()

	base, src, client := fixture(t, []byte("SET UTF-8\n"), []byte("2\nalpha\nbeta\n"), []byte("GPL\n"))
	dir := t.TempDir()

	if err := install(t.Context(), client, base, src, dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Installed under the *tag*, not under whatever upstream calls the file. German is
	// "de_DE_frami" up there, and nobody should have to know that to load it.
	for name, want := range map[string]string{
		"xx_XX.aff":     "SET UTF-8\n",
		"xx_XX.dic":     "2\nalpha\nbeta\n",
		"xx_XX.LICENSE": "GPL\n",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// The check the whole pinning exercise exists for. A file that is not what the
// manifest recorded is refused, and — the part that matters — **nothing is written**,
// so there is no half-installed pair for the engine to load and be quietly wrong with.
func TestInstallRefusesAndWritesNothingOnAHashMismatch(t *testing.T) {
	t.Parallel()

	base, src, client := fixture(t, []byte("SET UTF-8\n"), []byte("1\nalpha\n"), []byte("GPL\n"))
	// The manifest now disagrees with what the server will serve.
	src.Dic.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	dir := t.TempDir()

	err := install(t.Context(), client, base, src, dir)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("install = %v, want ErrCorrupt", err)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		t.Errorf("%d files were written despite the refusal: %v", len(entries), entries)
	}
}

// Size is checked before the hash so a server answering a 3 MB request with 3 GB
// cannot be made to exhaust memory while we wait to find out its hash is wrong.
func TestInstallRefusesAFileOfTheWrongLength(t *testing.T) {
	t.Parallel()

	t.Run("longer than recorded", func(t *testing.T) {
		t.Parallel()
		base, src, client := fixture(t, []byte("SET UTF-8\n"), []byte("1\nalpha\n"), []byte("GPL\n"))
		src.Dic.Size = 2 // the server will send more than this
		if err := install(t.Context(), client, base, src, t.TempDir()); !errors.Is(err, ErrCorrupt) {
			t.Errorf("install = %v, want ErrCorrupt", err)
		}
	})

	t.Run("shorter than recorded", func(t *testing.T) {
		t.Parallel()
		base, src, client := fixture(t, []byte("SET UTF-8\n"), []byte("1\nalpha\n"), []byte("GPL\n"))
		src.Dic.Size = 10_000
		if err := install(t.Context(), client, base, src, t.TempDir()); !errors.Is(err, ErrCorrupt) {
			t.Errorf("install = %v, want ErrCorrupt", err)
		}
	})
}

// A missing file upstream is a plain failure, not a corruption: the pin names a commit
// that should have it, so this means the pin is wrong rather than the network.
func TestInstallReportsAMissingFile(t *testing.T) {
	t.Parallel()

	base, src, client := fixture(t, []byte("SET UTF-8\n"), []byte("1\nalpha\n"), []byte("GPL\n"))
	src.Aff.Path = "x/not-there.aff"
	if err := install(t.Context(), client, base, src, t.TempDir()); err == nil {
		t.Error("install succeeded with a file that is not upstream")
	}
}

// A dictionary with no license upstream installs its two files and does not invent a
// third.
func TestInstallWithoutALicense(t *testing.T) {
	t.Parallel()

	base, src, client := fixture(t, []byte("SET UTF-8\n"), []byte("1\nalpha\n"), []byte("GPL\n"))
	src.License = File{}
	dir := t.TempDir()

	if err := install(t.Context(), client, base, src, dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("wrote %d files, want the pair alone", len(entries))
	}
}

func TestInstallRejectsAnUnknownTag(t *testing.T) {
	t.Parallel()

	_, err := Install(t.Context(), DefaultClient(), "zz_ZZ", t.TempDir())
	if !errors.Is(err, ErrUnknownDictionary) {
		t.Errorf("Install = %v, want ErrUnknownDictionary", err)
	}
}

// The committed manifest has every entry keyed by its tag and every file hashed and sized.
func TestGeneratedManifestIsWellFormed(t *testing.T) {
	t.Parallel()

	if len(dictionarySources) == 0 {
		t.Fatal("the generated manifest is empty; run `make dict-manifest`")
	}
	for tag, src := range dictionarySources {
		if src.Tag != tag {
			t.Errorf("%s is keyed as %q but names itself %q", tag, tag, src.Tag)
		}
		for what, f := range map[string]File{"aff": src.Aff, "dic": src.Dic} {
			switch {
			case f.Path == "":
				t.Errorf("%s %s has no path", tag, what)
			case len(f.SHA256) != 64:
				t.Errorf("%s %s has no usable sha256 (%q)", tag, what, f.SHA256)
			case f.Size <= 0:
				t.Errorf("%s %s has size %d", tag, what, f.Size)
			}
		}
		// A license is optional upstream, but a half-filled entry is a generator bug.
		if (src.License.Path == "") != (src.License.SHA256 == "") {
			t.Errorf("%s license is half-recorded: %+v", tag, src.License)
		}
	}
	// The pin has to be a real commit, or the URLs it builds are nonsense.
	if _, commit, _ := Pin(); len(commit) != 40 {
		t.Errorf("pinned commit %q is not a git SHA", commit)
	}
}
