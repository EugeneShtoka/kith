// Package mediaarchive keeps a copy of every attachment kithd caches, so one a network
// later removes (WhatsApp drops old media for good) still opens. It never deletes a
// copy: past a size it warns instead. Where a room's are to be encrypted, its copies
// are sealed (seal.go), those already there too.
package mediaarchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Cache is the cache's half: which attachments there are to keep.
type Cache interface {
	// Attachments lists room's ("" every room's) on messages sent since, oldest first.
	Attachments(ctx context.Context, room domain.RoomID, since time.Time) ([]domain.AttachmentRef, error)
	// Attachment is one message's, its message's deletion marked; false when it has none.
	Attachment(ctx context.Context, room domain.RoomID, event domain.EventID) (domain.AttachmentRef, bool, error)
}

// Fetch downloads an attachment's bytes from its network.
type Fetch func(ctx context.Context, room domain.RoomID, event domain.EventID) ([]byte, error)

// settle is how long arrivals gather before the worker fetches them: a sync writes in
// many small batches, and a disappearing message's timer is recorded just after it.
const settle = 2 * time.Second

// tmpPattern names a file being written; one a crash left is removed at the next start.
const tmpPattern = ".tmp-*"

// Archive is the folder of copies and the worker filling it.
type Archive struct {
	dir    string
	cache  Cache
	fetch  Fetch
	sealer *Sealer
	log    *slog.Logger

	// Set before Run (Use): whether a room's attachments are kept, and sealed, and its
	// disappearing messages' kept too, the size to warn past (negative: never), and how
	// to warn.
	archived  func(domain.RoomID) bool
	encrypted func(domain.RoomID) bool
	timed     func(domain.RoomID) bool
	warnAt    func() int64
	warn      func(body string)

	mu     sync.Mutex
	used   int64 // bytes in dir
	warned bool
	rooms  map[domain.RoomID]time.Time // arrivals not yet fetched, by the oldest message
	wake   chan struct{}
}

// New is the archive in dir, fetching through fetch and sealing with sealer. Nothing
// is kept until Use.
func New(dir string, cache Cache, fetch Fetch, sealer *Sealer, log *slog.Logger) *Archive {
	return &Archive{
		dir: dir, cache: cache, fetch: fetch, sealer: sealer, log: log,
		archived:  func(domain.RoomID) bool { return false },
		encrypted: func(domain.RoomID) bool { return false },
		timed:     func(domain.RoomID) bool { return false },
		warnAt:    func() int64 { return -1 },
		warn:      func(string) {},
		rooms:     map[domain.RoomID]time.Time{},
		wake:      make(chan struct{}, 1),
	}
}

// Use sets the policy, read on every use so a reload reaches it; a reload that may
// change it asks for a pass over every room (Arrived("", time.Time{})).
func (a *Archive) Use(archived, encrypted, timed func(domain.RoomID) bool, warnAt func() int64, warn func(body string)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.archived, a.encrypted, a.timed, a.warnAt, a.warn = archived, encrypted, timed, warnAt, warn
}

// Arrived queues room's attachments on messages since to be fetched; it never blocks.
func (a *Archive) Arrived(room domain.RoomID, since time.Time) {
	a.mu.Lock()
	if had, ok := a.rooms[room]; !ok || since.Before(had) {
		a.rooms[room] = since
	}
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Run keeps every attachment the cache already holds, then each that arrives, until
// ctx ends. One download at a time: a catch-up must not crowd out what is opened.
func (a *Archive) Run(ctx context.Context) {
	if err := a.open(); err != nil {
		a.log.Warn("open the media archive failed", "dir", a.dir, "err", err)
		return
	}
	a.keepSince(ctx, "", time.Time{})
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(settle):
		}
		a.mu.Lock()
		rooms := a.rooms
		a.rooms = map[domain.RoomID]time.Time{}
		a.mu.Unlock()
		for room, since := range rooms {
			a.keepSince(ctx, room, since)
		}
	}
}

