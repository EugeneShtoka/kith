package backup

import (
	"archive/tar"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/mesh"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// keyName is the backup key's public half among the files a restore puts in place. No
// backup holds it as a file (checkEntry refuses it): it is the key the backup opened
// with.
const keyName = "backup.pub"

// damaged is ErrDamaged with why.
func damaged(why error) error { return fmt.Errorf("restore: %w: %w", ErrDamaged, why) }

// archiveReader reads the backup, telling its failures (a cut, an altered chunk)
// from the failures of writing what it read.
type archiveReader struct{ r io.Reader }

func (a archiveReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	switch {
	case err == nil:
		return n, nil
	case errors.Is(err, io.EOF):
		return n, io.EOF
	default:
		return n, badArchive{err}
	}
}

// badArchive is a failure reading the backup itself.
type badArchive struct{ err error }

func (b badArchive) Error() string { return b.err.Error() }
func (b badArchive) Unwrap() error { return b.err }

// read decrypts the backup and stages every file: the small ones in memory, the
// pictures and the cache beside where they go (the cache adopted). It reads to the
// very end, so a cut or altered backup fails here, before anything in place is touched.
func (r *restore) read(ctx context.Context, f io.Reader) error {
	plain, err := age.Decrypt(f, r.id)
	if err != nil {
		if _, ok := errors.AsType[*age.NoIdentityMatchError](err); ok {
			return fmt.Errorf("restore: %w", ErrWrongKey)
		}
		return damaged(err)
	}
	tr := tar.NewReader(plain)
	listed, key, err := readManifest(tr)
	if err != nil {
		return err
	}
	r.backupKey = key
	r.small, r.smallMode = map[string][]byte{}, map[string]fs.FileMode{}
	var seen []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return damaged(err)
		}
		if bad := checkEntry(hdr, seen, r.cache != ""); bad != nil {
			return bad
		}
		seen = append(seen, hdr.Name)
		if stageErr := r.stage(ctx, hdr, tr); stageErr != nil {
			return stageErr
		}
	}
	// age checks the last chunk only once it is read: a backup cut at its end is found here.
	if _, err := io.Copy(io.Discard, plain); err != nil {
		return damaged(err)
	}
	if !slices.Equal(seen, listed) || r.cache == "" {
		return damaged(fmt.Errorf("it holds %v; its list says %v", seen, listed))
	}
	return nil
}

// stage stages one entry: a picture or the cache beside where it goes, a small file in
// memory.
func (r *restore) stage(ctx context.Context, hdr *tar.Header, tr io.Reader) error {
	if name, ok := strings.CutPrefix(hdr.Name, mediaPrefix); ok {
		return r.stagePicture(ctx, name, hdr, tr)
	}
	if hdr.Name == cacheName {
		return r.stageCache(ctx, tr)
	}
	if hdr.Name == meshName {
		return r.stageMesh(ctx, tr)
	}
	body, err := io.ReadAll(io.LimitReader(tr, smallLimit+1))
	if err != nil {
		return damaged(err)
	}
	if len(body) > smallLimit {
		return damaged(fmt.Errorf("%s is larger than any kith writes", hdr.Name))
	}
	r.small[hdr.Name], r.smallMode[hdr.Name] = body, fs.FileMode(hdr.Mode&0o777) // #nosec G115 -- permission bits only
	r.order = append(r.order, hdr.Name)
	return nil
}

// readManifest reads the first entry: the list of every file after it, and the key
// the cache in the backup is encrypted under.
func readManifest(tr *tar.Reader) ([]string, []byte, error) {
	hdr, err := tr.Next()
	if err != nil {
		return nil, nil, damaged(err)
	}
	if hdr.Name != manifestName {
		return nil, nil, damaged(fmt.Errorf("it starts with %q", hdr.Name))
	}
	var m manifest
	if decErr := json.NewDecoder(io.LimitReader(tr, smallLimit)).Decode(&m); decErr != nil {
		return nil, nil, damaged(decErr)
	}
	if m.Format < oldestFormat || m.Format > format {
		return nil, nil, fmt.Errorf("restore: the backup's layout is %d; this kith reads %d to %d (it was made by %s)",
			m.Format, oldestFormat, format, m.Kith)
	}
	key, err := hex.DecodeString(m.CacheKey)
	if err != nil || len(key) != db.KeyLen {
		return nil, nil, damaged(errors.New("its cache's key is not a key"))
	}
	return m.Files, key, nil
}

