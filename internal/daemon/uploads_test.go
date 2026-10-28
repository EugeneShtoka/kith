package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// chunk is the Remote's chunk size, which these tests cut files around.
const chunk = 1 << 20

// posted is one file a backend was handed, as it read it then.
type posted struct {
	room          domain.RoomID
	name, caption string
	data          []byte
	mode          fs.FileMode
}

// receiving is a backend that reads each file it is handed to post, and fails the
// post when fail is set.
type receiving struct {
	apitest.Nop
	mu   sync.Mutex
	got  []posted
	fail error
}

func (b *receiving) SendFile(_ context.Context, room domain.RoomID, path, name, caption string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the staged upload
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = append(b.got, posted{room: room, name: name, caption: caption, data: data, mode: info.Mode().Perm()})
	return b.fail
}

func (b *receiving) posted() []posted {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]posted(nil), b.got...)
}

// uploading serves b with its uploads staged in a folder the test can look into.
func uploading(t *testing.T, b *receiving) (h *harness, staging string) {
	t.Helper()
	staging = filepath.Join(t.TempDir(), "uploads")
	uploads, err := daemon.NewUploads(staging)
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, b, func(d *daemon.Daemon) { d.Uploads = uploads }), staging
}

// staged names the files waiting in the staging folder.
func staged(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// randomFile writes size random bytes to a file named name and returns its path.
func randomFile(t *testing.T, rng *rand.Rand, name string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(rng.Uint32())
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

// A file of several chunks and a ragged end reaches the backend byte for byte, as a
// 0600 file under the name asked for (else its own), and is gone from kithd once sent.
func TestALargeFileArrivesIntact(t *testing.T) {
	t.Parallel()
	b := &receiving{}
	h, staging := uploading(t, b)
	r := h.client()
	rng := rand.New(rand.NewPCG(1, 2)) // #nosec G404 -- reproducible
	path, want := randomFile(t, rng, "holiday.mkv", 5*chunk+12345)

	if err := r.SendFile(t.Context(), mx+"!a:x", path, "video.mkv", "look"); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if err := r.SendFile(t.Context(), mx+"!a:x", path, "", ""); err != nil {
		t.Fatalf("SendFile under its own name: %v", err)
	}
	got := b.posted()
	if len(got) != 2 {
		t.Fatalf("posted %d files, want 2", len(got))
	}
	for i, wantName := range []string{"video.mkv", "holiday.mkv"} {
		p := got[i]
		if !bytes.Equal(p.data, want) {
			t.Errorf("file %d: %d bytes arrived, not the %d sent", i, len(p.data), len(want))
		}
		if p.name != wantName || p.room != mx+"!a:x" || p.mode != 0o600 {
			t.Errorf("file %d posted as %q in %s, mode %v; want %q, 0600", i, p.name, p.room, p.mode, wantName)
		}
	}
	if got[0].caption != "look" {
		t.Errorf("caption %q", got[0].caption)
	}
	if left := staged(t, staging); len(left) != 0 {
		t.Errorf("staged after sending: %v", left)
	}
}

// fault is what happens to one PutUpload on its way.
type fault int

const (
	passed fault = iota
	// cutBefore drops the request before kithd sees it; cutAfter drops kithd's answer
	// after it wrote the chunk.
	cutBefore
	cutAfter
)

// cutting is a transport that cuts PutUpload requests off as next says.
type cutting struct {
	through http.RoundTripper
	mu      sync.Mutex
	next    func() fault
	cuts    int
}

var errCut = errors.New("connection cut")

func (c *cutting) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/PutUpload") {
		return c.through.RoundTrip(r)
	}
	c.mu.Lock()
	f := c.next()
	if f != passed {
		c.cuts++
	}
	c.mu.Unlock()
	switch f {
	case cutBefore:
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, errCut
	case cutAfter:
		resp, err := c.through.RoundTrip(r)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return nil, errCut
	default:
		return c.through.RoundTrip(r)
	}
}

