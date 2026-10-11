// Package media caches timeline images: originals as files an external viewer can open,
// and downsampled renders so reopening a room doesn't re-decode. A miss just redoes the work.
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// DefaultMaxBytes is how much the cache keeps when nothing says otherwise.
const DefaultMaxBytes int64 = 1 << 30

// Cache is a directory of attachment bytes and scaled renders.
type Cache struct {
	dir string
	// renders is a subdirectory, so the root holds only attachments.
	renders string
	max     int64
}

// New opens (creating if needed) a cache under dir, keeping at most max bytes.
func New(dir string, max int64) (*Cache, error) {
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("media: no cache directory: %w", err)
		}
		dir = filepath.Join(base, "kith", "media")
	}
	renders := filepath.Join(dir, "renders")
	if err := os.MkdirAll(renders, 0o700); err != nil {
		return nil, fmt.Errorf("media: create %s: %w", renders, err)
	}
	if max == 0 {
		max = DefaultMaxBytes
	}
	return &Cache{dir: dir, renders: renders, max: max}, nil
}

// Dir is where the cache keeps its files.
func (c *Cache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// Path is where an attachment's bytes belong, present or not.
func (c *Cache) Path(event domain.EventID, name, mimeType string) string {
	if c == nil {
		return ""
	}
	return filepath.Join(c.dir, key(event)+extension(name, mimeType))
}

// Read returns an attachment's cached bytes, and whether they were there.
func (c *Cache) Read(event domain.EventID, name, mimeType string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	return readNonEmpty(c.Path(event, name, mimeType))
}

// Write stores an attachment's bytes and returns their path. A failure is non-fatal:
// the caller already has the bytes.
func (c *Cache) Write(event domain.EventID, name, mimeType string, data []byte) (string, error) {
	if c == nil || len(data) == 0 {
		return "", nil
	}
	path := c.Path(event, name, mimeType)
	if err := write(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// Render returns what was drawn for one cell box, and whether it was there.
func (c *Cache) Render(event domain.EventID, cols, rows int) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	return readNonEmpty(c.renderPath(event, cols, rows))
}

func readNonEmpty(path string) ([]byte, bool) {
	data, err := os.ReadFile(path) // #nosec G304 -- a file under the media cache directory, named by the cache
	return data, err == nil && len(data) > 0
}

// PutRender stores what was drawn for one cell box.
func (c *Cache) PutRender(event domain.EventID, cols, rows int, drawn []byte) error {
	if c == nil || len(drawn) == 0 {
		return nil
	}
	return write(c.renderPath(event, cols, rows), drawn)
}

// renderPath is the render for one cell box, under renders/.
func (c *Cache) renderPath(event domain.EventID, cols, rows int) string {
	return filepath.Join(c.renders, fmt.Sprintf("%s@%dx%d.rows", key(event), cols, rows))
}

func key(event domain.EventID) string {
	sum := sha256.Sum256([]byte(event))
	return hex.EncodeToString(sum[:])
}

// Trim deletes the least recently used files until the cache is within its limit,
// and reports how many bytes it freed.
func (c *Cache) Trim() (freed int64, err error) {
	if c == nil || c.max < 0 {
		return 0, nil
	}
	type item struct {
		path string
		size int64
		age  int64
	}
	var files []item
	var total int64
	for _, dir := range []string{c.dir, c.renders} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return 0, fmt.Errorf("media: read %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue // removed since ReadDir: nothing to count
			}
			files = append(files, item{filepath.Join(dir, e.Name()), info.Size(), info.ModTime().UnixNano()})
			total += info.Size()
		}
	}
	if total <= c.max {
		return 0, nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].age < files[j].age })
	for _, f := range files {
		if total <= c.max {
			break
		}
		if err := os.Remove(f.path); err != nil {
			continue // try the next oldest; a file that will not go stays counted
		}
		total -= f.size
		freed += f.size
	}
	return freed, nil
}

// Touch marks a file used now for Trim's LRU; a failure only risks early eviction.
func (c *Cache) Touch(path string) {
	if c == nil || path == "" {
		return
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// write replaces path atomically: a miss is retried, a torn file would be believed.
func write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("media: create temp: %w", err)
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, 0o600)
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name) // cleanup on the error path; err is the report
		return fmt.Errorf("media: write %s: %w", path, err)
	}
	return nil
}

// extension picks a file extension so a viewer knows the type.
func extension(name, mimeType string) string {
	if ext := filepath.Ext(name); usableExtension(ext) {
		return strings.ToLower(ext)
	}
	exts, err := mime.ExtensionsByType(mimeType)
	if err != nil || len(exts) == 0 {
		return ".bin"
	}
	// The conventional spelling first, then whatever the table offers.
	for _, preferred := range conventional {
		for _, ext := range exts {
			if strings.EqualFold(ext, preferred) {
				return preferred
			}
		}
	}
	for _, ext := range exts {
		if usableExtension(ext) {
			return strings.ToLower(ext)
		}
	}
	return ".bin"
}

// conventional extensions are preferred over a MIME table's other spellings.
var conventional = []string{
	".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".heic", ".tif", ".mp4", ".pdf",
	".ogg", ".oga", ".opus", ".mp3", ".m4a", ".aac", ".wav", ".flac", ".webm",
}

// usableExtension reports whether ext is a short, plain, dotted suffix.
func usableExtension(ext string) bool {
	if len(ext) < 2 || len(ext) > 6 || !strings.HasPrefix(ext, ".") {
		return false
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
