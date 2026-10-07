package media

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A deleted message's attachment is kept apart, in the cache's deleted/ folder: out of
// the way of the pictures a viewer is pointed at, and out of trimming (Trim reads the
// root and renders/ only), since what was deleted may not be fetchable again. Its
// drawings go: a deleted picture is not drawn in the timeline.

// deletedDir is the folder deleted messages' attachments are kept in.
func (c *Cache) deletedDir() string { return filepath.Join(c.dir, "deleted") }

// AsidePath is where a deleted message's attachment is kept, whether or not it is there.
func (c *Cache) AsidePath(event domain.EventID, name, mimeType string) string {
	if c == nil {
		return ""
	}
	return filepath.Join(c.deletedDir(), key(event)+extension(name, mimeType))
}

// SetAside moves a deleted message's attachment into deleted/, if the cache holds it,
// and drops its drawings; it answers where the attachment is kept now.
func (c *Cache) SetAside(event domain.EventID, name, mimeType string) (string, error) {
	if c == nil {
		return "", nil
	}
	aside := c.AsidePath(event, name, mimeType)
	if err := os.MkdirAll(c.deletedDir(), 0o700); err != nil {
		return aside, fmt.Errorf("media: create %s: %w", c.deletedDir(), err)
	}
	if err := os.Rename(c.Path(event, name, mimeType), aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return aside, fmt.Errorf("media: set aside %s: %w", event, err)
	}
	drawings, _ := filepath.Glob(filepath.Join(c.renders, key(event)+"*"))
	for _, d := range drawings {
		_ = os.Remove(d) // a drawing left behind is never shown: nothing draws it
	}
	return aside, nil
}

// ReadAside returns a deleted message's kept attachment, and whether it was there.
func (c *Cache) ReadAside(event domain.EventID, name, mimeType string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	return readNonEmpty(c.AsidePath(event, name, mimeType))
}

// WriteAside keeps a deleted message's attachment, fetched after it was deleted.
func (c *Cache) WriteAside(event domain.EventID, name, mimeType string, data []byte) (string, error) {
	if c == nil || len(data) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(c.deletedDir(), 0o700); err != nil {
		return "", fmt.Errorf("media: create %s: %w", c.deletedDir(), err)
	}
	path := c.AsidePath(event, name, mimeType)
	if err := write(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// Forget removes everything the cache holds of a message's attachment, wherever it
// is: its bytes, kept aside or not, and its drawings. [display.deleted] keep off
// erases a deleted message, and this is its attachment's share.
func (c *Cache) Forget(event domain.EventID) {
	if c == nil {
		return
	}
	for _, pattern := range []string{
		filepath.Join(c.dir, key(event)+".*"),
		filepath.Join(c.deletedDir(), key(event)+".*"),
		filepath.Join(c.renders, key(event)+"*"),
	} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			_ = os.Remove(f) // one left behind is never shown: nothing points at it
		}
	}
}