// Over generated files and connections that cut chunks off before kithd sees them or
// after it wrote them, never more often in a row than the Remote tries: every file
// arrives intact, and nothing stays staged. Cut off on every try, the send fails and
// nothing is posted.
func TestAnUploadCutOffResumesWhereItWas(t *testing.T) {
	t.Parallel()
	for seed := range uint64(16) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 0xc07)) // #nosec G404 -- reproducible
			b := &receiving{}
			h, staging := uploading(t, b)
			hopeless := seed%8 == 7
			inARow := 0
			cut := &cutting{through: daemon.Dial(h.path).Transport, next: func() fault {
				if !hopeless && (inARow == 3 || rng.IntN(3) > 0) {
					inARow = 0
					return passed
				}
				inARow++
				return fault(1 + rng.IntN(2))
			}}
			r := daemon.RemoteOver(&http.Client{Transport: cut})
			path, want := randomFile(t, rng, "f.bin", rng.IntN(3*chunk+chunk/2)+1)

			err := r.SendFile(t.Context(), mx+"!a:x", path, "", "")
			got := b.posted()
			if hopeless {
				if err == nil || len(got) != 0 {
					t.Fatalf("every chunk cut off: SendFile = %v, %d posted; want a failure and none", err, len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("SendFile after %d cuts: %v", cut.cuts, err)
			}
			if len(got) != 1 || !bytes.Equal(got[0].data, want) {
				t.Fatalf("after %d cuts the backend got %d files; the first intact: %v", cut.cuts, len(got),
					len(got) > 0 && bytes.Equal(got[0].data, want))
			}
			if left := staged(t, staging); len(left) != 0 {
				t.Errorf("staged after sending: %v", left)
			}
		})
	}
}

// rpc runs one raw call and returns its error's code, or 0 when it succeeded.
func rpc[Req, Res any](ctx context.Context, f func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) (*Res, connect.Code) {
	resp, err := f(ctx, connect.NewRequest(msg))
	if err != nil {
		return nil, connect.CodeOf(err)
	}
	return resp.Msg, 0
}

// start begins an upload over the raw API.
func start(t *testing.T, c backendv1connect.BackendServiceClient, size int64) string {
	t.Helper()
	res, code := rpc(t.Context(), c.StartUpload, &v1.StartUploadRequest{Name: "f.bin", Size: size})
	if code != 0 {
		t.Fatalf("StartUpload(%d) = %v", size, code)
	}
	return res.GetUploadId()
}

func put(ctx context.Context, c backendv1connect.BackendServiceClient, id string, offset int, data []byte) connect.Code {
	_, code := rpc(ctx, c.PutUpload, &v1.PutUploadRequest{UploadId: id, Offset: int64(offset), Data: data})
	return code
}

func sendUpload(ctx context.Context, c backendv1connect.BackendServiceClient, id string) connect.Code {
	_, code := rpc(ctx, c.SendFile, &v1.SendFileRequest{RoomId: mx + "!a:x", UploadId: id})
	return code
}

// piece is one chunk a client writes: bytes [from, to) of the file.
type piece struct{ from, to int }

