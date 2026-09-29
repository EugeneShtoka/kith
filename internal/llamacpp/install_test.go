package llamacpp

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

	"github.com/EugeneShtoka/kith/internal/llamacpp/models"
)

// fixture serves one file and returns the base URL plus a Source describing it
// truthfully.
func fixture(t *testing.T, weights []byte) (string, models.Source, *http.Client) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/m/model.gguf" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(weights)
	}))
	t.Cleanup(srv.Close)

	sum := sha256.Sum256(weights)
	src := models.Source{
		Tag:  "xx-1b",
		Name: "Example 1B",
		File: models.File{
			Path:   "m/model.gguf",
			SHA256: hex.EncodeToString(sum[:]),
			Size:   int64(len(weights)),
		},
	}
	return srv.URL + "/", src, srv.Client()
}

func TestInstallWritesTheWeightsUnderTheirTag(t *testing.T) {
	t.Parallel()

	base, src, client := fixture(t, []byte("GGUF\x00weights"))
	dir := t.TempDir()

	if err := install(t.Context(), client, base, src, dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Under the tag, not under whatever upstream calls the file: the thing a person
	// types and the thing on disk should be the same word.
	got, err := os.ReadFile(filepath.Join(dir, "xx-1b.gguf"))
	if err != nil {
		t.Fatalf("read the installed model: %v", err)
	}
	if string(got) != "GGUF\x00weights" {
		t.Errorf("installed %q, want the bytes that were served", got)
	}
}

// A mismatch is a refusal, and the refusal has to be complete: this is the whole reason
// the manifest carries a hash, so a model that is *nearly* right must not be loadable.
func TestInstallRefusesWeightsThatDoNotMatchTheirHash(t *testing.T) {
	t.Parallel()

	base, src, client := fixture(t, []byte("GGUF\x00tampered"))
	src.File.SHA256 = strings.Repeat("0", 64)
	dir := t.TempDir()

	err := install(t.Context(), client, base, src, dir)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("install: %v, want ErrCorrupt", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		// Nothing at all, not even the partial download: a directory holding a
		// half-verified model is a server that starts and then fails to parse it.
		t.Errorf("refused the model and left %d files behind", len(entries))
	}
}

// The size is checked before the hash and on its own, so a server that answers a 12-byte
// request with a stream cannot be waited out to find out its hash is wrong.
func TestInstallRefusesAFileLongerThanTheManifestSaid(t *testing.T) {
	t.Parallel()

	base, src, client := fixture(t, []byte("GGUF\x00much longer than declared"))
	src.File.Size = 4
	dir := t.TempDir()

	if err := install(t.Context(), client, base, src, dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("install: %v, want ErrCorrupt", err)
	}
}

func TestInstallRefusesAModelTheManifestDoesNotCarry(t *testing.T) {
	t.Parallel()

	if _, err := Install(t.Context(), http.DefaultClient, "no-such-model", t.TempDir()); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("install: %v, want ErrUnknownModel", err)
	}
}