// checkEntry refuses an entry this kith never writes, one seen before, or one after
// the cache, which comes last.
func checkEntry(hdr *tar.Header, seen []string, afterCache bool) error {
	name := hdr.Name
	command, isCommand := strings.CutPrefix(name, commandsDir)
	isCommand = isCommand && plainName(command)
	picture, isPicture := strings.CutPrefix(name, mediaPrefix)
	isPicture = isPicture && plainName(picture) && !strings.HasPrefix(picture, ".")
	switch {
	case hdr.Typeflag != tar.TypeReg:
		return damaged(fmt.Errorf("%q is not a file", name))
	case !isCommand && !isPicture && !slices.Contains([]string{configName, ledgerName, scheduleName, meshName, cacheName}, name):
		return damaged(fmt.Errorf("it holds %q, which kith never writes", name))
	case slices.Contains(seen, name):
		return damaged(fmt.Errorf("it holds %q twice", name))
	case afterCache:
		return damaged(fmt.Errorf("%q comes after the cache", name))
	}
	return nil
}

// plainName is a file name with no folder in it.
func plainName(name string) bool {
	return name != "" && path.Base(name) == name && name != "." && name != ".."
}

// prepare reads the backup's config, once, to learn where files go on this machine,
// and refuses if kithd runs there or files are there (unless replacing). Pictures and
// the cache follow the config, so the first of them prepares.
func (r *restore) prepare() error {
	if r.storage.DataDir != "" {
		return nil
	}
	text, ok := r.small[configName]
	if !ok {
		return damaged(errors.New("the pictures or the cache come before the config"))
	}
	cfg, err := config.Decode(string(text))
	if err != nil {
		return fmt.Errorf("restore: the backup's config: %w", err)
	}
	storage, err := setup.StorageFor(cfg, r.configPath)
	if err != nil {
		return fmt.Errorf("restore: the backup's config: %w", err)
	}
	r.cfg, r.storage, r.media = cfg, storage, storage.MediaDir()
	if lockErr := r.lock(storage); lockErr != nil {
		return lockErr
	}
	return r.refuseExisting()
}

// stagePicture writes one picture into a folder beside the picture cache, keeping its
// time, which the cache trims by.
func (r *restore) stagePicture(ctx context.Context, name string, hdr *tar.Header, tr io.Reader) error {
	if err := r.prepare(); err != nil {
		return err
	}
	if r.pictures == "" {
		if err := os.MkdirAll(r.media, 0o700); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		dir, err := os.MkdirTemp(filepath.Dir(r.media), ".restore-pictures-*")
		if err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		r.staged = append(r.staged, dir)
		r.pictures = dir
	}
	staged, err := r.stageStream(ctx, r.pictures, ".picture-*", archiveReader{tr})
	if err != nil {
		return err
	}
	dest := filepath.Join(r.pictures, name)
	if err := os.Rename(staged, dest); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if err := os.Chmod(dest, fs.FileMode(hdr.Mode&0o600)); err != nil { // #nosec G115 -- permission bits only
		return fmt.Errorf("restore: %w", err)
	}
	_ = os.Chtimes(dest, hdr.ModTime, hdr.ModTime)
	r.pictureNames = append(r.pictureNames, name)
	return nil
}

// stageCache writes the cache beside its place on this machine and adopts it under
// this machine's key.
func (r *restore) stageCache(ctx context.Context, tr io.Reader) error {
	key, err := r.storeKey()
	if err != nil {
		return err
	}
	storage := r.storage
	copied, err := r.stageStream(ctx, storage.DataDir, ".restore-cache-*.db", archiveReader{tr})
	if err != nil {
		return err
	}
	adopted, err := r.stageStream(ctx, storage.DataDir, ".restore-adopted-*.db", strings.NewReader(""))
	if err != nil {
		return err
	}
	if adoptErr := db.Adopt(ctx, copied, r.backupKey, adopted, key); adoptErr != nil {
		return fmt.Errorf("restore: the backup's cache: %w", adoptErr)
	}
	_ = os.Remove(copied)
	r.cache = adopted
	return nil
}

// storeKey prepares, makes the data folder, and finds the key the cache and the mesh
// store are encrypted under on this machine.
func (r *restore) storeKey() ([]byte, error) {
	if err := r.prepare(); err != nil {
		return nil, err
	}
	if r.key != nil {
		return r.key, nil
	}
	if mkErr := os.MkdirAll(r.storage.DataDir, 0o700); mkErr != nil {
		return nil, fmt.Errorf("restore: %w", mkErr)
	}
	key, err := r.cacheKey(r.storage)
	if err != nil {
		return nil, fmt.Errorf("restore: the cache's key on this machine: %w", err)
	}
	r.key = key
	return key, nil
}

