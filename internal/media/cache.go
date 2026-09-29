// Package media is the client's own cache of timeline images: the originals as files
// an external viewer can open, and the decoded, downsampled renderings so reopening a
// room doesn't re-decode. Every miss just does the work; it is never required.
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
	// renders is a subdirectory, so the cache root holds nothing but the attachments
	// themselves.
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

// Path is where an attachment's own bytes belong, whether or not they are there yet.
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

// Write stores an attachment's bytes and returns the file they went to. A failure to
// write is reported but is not fatal to anything: the bytes are already in hand.
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

// renderPath is where the drawing for one cell box lives: under renders/, out of the
// way of anything that opens the cache looking for pictures.
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

// Touch records that a file was used now, so Trim keeps what is being looked at. A
// failure is ignored: the worst it costs is that something useful is evicted early.
func (c *Cache) Touch(path string) {
	if c == nil || path == "" {
		return
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// write puts data at path without ever leaving a half-written file where a reader could
// find one: a torn PNG is worse than a miss, because a miss is retried and a torn file
// is believed.
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

// extension is what to call the file, so a viewer knows what it is holding.
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

// conventional are the extensions people and desktops expect, preferred over the
// other spellings a MIME table lists for the same type.
var conventional = []string{
	".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".heic", ".tif", ".mp4", ".pdf",
	".ogg", ".oga", ".opus", ".mp3", ".m4a", ".aac", ".wav", ".flac", ".webm",
}

// usableExtension reports whether ext is a short, plain, dotted suffix — the shape a
// file extension has, as opposed to whatever else a filename might end in.
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