// open makes the folder, removes what a crash left half written, and counts its size.
func (a *Archive) open() error {
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("mediaarchive: %w", err)
	}
	partial, _ := filepath.Glob(filepath.Join(a.dir, tmpPattern))
	for _, p := range partial {
		_ = os.Remove(p) // a crash's leftover; failing only leaves it for next time
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return fmt.Errorf("mediaarchive: %w", err)
	}
	var used int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			used += info.Size()
		}
	}
	a.mu.Lock()
	a.used = used
	a.mu.Unlock()
	a.checkSize()
	return nil
}

// keepSince fetches each attachment in room ("" every room) since that is kept and not
// there yet, and seals each copy there plain that is to be sealed. A failure is retried
// when the room next gains messages, or at the next start.
func (a *Archive) keepSince(ctx context.Context, room domain.RoomID, since time.Time) {
	refs, err := a.cache.Attachments(ctx, room, since)
	if err != nil {
		a.log.Warn("list attachments to archive failed", "room", room, "err", err)
		return
	}
	var failed int
	for _, ref := range refs {
		if ctx.Err() != nil {
			return
		}
		if !a.keeps(ref) {
			continue
		}
		if a.seals(ref.Room) && exists(a.path(ref)) {
			if err := a.sealPlain(ref); err != nil {
				a.log.Warn("encrypt an archived attachment failed", "room", ref.Room, "err", err)
				return // a full or unwritable disk fails the rest alike
			}
			continue
		}
		if a.has(ref) {
			continue
		}
		data, err := a.fetch(ctx, ref.Room, ref.Event)
		if err != nil {
			failed++
			a.log.Debug("fetch an attachment to archive failed", "room", ref.Room, "event", ref.Event, "err", err)
			continue
		}
		if err := a.keep(ctx, ref, data); err != nil {
			a.log.Warn("archive an attachment failed", "room", ref.Room, "err", err)
			return // a full or unwritable disk fails the rest alike
		}
	}
	if failed > 0 {
		a.log.Info("some attachments could not be archived; retried later", "room", room, "failed", failed, "of", len(refs))
	}
}

// Load is an attachment's bytes: the archived copy when there is one (a deleted or
// disappeared message's too, while the cache keeps what it said), else fetched, and
// kept when its room's are.
func (a *Archive) Load(ctx context.Context, room domain.RoomID, event domain.EventID) ([]byte, error) {
	ref, ok, err := a.cache.Attachment(ctx, room, event)
	if err != nil {
		a.log.Warn("look up an archived attachment failed", "room", room, "err", err)
	}
	if ok {
		if data, readErr := a.read(ref); readErr == nil {
			return data, nil
		}
	}
	data, err := a.fetch(ctx, room, event)
	if err != nil {
		return nil, err
	}
	if ok && a.keeps(ref) {
		if err := a.keep(ctx, ref, data); err != nil {
			a.log.Warn("archive an attachment failed", "room", room, "err", err)
		}
	}
	return data, nil
}

// keeps is whether ref is archived: its room's are, and, on a disappearing message, its
// room's disappearing messages' are too. A deleted message's copy is served, never made.
func (a *Archive) keeps(ref domain.AttachmentRef) bool {
	a.mu.Lock()
	archived, timed := a.archived, a.timed
	a.mu.Unlock()
	return !ref.Deleted && archived(ref.Room) && (!ref.Timed || timed(ref.Room))
}

// seals is whether room's copies are sealed: encrypt_media, where kithd has a key.
func (a *Archive) seals(room domain.RoomID) bool {
	a.mu.Lock()
	encrypted := a.encrypted
	a.mu.Unlock()
	return a.sealer != nil && encrypted(room)
}

// has is whether ref is archived, plain or sealed.
func (a *Archive) has(ref domain.AttachmentRef) bool {
	return exists(a.path(ref)) || exists(a.sealedPath(ref))
}