// Over generated files cut into uneven, overlapping pieces sent in any order, some
// twice: SendFile is refused while any piece is missing, wherever it is (a gap counts,
// not only the end), and once the last arrives the file is posted intact.
func TestChunksOutOfOrderAndRepeatedMakeTheFile(t *testing.T) {
	t.Parallel()
	b := &receiving{}
	h, staging := uploading(t, b)
	c := daemon.Client(h.path)
	ctx := t.Context()
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 0x0dd)) // #nosec G404 -- reproducible
		size := rng.IntN(4096) + 1
		_, want := randomFile(t, rng, "f.bin", size)
		var pieces []piece
		for from := 0; from < size; {
			to := min(size, from+rng.IntN(700)+1)
			pieces = append(pieces, piece{from, to})
			// An overlap: the next piece starts back inside this one.
			from = max(from+1, to-rng.IntN(50))
		}
		// One piece, anywhere in the file, comes last; the rest in any order, some twice.
		at := rng.IntN(len(pieces))
		last := pieces[at]
		rest := slices.Delete(slices.Clone(pieces), at, at+1)
		for _, p := range slices.Clone(rest) {
			if rng.IntN(3) == 0 {
				rest = append(rest, p)
			}
		}
		rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
		id := start(t, c, int64(size))
		covered := make([]bool, size)
		for _, p := range rest {
			if code := put(ctx, c, id, p.from, want[p.from:p.to]); code != 0 {
				t.Fatalf("seed %d: PutUpload [%d,%d) = %v", seed, p.from, p.to, code)
			}
			for i := p.from; i < p.to; i++ {
				covered[i] = true
			}
		}
		if slices.Contains(covered, false) {
			if code := sendUpload(ctx, c, id); code != connect.CodeFailedPrecondition {
				t.Fatalf("seed %d: SendFile with bytes missing = %v, want failed_precondition", seed, code)
			}
		}
		if code := put(ctx, c, id, last.from, want[last.from:last.to]); code != 0 {
			t.Fatalf("seed %d: the last piece = %v", seed, code)
		}
		if code := sendUpload(ctx, c, id); code != 0 {
			t.Fatalf("seed %d: SendFile = %v", seed, code)
		}
		got := b.posted()
		if p := got[len(got)-1]; !bytes.Equal(p.data, want) {
			t.Fatalf("seed %d: the posted file differs from the one sent", seed)
		}
	}
	if left := staged(t, staging); len(left) != 0 {
		t.Errorf("staged after sending: %v", left)
	}
}

// Several files sent at once, through one Remote and through several, each arrive
// intact and under their own names.
func TestUploadsAtOnceKeepTheirOwnBytes(t *testing.T) {
	t.Parallel()
	b := &receiving{}
	h, staging := uploading(t, b)
	shared := h.client()
	rng := rand.New(rand.NewPCG(3, 4)) // #nosec G404 -- reproducible
	const files = 6
	want := map[string][]byte{}
	paths := make([]string, files)
	for i := range files {
		name := fmt.Sprintf("f%d.bin", i)
		paths[i], want[name] = randomFile(t, rng, name, chunk/2+rng.IntN(2*chunk))
	}
	var wg sync.WaitGroup
	for i, path := range paths {
		r := shared
		if i%2 == 1 {
			r = h.client()
		}
		wg.Go(func() {
			if err := r.SendFile(t.Context(), mx+"!a:x", path, "", ""); err != nil {
				t.Errorf("SendFile %s: %v", filepath.Base(path), err)
			}
		})
	}
	wg.Wait()
	got := b.posted()
	if len(got) != files {
		t.Fatalf("posted %d files, want %d", len(got), files)
	}
	for _, p := range got {
		if !bytes.Equal(p.data, want[p.name]) {
			t.Errorf("%s arrived with another file's bytes, or a mix", p.name)
		}
	}
	if left := staged(t, staging); len(left) != 0 {
		t.Errorf("staged after sending: %v", left)
	}
}

// Two SendFiles of one upload at once post it once; the other finds no upload.
func TestAnUploadIsSentOnce(t *testing.T) {
	t.Parallel()
	b := &receiving{}
	h, _ := uploading(t, b)
	c := daemon.Client(h.path)
	for range 20 {
		id := start(t, c, 3)
		if code := put(t.Context(), c, id, 0, []byte("abc")); code != 0 {
			t.Fatal(code)
		}
		codes := make([]connect.Code, 2)
		var wg sync.WaitGroup
		for i := range codes {
			wg.Go(func() { codes[i] = sendUpload(t.Context(), c, id) })
		}
		wg.Wait()
		slices.Sort(codes)
		if codes[0] != 0 || codes[1] != connect.CodeNotFound {
			t.Fatalf("two sends of one upload = %v, want one sent and one not found", codes)
		}
	}
	if n := len(b.posted()); n != 20 {
		t.Errorf("posted %d, want 20", n)
	}
}