// stageMesh writes the mesh store beside its place on this machine and copies it under
// this machine's key, checked as kithd reads it.
func (r *restore) stageMesh(ctx context.Context, tr io.Reader) error {
	key, err := r.storeKey()
	if err != nil {
		return err
	}
	copied, err := r.stageStream(ctx, r.storage.DataDir, ".restore-mesh-copy-*.db", archiveReader{tr})
	if err != nil {
		return err
	}
	adopted, err := r.stageStream(ctx, r.storage.DataDir, ".restore-mesh-*.db", strings.NewReader(""))
	if err != nil {
		return err
	}
	if copyErr := mesh.Copy(ctx, copied, r.backupKey, adopted, key); copyErr != nil {
		return fmt.Errorf("restore: the backup's mesh store: %w", copyErr)
	}
	_ = os.Remove(copied)
	r.mesh = adopted
	return nil
}

// refuseExisting refuses, unless replacing, when any file the restore would put in
// place is there already.
func (r *restore) refuseExisting() error {
	if r.replace {
		return nil
	}
	cache, meshStore := r.storage.CachePath(), r.storage.MeshPath()
	var there []string
	for _, p := range []string{
		cache, cache + "-wal", cache + "-shm", meshStore, meshStore + "-wal", meshStore + "-shm",
		r.storage.LedgerPath(), r.storage.SchedulePath(), r.storage.BackupKeyPath(),
	} {
		if _, err := os.Lstat(p); err == nil {
			there = append(there, p)
		}
	}
	if len(there) > 0 {
		return fmt.Errorf("restore: %w: %s (--replace sets them aside)", ErrWouldReplace, strings.Join(there, ", "))
	}
	return nil
}

// stageStream writes src to a new file in dir, synced, and records it as staged.
func (r *restore) stageStream(ctx context.Context, dir, pattern string, src io.Reader) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("restore: %w", err)
	}
	r.staged = append(r.staged, f.Name())
	err = copyCtx(ctx, f, src)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if bad, ok := errors.AsType[badArchive](err); ok {
		return "", damaged(bad.err)
	}
	if err != nil {
		return "", fmt.Errorf("restore: write %s: %w", f.Name(), err)
	}
	return f.Name(), nil
}

// stageBytes is stageStream of body, given mode.
func (r *restore) stageBytes(ctx context.Context, dir, pattern string, body []byte, mode fs.FileMode) (string, error) {
	p, err := r.stageStream(ctx, dir, pattern, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	if err := os.Chmod(p, mode); err != nil {
		return "", fmt.Errorf("restore: %w", err)
	}
	return p, nil
}

// move is a staged file or folder and where it goes.
type move struct {
	staged, dest string
}

// swap stages the small files beside their places (the last writes that can fail on
// a full disk), then moves everything in, setting aside what was there.
func (r *restore) swap(ctx context.Context) (Restored, error) {
	moves, err := r.stageSmall(ctx)
	if err != nil {
		return Restored{}, err
	}
	stamp := time.Now().Format("20060102-150405")
	if err := r.placeCache(ctx, stamp); err != nil {
		return r.done, err
	}
	if err := r.placeMesh(stamp); err != nil {
		return r.done, err
	}
	for _, m := range moves {
		if err := r.place(m, stamp); err != nil {
			return r.done, err
		}
	}
	r.placePictures()
	r.done.Logins = logins(r.cfg)
	return r.done, nil
}

// placePictures adds the staged pictures to the picture cache. A picture is named by
// the message it came with, so one already there is the same picture and stays.
func (r *restore) placePictures() {
	if r.pictures == "" {
		return
	}
	r.done.PicturesFolder = r.media
	var failed []string
	for _, name := range r.pictureNames {
		dest := filepath.Join(r.media, name)
		if _, err := os.Lstat(dest); err == nil {
			r.done.PicturesThere++
			continue
		}
		if err := os.Rename(filepath.Join(r.pictures, name), dest); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		r.done.Pictures++
	}
	if len(failed) > 0 {
		r.done.Left = append(r.done.Left, fmt.Sprintf("pictures: %d not put back (%s)", len(failed), failed[0]))
	}
}

// stageSmall writes the config, the records and the commands beside where each goes.
func (r *restore) stageSmall(ctx context.Context) ([]move, error) {
	var moves []move
	// The backup key's public half goes with it, so this machine's backups go on to the
	// key that opened this one.
	r.small[keyName] = []byte(r.id.Recipient().String() + "\n")
	for _, s := range []struct{ name, dest string }{
		{configName, r.configPath},
		{ledgerName, r.storage.LedgerPath()},
		{scheduleName, r.storage.SchedulePath()},
		{keyName, r.storage.BackupKeyPath()},
	} {
		body, ok := r.small[s.name]
		if !ok {
			continue
		}
		dir := filepath.Dir(s.dest)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("restore: %w", err)
		}
		staged, err := r.stageBytes(ctx, dir, ".restore-"+filepath.Base(s.dest)+"-*", body, 0o600)
		if err != nil {
			return nil, err
		}
		moves = append(moves, move{staged: staged, dest: s.dest})
	}
	commands, err := r.stageCommands(ctx, filepath.Join(filepath.Dir(r.configPath), "commands"))
	if err != nil {
		return nil, err
	}
	return append(moves, commands...), nil
}

