package meta

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"mime"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Register the extensions this test asserts about, so it does not depend on the host's
// shared-mime-info or /etc/mime.types — the upstream Go toolchain reads neither when
// those files are absent, as on NixOS without the Nix-patched Go.
func init() {
	_ = mime.AddExtensionType(".md", "text/markdown; charset=utf-8")
}

// pngOf is a w×h PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A file's kind and type come from its contents, then its name; a picture's size is
// read from it; it goes under the name it was given.
func TestAFileIsShownAsWhatItIs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sent string
		data       []byte
		kind       domain.MediaType
		mime       string
	}{
		{"photo.png", "photo.png", pngOf(t, 800, 600), domain.MediaImage, "image/png"},
		{"plan.pdf", "plan.pdf", []byte("%PDF-1.7\n"), domain.MediaFile, "application/pdf"},
		{"notes.md", "notes.md", []byte("# notes\n"), domain.MediaFile, "text/markdown; charset=utf-8"},
		{"blob.bin", "blob.bin", []byte{0, 1, 2, 3}, domain.MediaFile, "application/octet-stream"},
	} {
		m := fileMediaOf(tc.name, tc.data)
		if m.Type != tc.kind || m.Mime != tc.mime || m.Name != tc.sent || m.Size != len(tc.data) {
			t.Errorf("%s: %+v, want %q, %s %s", tc.name, m, tc.sent, tc.kind, tc.mime)
		}
	}
	if m := fileMediaOf("beach.png", pngOf(t, 800, 600)); m.Width != 800 || m.Height != 600 {
		t.Errorf("the picture's size: %dx%d", m.Width, m.Height)
	}
}

// What an encrypted chat is sent reads back as kith reads one received: its kind, its
// caption, its size, and the path and keys to open it.
func TestAnEncryptedFileReadsBackAsSent(t *testing.T) {
	t.Parallel()
	up := whatsmeow.UploadResponse{DirectPath: "/v/up", MediaKey: []byte("k"), FileSHA256: []byte("s"), FileEncSHA256: []byte("e"), ObjectID: "o"}
	for _, tc := range []struct {
		media   *domain.Media
		caption string
		kind    whatsmeow.MediaType
	}{
		{&domain.Media{Type: domain.MediaImage, Name: "beach.png", Mime: "image/png", Size: 10, Width: 800, Height: 600}, "the view", whatsmeow.MediaImage},
		{&domain.Media{Type: domain.MediaVideo, Name: "clip.mp4", Mime: "video/mp4", Size: 20}, "watch", whatsmeow.MediaVideo},
		{&domain.Media{Type: domain.MediaAudio, Name: "note.ogg", Mime: "audio/ogg", Size: 30}, "", whatsmeow.MediaAudio},
		{&domain.Media{Type: domain.MediaFile, Name: "plan.pdf", Mime: "application/pdf", Size: 40}, "", whatsmeow.MediaDocument},
	} {
		ca, err := encryptedFile(up, tc.media, tc.caption)
		if err != nil {
			t.Fatalf("%s: %v", tc.media.Name, err)
		}
		got, source, caption := encryptedMedia(ca.GetPayload().GetContent())
		if got == nil || got.Type != tc.media.Type || got.Mime != tc.media.Mime || got.Size != tc.media.Size || caption != tc.caption {
			t.Errorf("%s: read back as %+v, caption %q", tc.media.Name, got, caption)
			continue
		}
		if tc.media.Type == domain.MediaFile && got.Name != "plan.pdf" {
			t.Errorf("a file's name read back as %q", got.Name)
		}
		if uploadKind(tc.media.Type) != tc.kind {
			t.Errorf("%s: uploaded as %s, read back as %s: its keys would not open it", tc.media.Name, uploadKind(tc.media.Type), tc.kind)
		}
		if e := source.Encrypted; e == nil || e.Kind != tc.kind || e.Path != "/v/up" || string(e.Key) != "k" {
			t.Errorf("%s: source %+v", tc.media.Name, source.Encrypted)
		}
	}
}

// A thumbnail is at most 400 on a side, the picture's proportions kept; one of no
// known size is 400 square.
func TestThumbSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ w, h, ww, wh int }{
		{800, 600, 400, 300}, {600, 800, 300, 400}, {200, 100, 200, 100}, {0, 0, 400, 400}, {4000, 1, 400, 1},
	} {
		if w, h := thumbSize(tc.w, tc.h); int(w) != tc.ww || int(h) != tc.wh {
			t.Errorf("thumbSize(%d, %d) = %d×%d, want %d×%d", tc.w, tc.h, w, h, tc.ww, tc.wh)
		}
	}
}

// A file that cannot go says why and leaves nothing cached: no account connected, the
// connection still loading, encrypted chats not up, one too large.
func TestFilesThatCannotGo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.take(ctx, c, encryptedThread())
	a.take(ctx, c, &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(eli, table.ONE_TO_ONE, "Eli", "inbox", 10, 10, 0)}})
	dir := t.TempDir()
	small := filepath.Join(dir, "beach.png")
	if err := os.WriteFile(small, pngOf(t, 10, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(dir, "huge.bin")
	if err := os.WriteFile(huge, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(huge, mediaMost+1); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		room domain.RoomID
		path string
		want error
	}{
		{"a plain thread while the connection loads", roomID(me, eli), small, errNotConnected},
		{"an encrypted chat before encrypted chats connect", roomID(me, dana), small, errNoEncrypted},
		{"an account not signed in", roomID(me+1, eli), small, errNotConnected},
		{"a file too large", roomID(me, eli), huge, errFileTooLarge},
		{"another network's room", "!r:example.org", small, errNotAThread},
	} {
		if err := a.SendFile(ctx, tc.room, tc.path, filepath.Base(tc.path), "look"); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	for _, room := range []domain.RoomID{roomID(me, eli), roomID(me, dana)} {
		if msgs, err := cache.Messages(ctx, room, 10); err != nil || len(msgs) != 0 {
			t.Errorf("a file that did not go is cached in %s: %v (%v)", room, msgs, err)
		}
	}
}
