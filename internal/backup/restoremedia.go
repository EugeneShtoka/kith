package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// A media backup (CreateMedia) is put back apart from the history: into the media
// archive of a config already there, adding what it lacks.

var (
	errMediaBackup    = errors.New("this is a media backup: put it back with `kith restore --media`")
	errNotMediaBackup = errors.New("this is not a media backup: put it back without --media")
)

// RestoreMedia puts the media backup at archive back into the media archive of the
// config at configPath, which must read: restore the history first. A copy already
// there stays (it is named by its message, so it is the same one), and kithd may keep
// running, since the archive only gains files. key, the private backup key, is asked
// once the config reads. Until the whole backup is read and checked nothing is placed.
func RestoreMedia(ctx context.Context, archive, configPath string, key func() (*Key, error)) (Restored, error) {
	storage, ok := storageOf(configPath)
	if !ok {
		return Restored{}, fmt.Errorf("restore: %s does not read; put the history back first", configPath)
	}
	r := restore{configPath: configPath, storage: storage}
	r.archive = folder{dest: storage.MediaArchiveDir(), pattern: ".restore-archive-*"}
	defer r.cleanup()
	f, err := os.Open(archive) // #nosec G304 -- the backup this user named
	if err != nil {
		return Restored{}, fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = f.Close() }()
	id, err := key()
	if err != nil {
		return Restored{}, err
	}
	r.id = id
	if err := r.readMedia(ctx, f); err != nil {
		return Restored{}, err
	}
	r.done.Archived, r.done.ArchivedThere, r.done.ArchiveFolder = r.placeFolder(&r.archive, "archived attachments")
	return r.done, nil
}

// readMedia decrypts a media backup and stages every copy beside the archive, reading
// to the very end, so a cut or altered backup fails before anything is placed.
func (r *restore) readMedia(ctx context.Context, f io.Reader) error {
	plain, err := r.decrypt(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(plain)
	listed, _, err := readManifest(tr, kindMedia)
	if err != nil {
		return err
	}
	seen := make([]string, 0, len(listed))
	had := make(map[string]bool, len(listed)) // a set: an archive holds many thousands
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return damaged(err)
		}
		name, isCopy := strings.CutPrefix(hdr.Name, archivePrefix)
		switch {
		case hdr.Typeflag != tar.TypeReg || !isCopy || !plainName(name) || strings.HasPrefix(name, "."):
			return damaged(fmt.Errorf("it holds %q, which a media backup never does", hdr.Name))
		case had[hdr.Name]:
			return damaged(fmt.Errorf("it holds %q twice", hdr.Name))
		}
		had[hdr.Name] = true
		seen = append(seen, hdr.Name)
		if stageErr := r.stageFile(ctx, &r.archive, name, hdr, tr); stageErr != nil {
			return stageErr
		}
	}
	// age checks the last chunk only once it is read: a backup cut at its end is found here.
	if _, err := io.Copy(io.Discard, plain); err != nil {
		return damaged(err)
	}
	if !slices.Equal(seen, listed) {
		return damaged(fmt.Errorf("it holds %d copies; its list says %d", len(seen), len(listed)))
	}
	return nil
}
