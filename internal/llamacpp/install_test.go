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

// fixture serves one file and returns its base URL and matching Source.
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
	// Named by tag, not by the upstream filename.
	got, err := os.ReadFile(filepath.Join(dir, "xx-1b.gguf"))
	if err != nil {
		t.Fatalf("read the installed model: %v", err)
	}
	if string(got) != "GGUF\x00weights" {
		t.Errorf("installed %q, want the bytes that were served", got)
	}
}

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
		// Not even the partial download may remain.
		t.Errorf("refused the model and left %d files behind", len(entries))
	}
}

// The size limit stops an endless stream before the hash is computed.
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
