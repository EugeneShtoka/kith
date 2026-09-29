package spell

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
	"sort"
	"time"
)

// Dictionary downloads are pinned to one upstream commit, SHA-256 checked (a mismatch
// installs nothing), never automatic, and carry their license alongside.

// File is one downloadable file and the hash it must have.
type File struct {
	// Path is relative to dictionaryBase.
	Path   string
	SHA256 string
	Size   int64
}

// Source is a dictionary that can be installed, named by the tag it installs as.
type Source struct {
	Tag     string
	Aff     File
	Dic     File
	License File
}

// Bytes is the download size.
func (s Source) Bytes() int64 { return s.Aff.Size + s.Dic.Size + s.License.Size }

// ErrUnknownDictionary is returned for a tag the manifest does not carry.
var ErrUnknownDictionary = errors.New("spell: no such dictionary")

// ErrCorrupt is returned when a download does not match the manifest. Not transient.
var ErrCorrupt = errors.New("spell: downloaded file does not match its recorded checksum")

// Installable lists the dictionaries that can be fetched, sorted.
func Installable() []string {
	out := make([]string, 0, len(dictionarySources))
	for tag := range dictionarySources {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// SourceFor returns the manifest entry for a tag.
func SourceFor(tag string) (Source, bool) {
	s, ok := dictionarySources[tag]
	return s, ok
}

// Pin describes where dictionaries come from, for the confirmation prompt.
func Pin() (repo, commit, date string) {
	return dictionaryRepo, dictionaryCommit, dictionaryDate
}

// Install downloads one dictionary into dir. Every file is fetched and verified before
// any is written, so a half-installed dictionary cannot exist.
func Install(ctx context.Context, client *http.Client, tag, dir string) (Source, error) {
	src, ok := dictionarySources[tag]
	if !ok {
		return Source{}, fmt.Errorf("%w: %q", ErrUnknownDictionary, tag)
	}
	return src, install(ctx, client, dictionaryBase, src, dir)
}

// install is Install with the origin and entry decided, for tests.
func install(ctx context.Context, client *http.Client, base string, src Source, dir string) error {
	tag := src.Tag
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("spell: make %s: %w", dir, err)
	}

	// In memory: a dictionary is a few megabytes.
	want := map[string]File{
		tag + ".aff": src.Aff,
		tag + ".dic": src.Dic,
	}
	if src.License.Path != "" {
		want[tag+".LICENSE"] = src.License
	}
	got := make(map[string][]byte, len(want))
	for name, file := range want {
		body, err := fetchVerified(ctx, client, base, file)
		if err != nil {
			return fmt.Errorf("spell: %s: %w", file.Path, err)
		}
		got[name] = body
	}

	for name, body := range got {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o644); err != nil { // #nosec G306 -- a dictionary is world-readable data
			return fmt.Errorf("spell: write %s: %w", path, err)
		}
	}
	return nil
}

// fetchVerified downloads one file and returns it only if size and SHA-256 match. The
// size bounds the read.
func fetchVerified(ctx context.Context, client *http.Client, base string, file File) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+file.Path, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch: %s", resp.Status)
	}

	// One byte over the limit so "too long" is detectable.
	body, err := io.ReadAll(io.LimitReader(resp.Body, file.Size+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if int64(len(body)) != file.Size {
		return nil, fmt.Errorf("%w: %d bytes, expected %d", ErrCorrupt, len(body), file.Size)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != file.SHA256 {
		return nil, fmt.Errorf("%w: sha256 %s, expected %s",
			ErrCorrupt, hex.EncodeToString(sum[:]), file.SHA256)
	}
	return body, nil
}

// DefaultClient is the fetch client when a caller has no opinion.
func DefaultClient() *http.Client { return &http.Client{Timeout: 5 * time.Minute} }
