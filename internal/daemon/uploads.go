package daemon

import (
	"cmp"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
)

const (
	// uploadMost is the largest file kithd stages: 2 GiB, what Telegram and WhatsApp
	// take, the most of any network kith speaks.
	uploadMost = 2 << 30
	// stagedMost bounds every staged file together, so uploads cannot fill the disk.
	stagedMost = 2 * uploadMost
	// uploadIdle is how long an upload no chunk reaches is kept: a client that went
	// away mid-file does not come back for it.
	uploadIdle = time.Hour
)

// Uploads stages the files clients send (StartUpload, PutUpload) in kithd's own folder
// until SendFile hands one to the backend as a path, as before clients could run on
// another machine: only the bytes cross. Its methods answer in the wire's terms.
type Uploads struct {
	dir string

	mu   sync.Mutex
	open map[string]*upload
	// staged is the size of every staged file, open or being sent.
	staged int64
}

// upload is one file arriving.
type upload struct {
	name, path string
	size       int64
	// written is what chunks have covered, sorted and merged: the file is complete when
	// it is one span over all of it, whatever order the chunks came in and however often.
	written []span
	touched time.Time
	expiry  *time.Timer
}

// span is the bytes [from, to).
type span struct{ from, to int64 }

// NewUploads stages files in dir, emptied first: what a kithd that stopped had staged
// is no client's any more.
func NewUploads(dir string) (*Uploads, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("daemon: clear the uploads: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("daemon: make the uploads folder: %w", err)
	}
	return &Uploads{dir: dir, open: map[string]*upload{}}, nil
}

// start stages an empty file for an upload of size bytes; name is the file's own.
func (u *Uploads) start(name string, size int64) (string, error) {
	if size < 0 || size > uploadMost {
		return "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("daemon: a file kith sends is at most %d MiB", uploadMost>>20))
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.staged+size > stagedMost {
		return "", connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("daemon: %d MiB of files are waiting to be sent already; send this one after them", u.staged>>20))
	}
	id := rand.Text()
	path := filepath.Join(u.dir, id)
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", rpcErr(fmt.Errorf("daemon: stage an upload: %w", err))
	}
	if err := f.Close(); err != nil {
		return "", rpcErr(fmt.Errorf("daemon: stage an upload: %w", err))
	}
	up := &upload{name: name, path: path, size: size, touched: time.Now()}
	up.expiry = time.AfterFunc(uploadIdle, func() { u.expire(id, up) })
	u.open[id] = up
	u.staged += size
	return id, nil
}

// put writes data at offset into upload id's file.
func (u *Uploads) put(id string, offset int64, data []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	up, ok := u.open[id]
	if !ok {
		return noUpload()
	}
	if offset < 0 || offset > up.size-int64(len(data)) {
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("daemon: %d bytes at %d do not fit a file of %d", len(data), offset, up.size))
	}
	if err := writeAt(up.path, offset, data); err != nil {
		return rpcErr(fmt.Errorf("daemon: stage an upload: %w", err))
	}
	up.written = covering(up.written, span{offset, offset + int64(len(data))})
	up.touched = time.Now()
	up.expiry.Reset(uploadIdle)
	return nil
}

// take hands over complete upload id's file and name for sending; done removes the
// file, sent or not, so a send that failed starts again from StartUpload.
func (u *Uploads) take(id string) (path, name string, done func(), err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	up, ok := u.open[id]
	if !ok {
		return "", "", nil, noUpload()
	}
	if !up.complete() {
		return "", "", nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("daemon: the upload has %d of its %d bytes", up.has(), up.size))
	}
	delete(u.open, id)
	up.expiry.Stop()
	return up.path, up.name, func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.discard(up)
	}, nil
}

// expire removes upload id if no chunk reached it for uploadIdle; one sent, or
// touched since the timer fired (which re-armed it), stays.
func (u *Uploads) expire(id string, up *upload) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.open[id] != up || time.Since(up.touched) < uploadIdle {
		return
	}
	delete(u.open, id)
	u.discard(up)
}

// discard removes up's file and its bytes from the total; u.mu is held.
func (u *Uploads) discard(up *upload) {
	_ = os.Remove(up.path) // best effort: the next start empties the folder
	u.staged -= up.size
}

// noUpload refuses an upload ID kithd does not have: never started, sent already,
// expired, or started before kithd restarted.
func noUpload() error {
	return connect.NewError(connect.CodeNotFound, errors.New("daemon: no such upload"))
}

func (up *upload) complete() bool {
	return up.size == 0 || len(up.written) == 1 && up.written[0] == span{0, up.size}
}

// has counts the bytes written.
func (up *upload) has() int64 {
	var n int64
	for _, s := range up.written {
		n += s.to - s.from
	}
	return n
}

// covering adds s to spans, keeping them sorted and merged.
func covering(spans []span, s span) []span {
	if s.from == s.to {
		return spans
	}
	out := make([]span, 0, len(spans)+1)
	for _, have := range spans {
		if have.to < s.from || s.to < have.from {
			out = append(out, have)
			continue
		}
		s = span{min(have.from, s.from), max(have.to, s.to)}
	}
	out = append(out, s)
	slices.SortFunc(out, func(a, b span) int { return cmp.Compare(a.from, b.from) })
	return out
}

// writeAt writes data at offset into the file at path.
func writeAt(path string, offset int64, data []byte) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	_, werr := f.WriteAt(data, offset)
	return errors.Join(werr, f.Close())
}
