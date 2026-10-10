package telegram

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An attachment is shown as what it is: a photo by its largest size, a voice note, a
// song by its name, a video (a round one, a GIF), a still sticker as an image, an
// image sent as a file as an image, anything else as a file under its name. What
// kith cannot draw (an animated sticker, a location) reads as a label.
func TestAttachmentsAreShownAsWhatTheyAre(t *testing.T) {
	t.Parallel()
	doc := func(mimeType string, attrs ...tg.DocumentAttributeClass) tg.MessageMediaClass {
		return &tg.MessageMediaDocument{Document: &tg.Document{MimeType: mimeType, Size: 900, Attributes: attrs}}
	}
	for name, c := range map[string]struct {
		media tg.MessageMediaClass
		want  *domain.Media
		label string
	}{
		"photo": {&tg.MessageMediaPhoto{Photo: &tg.Photo{Sizes: []tg.PhotoSizeClass{
			&tg.PhotoStrippedSize{Type: "i"},
			&tg.PhotoSize{Type: "m", W: 320, H: 240, Size: 100},
			&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 960, Sizes: []int{10, 500}},
		}}}, &domain.Media{Type: domain.MediaImage, Mime: "image/jpeg", Width: 1280, Height: 960, Size: 500}, ""},
		"voice": {doc("audio/ogg", &tg.DocumentAttributeAudio{Voice: true}), &domain.Media{Type: domain.MediaAudio, Name: domain.VoiceMessage, Mime: "audio/ogg", Size: 900}, ""},
		"song":  {doc("audio/mpeg", &tg.DocumentAttributeAudio{Performer: "Band", Title: "Song"}), &domain.Media{Type: domain.MediaAudio, Name: "Band Song", Mime: "audio/mpeg", Size: 900}, ""},
		"video": {doc("video/mp4", &tg.DocumentAttributeVideo{W: 640, H: 360}, &tg.DocumentAttributeFilename{FileName: "clip.mp4"}),
			&domain.Media{Type: domain.MediaVideo, Name: "clip.mp4", Mime: "video/mp4", Width: 640, Height: 360, Size: 900}, ""},
		"gif":              {doc("video/mp4", &tg.DocumentAttributeAnimated{}), &domain.Media{Type: domain.MediaVideo, Mime: "video/mp4", Size: 900}, ""},
		"still sticker":    {doc("image/webp", &tg.DocumentAttributeSticker{Alt: "😀"}, &tg.DocumentAttributeImageSize{W: 512, H: 512}), &domain.Media{Type: domain.MediaImage, Mime: "image/webp", Width: 512, Height: 512, Size: 900}, ""},
		"animated sticker": {doc("application/x-tgsticker", &tg.DocumentAttributeSticker{Alt: "😀"}), nil, "[sticker] 😀"},
		"image as a file":  {doc("image/png", &tg.DocumentAttributeFilename{FileName: "a.png"}), &domain.Media{Type: domain.MediaImage, Name: "a.png", Mime: "image/png", Size: 900}, ""},
		"file":             {doc("application/pdf", &tg.DocumentAttributeFilename{FileName: "a.pdf"}), &domain.Media{Type: domain.MediaFile, Name: "a.pdf", Mime: "application/pdf", Size: 900}, ""},
		"location":         {&tg.MessageMediaGeo{}, nil, "[location]"},
		"link preview":     {&tg.MessageMediaWebPage{}, nil, ""},
	} {
		got, label := attachment(c.media)
		if label != c.label || (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: (%+v, %q), want (%+v, %q)", name, got, label, c.want, c.label)
		}
	}
}

