// Package backup writes kith's history to one encrypted file and puts it back, on this
// machine or a new one. A backup holds the cache, the config and its commands, the
// local records nothing re-fetches (what an assistant sent, the send-later queue, the
// mesh store with the paired devices) and the picture cache (WhatsApp and Signal
// pictures cannot be fetched again). It never holds a network session or a keyring
// key: a restored machine logs in afresh.
//
// The file is an age file around a tar, encrypted to the user's backup key (key.go),
// so `age -d -i key` and `tar` read it without kith. The cache and the mesh store in it
// are SQLite copies encrypted as they are on disk (internal/sealeddb), under a key made
// for this backup alone and kept in its manifest, so neither is ever written to disk in
// the clear, not even while the backup is made; a restore writes them under the
// restored machine's own key.
package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
)

// The names inside a backup. They carry no instance: a restore writes each where the
// restored config puts it on that machine. The cache comes last, after the config
// that says where it goes.
const (
	manifestName = "kith-backup.json"
	configName   = "config/config.toml"
	commandsDir  = "config/commands/"
	ledgerName   = "state/agent-sends.jsonl"
	scheduleName = "state/scheduled.toml"
	mediaPrefix  = "media/"
	meshName     = "mesh.db"
	cacheName    = "cache.db"
)

// snapshotPattern and meshPattern name the copies of the cache and the mesh store a
// backup makes beside them while it runs.
const (
	snapshotPattern = ".backup-cache-*.db"
	meshPattern     = ".backup-mesh-*.db"
)

// format is the layout this kith writes: encrypted to a backup key, the cache and the
// mesh store in it encrypted under the manifest's CacheKey. It reads oldestFormat on,
// a backup before 4 holding no mesh store.
const (
	format       = 4
	oldestFormat = 3
)

// manifest is a backup's first entry: its layout, when and by what it was made, every
// file after it, so a restore can tell a whole backup from a cut one, the key (hex)
// the cache and the mesh store in it are encrypted under, and the public backup key it
// was made to.
type manifest struct {
	Format    int       `json:"format"`
	Created   time.Time `json:"created"`
	Kith      string    `json:"kith"`
	Files     []string  `json:"files"`
	CacheKey  string    `json:"cache_key"`
	Recipient string    `json:"recipient"`
}

// Source is what a backup is made of: the config file at ConfigPath, its contents,
// the storage it names, the key the cache there is encrypted under, and the picture
// cache, left out when MediaDir is empty.
type Source struct {
	ConfigPath string
	Config     config.Config
	Storage    domain.Storage
	CacheKey   []byte
	MediaDir   string
}

// Entry is one file in a backup, and its size; the pictures are one Entry of Count
// files.
type Entry struct {
	Name  string
	Size  int64
	Count int
}

// Made is a backup written: where, what went in, and what was left out and why.
type Made struct {
	Path    string
	Entries []Entry
	Left    []string
}

// file is a file to put in a backup: its name there, and where it is.
type file struct {
	name, path string
}

// Create writes a backup of src to out, a file that must not exist, encrypted to the
// backup key to. The cache is copied as it stands, so the daemon may keep running. A
// failed or interrupted backup leaves no file at out.
func Create(ctx context.Context, src Source, out string, to *PublicKey) (Made, error) {
	if to == nil {
		return Made{}, ErrNoKey
	}
	if _, err := os.Lstat(out); err == nil {
		return Made{}, fmt.Errorf("backup: %s exists; name another file", out)
	}
	files, left, err := smallFiles(src)
	if err != nil {
		return Made{}, err
	}
	key := make([]byte, db.KeyLen)
	if _, randErr := rand.Read(key); randErr != nil {
		return Made{}, fmt.Errorf("backup: %w", randErr)
	}
	meshCopy, err := snapshotMesh(ctx, src.Storage, src.CacheKey, key)
	if err != nil {
		return Made{}, err
	}
	if meshCopy != "" {
		defer func() { _ = os.Remove(meshCopy) }()
		files = append(files, file{name: meshName, path: meshCopy})
	}
	media, err := mediaFiles(src.MediaDir)
	if err != nil {
		return Made{}, err
	}
	files = append(files, media...)
	snapshot, err := snapshotCache(ctx, src.Storage, src.CacheKey, key)
	if err != nil {
		return Made{}, err
	}
	defer func() { _ = os.Remove(snapshot) }()
	files = append(files, file{name: cacheName, path: snapshot})

	if mkErr := os.MkdirAll(filepath.Dir(out), 0o700); mkErr != nil {
		return Made{}, fmt.Errorf("backup: %w", mkErr)
	}
	entries, err := writeArchive(ctx, out, to, files, key)
	if err != nil {
		return Made{}, err
	}
	return Made{Path: out, Entries: entries, Left: left}, nil
}