// stageCommands writes the backup's commands to a folder beside dest. A dest that is
// a link is left alone: whatever made it keeps it.
func (r *restore) stageCommands(ctx context.Context, dest string) ([]move, error) {
	var names []string
	for _, name := range r.order {
		if strings.HasPrefix(name, commandsDir) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	if info, err := os.Lstat(dest); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		target, _ := os.Readlink(dest)
		r.done.Left = append(r.done.Left, "commands: "+dest+" is a link to "+target+"; left as it is")
		return nil, nil
	}
	dir, err := os.MkdirTemp(filepath.Dir(dest), ".restore-commands-*")
	if err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}
	r.staged = append(r.staged, dir)
	for _, name := range names {
		staged, err := r.stageBytes(ctx, dir, ".command-*", r.small[name], r.smallMode[name])
		if err != nil {
			return nil, err
		}
		if err := os.Rename(staged, filepath.Join(dir, strings.TrimPrefix(name, commandsDir))); err != nil {
			return nil, fmt.Errorf("restore: %w", err)
		}
	}
	return []move{{staged: dir, dest: dest}}, nil
}

// placeCache moves the adopted cache into place. A cache there is checkpointed first,
// so its file holds every commit, then set aside with its log.
func (r *restore) placeCache(ctx context.Context, stamp string) error {
	dest := r.storage.CachePath()
	if _, err := os.Stat(dest); err == nil {
		if serr := db.Settle(context.WithoutCancel(ctx), dest, r.key); serr != nil {
			r.done.Left = append(r.done.Left, "the old cache could not be checkpointed ("+serr.Error()+"); its log was set aside with it")
		}
		aside := dest + asideInfix + stamp
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Rename(dest+suffix, aside+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("restore: set the old cache aside: %w", err)
			}
		}
		r.done.Aside = append(r.done.Aside, aside)
	}
	if err := os.Rename(r.cache, dest); err != nil {
		return fmt.Errorf("restore: put the cache in place: %w", err)
	}
	r.done.Files = append(r.done.Files, dest)
	return nil
}

// placeMesh moves the copied mesh store into place, if the backup held one. A store
// there is set aside with its log, which it is read with.
func (r *restore) placeMesh(stamp string) error {
	if r.mesh == "" {
		return nil
	}
	dest := r.storage.MeshPath()
	if _, err := os.Stat(dest); err == nil {
		aside := dest + asideInfix + stamp
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Rename(dest+suffix, aside+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("restore: set the old mesh store aside: %w", err)
			}
		}
		r.done.Aside = append(r.done.Aside, aside)
	}
	if err := os.Rename(r.mesh, dest); err != nil {
		return fmt.Errorf("restore: put the mesh store in place: %w", err)
	}
	r.done.Files = append(r.done.Files, dest)
	return nil
}

// place moves one staged file or folder in. A file there stays in place until the
// rename replaces it (its aside name is a second link), so dest is never missing.
func (r *restore) place(m move, stamp string) error {
	aside := m.dest + asideInfix + stamp
	if info, err := os.Lstat(m.dest); err == nil {
		if info.IsDir() || os.Link(m.dest, aside) != nil {
			if err := os.Rename(m.dest, aside); err != nil {
				return fmt.Errorf("restore: set %s aside: %w", m.dest, err)
			}
		}
		r.done.Aside = append(r.done.Aside, aside)
	}
	if err := os.Rename(m.staged, m.dest); err != nil {
		return fmt.Errorf("restore: put %s in place: %w", m.dest, err)
	}
	r.done.Files = append(r.done.Files, m.dest)
	return nil
}

// logins is every account the config names, to log in to again.
func logins(cfg config.Config) []Login {
	var out []Login
	for _, n := range cfg.Networks() {
		network := strings.TrimSuffix(n.Table(), ".account")
		for _, account := range n.AccountNames() {
			out = append(out, Login{Network: network, Account: account})
		}
	}
	return out
}