// An attachment is downloaded by the file reference Telegram gives the message now:
// the message is asked for again, not a reference kept from when it arrived.
func TestAnAttachmentIsDownloadedByAFreshReference(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	d := f.cluster.Dispatch(2, "dc2")
	content := bytes.Repeat([]byte("voice"), 100)
	d.HandleFunc(tg.MessagesGetMessagesRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		return sendResult(s, r, &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{
			ID: 5, PeerID: &tg.PeerUser{UserID: 7}, Date: 1000,
			Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 9, AccessHash: 90, FileReference: []byte("fresh"), MimeType: "audio/ogg", Size: int64(len(content)),
				Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}}},
		}}, Users: []tg.UserClass{dana}})
	})
	var mu sync.Mutex
	var reference string
	d.HandleFunc(tg.UploadGetFileRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.UploadGetFileRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		if loc, ok := req.Location.(*tg.InputDocumentFileLocation); ok {
			mu.Lock()
			reference = string(loc.FileReference)
			mu.Unlock()
		}
		part := content[min(int(req.Offset), len(content)):min(int(req.Offset)+req.Limit, len(content))]
		return sendResult(s, r, &tg.UploadFile{Type: &tg.StorageFilePartial{}, Bytes: part, Mtime: 1})
	})
	st := openStore(t)
	knowDana(t, st)
	a, _ := loggedInWithStore(t, f, st)
	got, err := a.LoadImage(t.Context(), "telegram:42/7", "telegram:42/7/5")
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("loaded %d bytes, %v; want %d", len(got), err, len(content))
	}
	mu.Lock()
	defer mu.Unlock()
	if reference != "fresh" {
		t.Errorf("downloaded by reference %q", reference)
	}
}

// A file sent is uploaded and goes as a photo when it is an image, else as a document
// under its name; it is cached with its attachment as Telegram answers it.
func TestAFileSentGoesAsWhatItIs(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	d := f.cluster.Dispatch(2, "dc2")
	d.HandleFunc(tg.UploadSaveFilePartRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		return sendResult(s, r, &tg.BoolTrue{})
	})
	var mu sync.Mutex
	var sent []tg.InputMediaClass
	next := 20
	d.HandleFunc(tg.MessagesSendMediaRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesSendMediaRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		sent = append(sent, req.Media)
		next++
		id := next
		mu.Unlock()
		var media tg.MessageMediaClass = &tg.MessageMediaDocument{Document: &tg.Document{ID: 1, MimeType: "text/plain", Size: 5,
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "notes.txt"}}}}
		if _, ok := req.Media.(*tg.InputMediaUploadedPhoto); ok {
			media = &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 2, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 2, H: 2, Size: 70}}}}
		}
		return sendResult(s, r, &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: id, RandomID: req.RandomID},
			&tg.UpdateNewMessage{Message: &tg.Message{ID: id, Out: true, PeerID: &tg.PeerUser{UserID: 7}, Message: req.Message, Date: int(time.Now().Unix()), Media: media}, Pts: 1 + id - 20, PtsCount: 1},
		}, Users: []tg.UserClass{dana, f.user}, Date: int(time.Now().Unix())})
	})
	st := openStore(t)
	knowDana(t, st)
	a, cache := loggedInWithStore(t, f, st)
	dir := t.TempDir()
	for name, data := range map[string][]byte{"dot.png": {0x89, 'P', 'N', 'G'}, "notes.txt": []byte("hello")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, room := t.Context(), domain.RoomID("telegram:42/7")
	if err := a.SendFile(ctx, room, filepath.Join(dir, "dot.png"), "**look**"); err != nil {
		t.Fatal(err)
	}
	if err := a.SendFile(ctx, room, filepath.Join(dir, "notes.txt"), ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	photo, isPhoto := sent[0].(*tg.InputMediaUploadedPhoto)
	file, isFile := sent[1].(*tg.InputMediaUploadedDocument)
	mu.Unlock()
	if !isPhoto || photo.File == nil {
		t.Errorf("the image went as %T", sent[0])
	}
	if !isFile || file.MimeType != "text/plain" || len(file.Attributes) != 1 {
		t.Errorf("the file went as %+v", sent[1])
	}
	msgs, _ := cache.Messages(ctx, room, 10)
	if len(msgs) != 2 || !msgs[0].Media.IsImage() || msgs[0].Body != "look" || msgs[1].Media == nil || msgs[1].Body != "notes.txt" {
		t.Errorf("cached %+v", msgs)
	}
}