// read is ref's archived copy: the plain one, else the sealed one opened.
func (a *Archive) read(ref domain.AttachmentRef) ([]byte, error) {
	data, err := os.ReadFile(a.path(ref)) // #nosec G304 -- a name this package made from a hash
	if err == nil {
		return data, nil
	}
	if a.sealer == nil {
		return nil, fmt.Errorf("mediaarchive: %w", err)
	}
	sealed, err := os.ReadFile(a.sealedPath(ref)) // #nosec G304 -- as above
	if err != nil {
		return nil, fmt.Errorf("mediaarchive: %w", err)
	}
	return a.sealer.Open(a.sealedPath(ref), sealed)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// keep writes data as ref's copy, unless the message changed while it was fetched (an
// edit that replaced the attachment, a deletion, a timer): the next pass sees it anew.
func (a *Archive) keep(ctx context.Context, ref domain.AttachmentRef, data []byte) error {
	now, ok, err := a.cache.Attachment(ctx, ref.Room, ref.Event)
	if err != nil {
		return fmt.Errorf("mediaarchive: look up %s again: %w", ref.Event, err)
	}
	if !ok || now != ref {
		return nil
	}
	body, dest := data, a.path(ref)
	if a.seals(ref.Room) {
		dest = a.sealedPath(ref)
		body = a.sealer.Seal(dest, data)
	}
	if err := a.place(ref, body, dest, false); err != nil {
		return err
	}
	a.checkSize()
	return nil
}

// sealPlain seals ref's plain copy, in its place.
func (a *Archive) sealPlain(ref domain.AttachmentRef) error {
	data, err := os.ReadFile(a.path(ref)) // #nosec G304 -- a name this package made from a hash
	if err != nil {
		return fmt.Errorf("mediaarchive: %w", err)
	}
	dest := a.sealedPath(ref)
	return a.place(ref, a.sealer.Seal(dest, data), dest, true)
}

// place writes body as dest unless ref is archived already (sealing: sealed already),
// written whole first, so a crash leaves no part. Sealing then removes the plain copy,
// only once the sealed one is there, so a Load meanwhile finds one or the other.
func (a *Archive) place(ref domain.AttachmentRef, body []byte, dest string, sealing bool) error {
	tmp, err := os.CreateTemp(a.dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("mediaarchive: write %s: %w", ref.Event, err)
	}
	name := tmp.Name()
	_, err = tmp.Write(body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name) // cleanup on the error path; err is the report
		return fmt.Errorf("mediaarchive: write %s: %w", ref.Event, err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Under the lock, so a copy written twice at once (a request and the worker) is
	// counted once.
	if exists(dest) || !sealing && a.has(ref) {
		_ = os.Remove(name) // a copy that was there first
	} else {
		if err := os.Rename(name, dest); err != nil {
			_ = os.Remove(name) // cleanup on the error path; err is the report
			return fmt.Errorf("mediaarchive: write %s: %w", ref.Event, err)
		}
		a.used += int64(len(body))
	}
	if plain := a.path(ref); sealing {
		if info, err := os.Lstat(plain); err == nil && os.Remove(plain) == nil {
			a.used -= info.Size()
		}
	}
	return nil
}

// checkSize warns once the folder passes the size to warn at, and again only after it
// was back under it (a raised limit).
func (a *Archive) checkSize() {
	a.mu.Lock()
	at, used := a.warnAt(), a.used
	over := at >= 0 && used > at
	say := over && !a.warned
	a.warned = over
	warn := a.warn
	a.mu.Unlock()
	if say {
		warn(fmt.Sprintf("Archived attachments take %s, past [storage] archive_warn_mb (%s). "+
			"kith deletes none of them: free space in %s, or raise the limit.",
			domain.HumanSize(int(used)), domain.HumanSize(int(at)), a.dir))
	}
}

// path is where ref's copy is: named by its message and the edit it shows.
func (a *Archive) path(ref domain.AttachmentRef) string {
	return filepath.Join(a.dir, hashOf(ref)+extension(ref.Name, ref.Mime))
}

// sealedPath is where ref's sealed copy is.
func (a *Archive) sealedPath(ref domain.AttachmentRef) string {
	return filepath.Join(a.dir, hashOf(ref)+SealedExt)
}

func hashOf(ref domain.AttachmentRef) string {
	sum := sha256.Sum256([]byte(string(ref.Event) + "\x00" + string(ref.Revision)))
	return hex.EncodeToString(sum[:])
}

// extension is the name's own, when short and plain, else the MIME type's, so a file
// opens from the folder too.
func extension(name, mimeType string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) > 1 && len(ext) <= 6 && strings.Trim(ext[1:], "abcdefghijklmnopqrstuvwxyz0123456789") == "" {
		return ext
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}
