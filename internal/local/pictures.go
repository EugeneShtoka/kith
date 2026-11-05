package local

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
	"strings"
	"time"
)

// An identity's own picture is a file kithd keeps in its data dir (pictures/<hash>), named
// by the identity as "file:<hash>". The cache is what pictures are read from, so each
// named one is put there too, under that name, as UseFaces sets the identities: at start,
// on reload, and when it is saved; an emptied cache has them back by the next start.

// customPicturePrefix starts an identity's picture that is a file kithd keeps.
const customPicturePrefix = "file:"

// customPictureBytes bounds a saved picture.
const customPictureBytes = 4 << 20

// errNotAPicture refuses a file that is no image.
var errNotAPicture = errors.New("not a picture")

// SavePicture keeps the image at path as a picture an identity can name, returning its name.
func (s *Service) SavePicture(ctx context.Context, path string) (string, error) {
	data, err := readPicture(path)
	if err != nil {
		return "", fmt.Errorf("local: save a picture: %w", err)
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])
	dir := s.picturesDir()
	if dir == "" {
		return "", errors.New("local: save a picture: no data dir")
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("local: save a picture: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".saving-*")
	if err != nil {
		return "", fmt.Errorf("local: save a picture: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone once renamed
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}
	if werr != nil {
		return "", fmt.Errorf("local: save a picture: %w", werr)
	}
	s.keepCustom(ctx, name, data)
	return customPicturePrefix + name, nil
}

// readPicture is the file at path when it is an image no larger than customPictureBytes.
func readPicture(path string) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names what failed
	}
	defer func() { _ = f.Close() }() // read only
	data, err := io.ReadAll(io.LimitReader(f, customPictureBytes+1))
	switch {
	case err != nil:
		return nil, err //nolint:wrapcheck // the caller names what failed
	case len(data) > customPictureBytes:
		return nil, fmt.Errorf("a picture of over %d MB", customPictureBytes>>20)
	case !strings.HasPrefix(http.DetectContentType(data), "image/"):
		return nil, errNotAPicture
	}
	return data, nil
}

// picturesDir is where saved pictures are kept, "" with no data dir.
func (s *Service) picturesDir() string {
	s.spell.mu.Lock()
	defer s.spell.mu.Unlock()
	if s.spell.data == "" {
		return ""
	}
	return filepath.Join(s.spell.data, "pictures")
}

// keepCustom puts a saved picture into the cache under its name, as the identities read it.
func (s *Service) keepCustom(ctx context.Context, name string, data []byte) {
	if s.cache == nil {
		return
	}
	if err := s.cache.PersonPictures().SetPicture(ctx, customPicturePrefix+name, name, data, time.Now()); err != nil {
		s.log().Warn("keep a saved picture failed", "picture", name, "err", err)
	}
}

// keepCustoms puts every saved picture faces name into the cache; one whose file is gone
// is left out, and the identity's chain goes on past it.
func (s *Service) keepCustoms(faces map[string][]string) {
	dir := s.picturesDir()
	done := map[string]bool{}
	for _, chain := range faces {
		for _, id := range chain {
			name, custom := strings.CutPrefix(id, customPicturePrefix)
			if !custom || done[name] || dir == "" {
				continue
			}
			done[name] = true
			data, err := readPicture(filepath.Join(dir, filepath.Base(name)))
			if err != nil {
				s.log().Warn("read a saved picture failed", "picture", name, "err", err)
				continue
			}
			s.keepCustom(context.Background(), name, data)
		}
	}
}