// smallFiles is everything but the cache that goes in a backup of src, and what was
// left out and why. The config must be there; the rest only if it is.
func smallFiles(src Source) (files []file, left []string, err error) {
	if _, statErr := os.Stat(src.ConfigPath); statErr != nil {
		return nil, nil, fmt.Errorf("backup: the config: %w", statErr)
	}
	files = append(files, file{name: configName, path: src.ConfigPath})
	commands, why, err := commandFiles(src)
	if err != nil {
		return nil, nil, err
	}
	files = append(files, commands...)
	if why != "" {
		left = append(left, why)
	}
	for _, f := range []file{
		{name: ledgerName, path: src.Storage.LedgerPath()},
		{name: scheduleName, path: src.Storage.SchedulePath()},
	} {
		switch info, statErr := os.Stat(f.path); {
		case errors.Is(statErr, fs.ErrNotExist):
		case statErr != nil:
			return nil, nil, fmt.Errorf("backup: %w", statErr)
		case info.Mode().IsRegular():
			files = append(files, f)
		}
	}
	return files, left, nil
}

// commandFiles is the commands beside the config, or why none are taken: a commands
// folder that is a link belongs to whatever made the link (a home-manager setup, say),
// and one configured elsewhere is not kith's to copy.
func commandFiles(src Source) (files []file, left string, err error) {
	dir := filepath.Join(filepath.Dir(src.ConfigPath), "commands")
	if src.Config.Commands.Dir != "" {
		return nil, "commands: [commands] dir is " + src.Config.Commands.Dir + ", not the config's folder; not copied", nil
	}
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "", nil
	case err != nil:
		return nil, "", fmt.Errorf("backup: %w", err)
	case info.Mode()&fs.ModeSymlink != 0:
		target, _ := os.Readlink(dir)
		return nil, "commands: " + dir + " is a link to " + target + "; not copied", nil
	case !info.IsDir():
		return nil, "", nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", fmt.Errorf("backup: %w", err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			files = append(files, file{name: commandsDir + e.Name(), path: filepath.Join(dir, e.Name())})
		}
	}
	return files, "", nil
}

// mediaFiles is the picture cache's originals in dir: its top level, where the
// attachments are. Renders and pictures waiting to be sent below it are made again.
func mediaFiles(dir string) ([]file, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: the pictures: %w", err)
	}
	var files []file
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			files = append(files, file{name: mediaPrefix + e.Name(), path: filepath.Join(dir, e.Name())})
		}
	}
	return files, nil
}

// snapshotCache copies the cache (under cacheKey) beside it, the same disk and as
// private, encrypted under key, returning the copy's path.
func snapshotCache(ctx context.Context, storage domain.Storage, cacheKey, key []byte) (string, error) {
	tmp, err := os.CreateTemp(storage.DataDir, snapshotPattern)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("backup: %w", err)
	}
	if err := db.Snapshot(ctx, storage.CachePath(), cacheKey, path, key); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("backup: copy the cache: %w", err)
	}
	return path, nil
}

