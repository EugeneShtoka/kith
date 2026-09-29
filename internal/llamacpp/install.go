package llamacpp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/EugeneShtoka/kith/internal/llamacpp/models"
)

// Model downloads are pinned to one upstream commit, SHA-256 checked (a mismatch
// installs nothing), and never automatic. Unlike dictionaries they stream to a temp
// file in the destination, hashing as they go, and are renamed only once verified.

// ErrUnknownModel is returned for a tag the manifest does not carry.
var ErrUnknownModel = errors.New("llamacpp: no such model")

// ErrCorrupt is returned when a download does not match the manifest. Not transient.
var ErrCorrupt = errors.New("llamacpp: downloaded file does not match its recorded checksum")

// Install downloads one model into dir, verifying it before it lands under its own name.
func Install(ctx context.Context, client *http.Client, tag, dir string) (models.Source, error) {
	src, ok := models.SourceFor(tag)
	if !ok {
		return models.Source{}, fmt.Errorf("%w: %q", ErrUnknownModel, tag)
	}
	return src, install(ctx, client, models.Base(), src, dir)
}

// install is Install with the origin and entry decided, for tests.
func install(ctx context.Context, client *http.Client, base string, src models.Source, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("llamacpp: make %s: %w", dir, err)
	}
	final := filepath.Join(dir, src.Filename())

	// In the destination directory so the rename is atomic.
	tmp, err := os.CreateTemp(dir, src.Tag+".part-*")
	if err != nil {
		return fmt.Errorf("llamacpp: make a temporary file in %s: %w", dir, err)
	}
	// A no-op after the successful rename; on failure, cleanup whose own errors add
	// nothing to the one returned.
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if err := download(ctx, client, base+src.File.Path, src.File, tmp); err != nil {
		return fmt.Errorf("llamacpp: %s: %w", src.File.Path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("llamacpp: finish writing %s: %w", tmp.Name(), err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { // #nosec G302 -- model weights are world-readable data
		return fmt.Errorf("llamacpp: set permissions on %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return fmt.Errorf("llamacpp: install %s: %w", final, err)
	}
	return nil
}

// download streams one file into w, hashing as it goes, and fails unless size and
// SHA-256 match.
func download(ctx context.Context, client *http.Client, url string, want models.File, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch: %s", resp.Status)
	}

	sum := sha256.New()
	// One byte over the limit so "too long" is detectable.
	written, err := io.Copy(io.MultiWriter(w, sum), io.LimitReader(resp.Body, want.Size+1))
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if written != want.Size {
		return fmt.Errorf("%w: %d bytes, expected %d", ErrCorrupt, written, want.Size)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want.SHA256 {
		return fmt.Errorf("%w: sha256 %s, expected %s", ErrCorrupt, got, want.SHA256)
	}
	return nil
}

// DefaultClient allows an hour: 369 MB on a slow link is a long honest download.
func DefaultClient() *http.Client { return &http.Client{Timeout: time.Hour} }
