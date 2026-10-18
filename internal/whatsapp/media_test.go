package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A photo becomes an image attachment whose kept source decodes to the same keys.
func TestAPhotoIsKeptToLoadLater(t *testing.T) {
	t.Parallel()
	photo := &waE2E.ImageMessage{
		Mimetype: new("image/jpeg"), Width: new(uint32(640)), Height: new(uint32(480)), FileLength: new(uint64(1234)),
		DirectPath: new("/v/t62/abc"), MediaKey: []byte("0123456789abcdef0123456789abcdef"),
	}
	media, source := attachment(&waE2E.Message{ImageMessage: photo})
	if media == nil || media.Type != domain.MediaImage || media.Width != 640 || media.Size != 1234 || source == nil {
		t.Fatalf("attachment = %+v, %+v", media, source)
	}
	part, err := source.downloadable()
	if err != nil {
		t.Fatal(err)
	}
	if got := part.(*waE2E.ImageMessage); got.GetDirectPath() != "/v/t62/abc" || !bytes.Equal(got.GetMediaKey(), photo.GetMediaKey()) {
		t.Errorf("decoded %v", got)
	}
	doc, _ := attachment(&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: new("plan.pdf")}})
	if doc == nil || doc.Type != domain.MediaFile || doc.Name != "plan.pdf" {
		t.Errorf("a document = %+v", doc)
	}
	if none, _ := attachment(&waE2E.Message{Conversation: new("hi")}); none != nil {
		t.Errorf("text has an attachment: %+v", none)
	}
	if _, err := (mediaSource{Kind: "hologram", Proto: ""}).downloadable(); err == nil {
		t.Error("an unknown kind decoded")
	}
}

// A voice note is labeled as one; a shared audio file is unnamed; both are playable.
func TestAVoiceNoteIsCalledOne(t *testing.T) {
	t.Parallel()
	note, _ := attachment(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: new("audio/ogg; codecs=opus"), PTT: new(true)}})
	if !note.IsAudio() || note.Name != domain.VoiceMessage {
		t.Errorf("a voice note = %+v", note)
	}
	song, _ := attachment(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: new("audio/mpeg")}})
	if !song.IsAudio() || song.Name != "" {
		t.Errorf("an audio file = %+v", song)
	}
}

// A photo arrives loadable; loading needs a connection and errors for no attachment.
func TestAPhotoArrivesLoadable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	e := danaWrites("3EB0P", "")
	e.Message = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: new("the view"), Mimetype: new("image/jpeg"), DirectPath: new("/v/1")}}
	a.onMessage(ctx, account, client, e)

	msgs, _ := cache.Messages(ctx, danaChat, 10)
	if len(msgs) != 1 || msgs[0].Body != "the view" || !msgs[0].Media.IsImage() {
		t.Fatalf("cached %+v", msgs)
	}
	_, fileJSON, ok, err := cache.MediaSource(ctx, danaChat, msgs[0].ID)
	var kept mediaSource
	if err != nil || !ok || json.Unmarshal([]byte(fileJSON), &kept) != nil || kept.Kind != kindImage {
		t.Errorf("source = (%q, %v, %v)", fileJSON, ok, err)
	}
	if _, err := a.LoadImage(ctx, danaChat, msgs[0].ID); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("loading with nothing connected = %v", err)
	}
	a.onMessage(ctx, account, client, danaWrites("3EB0Q", "just words"))
	if _, err := a.LoadImage(ctx, danaChat, "whatsapp:"+ownDigits+"/3EB0Q"); err == nil || errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("loading a message with no attachment = %v", err)
	}
}

// A picture goes with its size and a small JPEG preview, so the phone shows it before
// it downloads; one that cannot be decoded still goes, without them.
func TestAPictureGoesWithAPreview(t *testing.T) {
	t.Parallel()
	picture := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	for i := range picture.Pix {
		picture.Pix[i] = 0xff
	}
	picture.Set(0, 0, color.Black)
	var data bytes.Buffer
	if err := png.Encode(&data, picture); err != nil {
		t.Fatal(err)
	}
	msg, _ := fileMessage("shot.png", data.Bytes(), "")
	m := msg.GetImageMessage()
	if m.GetWidth() != 200 || m.GetHeight() != 100 {
		t.Errorf("size = %dx%d, want 200x100", m.GetWidth(), m.GetHeight())
	}
	preview, err := jpeg.Decode(bytes.NewReader(m.GetJPEGThumbnail()))
	if err != nil {
		t.Fatalf("preview is no JPEG: %v", err)
	}
	if got := preview.Bounds().Size(); got != image.Pt(72, 36) {
		t.Errorf("preview is %v, want 72x36", got)
	}
	msg, _ = fileMessage("broken.png", []byte("\x89PNG\r\n\x1a\n0000"), "")
	if m := msg.GetImageMessage(); m == nil || m.JPEGThumbnail != nil || m.Width != nil {
		t.Errorf("an undecodable picture = %+v, want an image without preview or size", m)
	}
}

// A file is sent as image, video or named document by type, carrying the upload keys.
func TestAFileGoesOutAsWhatItIs(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\n0000")
	for _, c := range []struct {
		name string
		data []byte
		want whatsmeow.MediaType
	}{
		{"shot.png", png, whatsmeow.MediaImage},
		{"noext", png, whatsmeow.MediaImage},
		{"clip.mp4", []byte("x"), whatsmeow.MediaVideo},
		{"note.ogg", []byte("x"), whatsmeow.MediaAudio},
		{"plan.pdf", []byte("%PDF-1.7"), whatsmeow.MediaDocument},
		{"drawing.svg", []byte("<svg/>"), whatsmeow.MediaDocument},
	} {
		msg, kind := fileMessage(c.name, c.data, "caption")
		if kind != c.want {
			t.Errorf("%s goes as %s, want %s", c.name, kind, c.want)
		}
		withUpload(msg, whatsmeow.UploadResponse{URL: "u", DirectPath: "/p", MediaKey: []byte("k")})
		media, source := attachment(msg)
		if media == nil || source == nil {
			t.Errorf("%s: no attachment after upload", c.name)
		}
		if c.want == whatsmeow.MediaDocument && msg.GetDocumentMessage().GetFileName() != c.name {
			t.Errorf("%s: document named %q", c.name, msg.GetDocumentMessage().GetFileName())
		}
	}
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatal(err)
	}
	a, _, _ := offline(t, Account{Name: "home", Digits: ownDigits})
	if err := a.SendFile(context.Background(), danaChat, path, filepath.Base(path), ""); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("sending a file with nothing connected = %v", err)
	}
}