// snapshotMesh copies the mesh store (under the cache's key) beside it, encrypted under
// key, returning the copy's path; "" when there is no store (kithd has not read its
// roster yet).
func snapshotMesh(ctx context.Context, storage domain.Storage, cacheKey, key []byte) (string, error) {
	if _, err := os.Stat(storage.MeshPath()); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	tmp, err := os.CreateTemp(storage.DataDir, meshPattern)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("backup: %w", err)
	}
	if err := mesh.Copy(ctx, storage.MeshPath(), cacheKey, path, key); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("backup: copy the mesh store: %w", err)
	}
	return path, nil
}

// writeArchive writes files to out through a partial file renamed into place once
// whole, so an interrupted backup never leaves a file that looks complete. cacheKey is
// what the cache among them is encrypted under.
func writeArchive(ctx context.Context, out string, to *PublicKey, files []file, cacheKey []byte) ([]Entry, error) {
	partial, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".partial-*")
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	done := false
	defer func() {
		if !done {
			_ = partial.Close()
			_ = os.Remove(partial.Name())
		}
	}()
	sealed, err := age.Encrypt(partial, to)
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	tw := tar.NewWriter(sealed)
	entries, err := writeEntries(ctx, tw, files, cacheKey, to)
	if err != nil {
		return nil, err
	}
	for _, step := range []func() error{tw.Close, sealed.Close, partial.Sync, partial.Close} {
		if err := step(); err != nil {
			return nil, fmt.Errorf("backup: write %s: %w", out, err)
		}
	}
	if err := os.Rename(partial.Name(), out); err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	done = true
	return entries, nil
}

// writeEntries writes the manifest, then each file. The pictures are reported as one
// entry, however many there are.
func writeEntries(ctx context.Context, tw *tar.Writer, files []file, cacheKey []byte, to *PublicKey) ([]Entry, error) {
	m := manifest{
		Format: format, Created: time.Now().UTC(), Kith: buildinfo.String("kith"),
		CacheKey: hex.EncodeToString(cacheKey), Recipient: to.String(),
	}
	for _, f := range files {
		m.Files = append(m.Files, f.name)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	if err := writeEntry(ctx, tw, manifestName, 0o600, time.Now(), int64(len(body)), bytes.NewReader(body)); err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(files))
	var pictures Entry
	for _, f := range files {
		size, err := copyFile(ctx, tw, f)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(f.name, mediaPrefix) {
			pictures.Name, pictures.Size, pictures.Count = mediaPrefix, pictures.Size+size, pictures.Count+1
			continue
		}
		if f.name == cacheName && pictures.Count > 0 {
			entries = append(entries, pictures)
		}
		entries = append(entries, Entry{Name: f.name, Size: size})
	}
	return entries, nil
}

// copyFile writes one file into the archive, keeping its permission bits and its time
// (which the picture cache trims by).
func copyFile(ctx context.Context, tw *tar.Writer, f file) (int64, error) {
	in, err := os.Open(f.path) // #nosec G304 -- a file of this config's own storage
	if err != nil {
		return 0, fmt.Errorf("backup: %w", err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return 0, fmt.Errorf("backup: %w", err)
	}
	return info.Size(), writeEntry(ctx, tw, f.name, int64(info.Mode().Perm()), info.ModTime(), info.Size(), in)
}

// writeEntry writes one tar entry of size bytes from r.
func writeEntry(ctx context.Context, tw *tar.Writer, name string, mode int64, at time.Time, size int64, r io.Reader) error {
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: size, ModTime: at}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: %s: %w", name, err)
	}
	if err := copyCtx(ctx, tw, r); err != nil {
		return fmt.Errorf("backup: %s: %w", name, err)
	}
	return nil
}

// copyCtx is io.Copy that stops between chunks once ctx is done, so an interrupt
// ends a long copy.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader) error {
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("interrupted: %w", err)
		}
		read, rerr := src.Read(buf)
		if read > 0 {
			if _, werr := dst.Write(buf[:read]); werr != nil {
				return fmt.Errorf("write: %w", werr)
			}
		}
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("read: %w", rerr)
		}
	}
}