// The staged file is removed whether the post succeeds or fails, and the upload with
// it: sending it again finds nothing.
func TestTheStagedFileGoesWhetherThePostSucceedsOrNot(t *testing.T) {
	t.Parallel()
	for _, fail := range []error{nil, errors.New("the network refused it")} {
		b := &receiving{fail: fail}
		h, staging := uploading(t, b)
		c := daemon.Client(h.path)
		id := start(t, c, 5)
		if code := put(t.Context(), c, id, 0, []byte("hello")); code != 0 {
			t.Fatal(code)
		}
		if left := staged(t, staging); len(left) != 1 {
			t.Fatalf("staged before sending: %v, want the one file", left)
		}
		want := connect.Code(0)
		if fail != nil {
			want = connect.CodeInternal
		}
		if code := sendUpload(t.Context(), c, id); code != want {
			t.Errorf("fail %v: SendFile = %v, want %v", fail, code, want)
		}
		if got := b.posted(); len(got) != 1 || string(got[0].data) != "hello" {
			t.Errorf("fail %v: the backend got %v", fail, got)
		}
		if left := staged(t, staging); len(left) != 0 {
			t.Errorf("fail %v: staged after the post: %v", fail, left)
		}
		if code := sendUpload(t.Context(), c, id); code != connect.CodeNotFound {
			t.Errorf("fail %v: sent again = %v, want not found", fail, code)
		}
	}
}

// What kithd refuses: an upload it does not have, a chunk outside the file, a file
// over the cap, and a file the staging total has no room for until another is sent.
func TestUploadsRefuseWhatTheyCannotHold(t *testing.T) {
	t.Parallel()
	b := &receiving{}
	h, staging := uploading(t, b)
	c := daemon.Client(h.path)
	ctx := t.Context()
	const gib = 1 << 30

	if code := sendUpload(ctx, c, "no-such-upload"); code != connect.CodeNotFound {
		t.Errorf("SendFile of an unknown upload = %v, want not found", code)
	}
	if code := put(ctx, c, "no-such-upload", 0, []byte("x")); code != connect.CodeNotFound {
		t.Errorf("PutUpload to an unknown upload = %v, want not found", code)
	}
	id := start(t, c, 10)
	for _, p := range []struct {
		offset int
		data   string
	}{{-1, "x"}, {8, "xyz"}, {10, "x"}, {11, ""}} {
		if code := put(ctx, c, id, p.offset, []byte(p.data)); code != connect.CodeInvalidArgument {
			t.Errorf("PutUpload %d bytes at %d of 10 = %v, want invalid argument", len(p.data), p.offset, code)
		}
	}
	for _, size := range []int64{-1, 2*gib + 1} {
		if _, code := rpc(ctx, c.StartUpload, &v1.StartUploadRequest{Name: "big", Size: size}); code != connect.CodeInvalidArgument {
			t.Errorf("StartUpload(%d) = %v, want invalid argument", size, code)
		}
	}

	// 10 bytes staged: two more fill the 4 GiB to the byte.
	start(t, c, 2*gib)
	start(t, c, 2*gib-20)
	small := start(t, c, 10)
	if _, code := rpc(ctx, c.StartUpload, &v1.StartUploadRequest{Name: "one more", Size: 1}); code != connect.CodeResourceExhausted {
		t.Fatalf("StartUpload over the staging total = %v, want resource exhausted", code)
	}
	if code := put(ctx, c, small, 0, []byte("0123456789")); code != 0 {
		t.Fatal(code)
	}
	if code := sendUpload(ctx, c, small); code != 0 {
		t.Fatal(code)
	}
	start(t, c, 1) // the sent file's room is free again
	if n := len(staged(t, staging)); n != 4 {
		t.Errorf("%d files staged, want 4", n)
	}
}
