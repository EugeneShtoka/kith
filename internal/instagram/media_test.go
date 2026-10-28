package instagram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// uploadPath is where Instagram's web app uploads a file.
const uploadPath = "/ajax/mercury/upload.php"

// upload answers a file upload with f.data["upload"] (the upload's metadata), keeping
// the file's name, type and size.
func (f *instagram) upload(req *http.Request) (*http.Response, error) {
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	vars := map[string]any{}
	parts := multipart.NewReader(req.Body, params["boundary"])
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if part.FileName() != "" {
			data, err := io.ReadAll(part)
			if err != nil {
				return nil, err
			}
			vars["filename"], vars["type"], vars["size"] = part.FileName(), part.Header.Get("Content-Type"), len(data)
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, igCall{"upload", vars})
	answer := "for (;;);" + f.data["upload"]
	f.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(answer)), Request: req,
	}, nil
}

// Attachments are kept as kith shows them, with where their bytes are: a photo's or
// video's first file (the rest counted), a voice message, a sticker and a GIF as
// Instagram's web app plays them; a view-once photo stays in the app.
func TestAttachmentsKeepWhereTheyAre(t *testing.T) {
	t.Parallel()
	msg := func(c slidetypes.MessageContent) *slidetypes.Message {
		return &slidetypes.Message{ID: "m1", Content: slidetypes.MessageContentWrapper{Content: c}}
	}
	cases := []struct {
		name   string
		msg    *slidetypes.Message
		kind   domain.MediaType
		media  string
		url    string
		mime   string
		body   string
		noneOK bool
	}{
		{"photos", msg(&slidetypes.MessageContentMultiMedia{Attachments: []*slidetypes.Attachment{
			{AttachmentCDNURL: "https://cdn.test/p1", PreviewCDNURL: "https://cdn.test/p1s", PreviewWidth: 720, PreviewHeight: 960}, {},
		}}), domain.MediaImage, "photo", "https://cdn.test/p1", photoMime, "(+1 more)", false},
		{"a photo with only its preview", msg(&slidetypes.MessageContentImage{Attachments: []*slidetypes.Attachment{
			{PreviewCDNURL: "https://cdn.test/p2s"},
		}}), domain.MediaImage, "photo", "https://cdn.test/p2s", photoMime, "", false},
		{"a video", msg(&slidetypes.MessageContentVideo{Videos: []*slidetypes.Attachment{{AttachmentCDNURL: "https://cdn.test/v"}}}),
			domain.MediaVideo, "video", "https://cdn.test/v", videoMime, "", false},
		{"a voice message", msg(&slidetypes.MessageContentAudio{AudioAttachments: []*slidetypes.AudioAttachment{{AttachmentCDNURL: "https://cdn.test/a"}}}),
			domain.MediaAudio, domain.VoiceMessage, "https://cdn.test/a", voiceMime, "", false},
		{"a sticker", msg(&slidetypes.MessageContentAnimatedMedia{AnimatedMedia: []*slidetypes.AnimatedAttachment{
			{IsSticker: true, AttachmentWebpURL: "https://cdn.test/s.webp", AttachmentMP4URL: "https://cdn.test/s.mp4", AltText: "waving cat"},
		}}), domain.MediaImage, "sticker", "https://cdn.test/s.webp", stickerMime, "waving cat", false},
		{"a GIF", msg(&slidetypes.MessageContentAnimatedMedia{AnimatedMedia: []*slidetypes.AnimatedAttachment{
			{AttachmentWebpURL: "https://cdn.test/g.webp", AttachmentMP4URL: "https://cdn.test/g.mp4"},
		}}), domain.MediaVideo, "GIF", "https://cdn.test/g.mp4", videoMime, "", false},
		{"a store sticker", msg(&slidetypes.MessageContentSticker{PreviewURL: "https://cdn.test/st", AltText: "heart"}),
			domain.MediaImage, "sticker", "https://cdn.test/st", "", "heart", false},
		{"a view-once photo", msg(&slidetypes.MessageContentRavenImage{}), "", "", "", "", "Instagram app", true},
	}
	for _, c := range cases {
		m, source, ok := message(self, danaChat, c.msg)
		if !ok {
			t.Errorf("%s: not shown", c.name)
			continue
		}
		if c.noneOK {
			if m.Media != nil || source.URL != "" || !strings.Contains(m.Body, c.body) {
				t.Errorf("%s: %+v, %+v", c.name, m, source)
			}
			continue
		}
		if m.Media == nil || m.Media.Type != c.kind || m.Media.Name != c.media || source.URL != c.url || source.Mime != c.mime ||
			!strings.Contains(m.Body, c.body) {
			t.Errorf("%s: media %+v, source %+v, body %q", c.name, m.Media, source, m.Body)
		}
	}
}

// cdn is Instagram's media servers: each path's bytes, an expired address refused.
type cdn struct {
	*httptest.Server
	asked atomic.Int32
}

func newCDN(t *testing.T, files map[string]string) *cdn {
	t.Helper()
	c := &cdn{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.asked.Add(1)
		data, ok := files[r.URL.Path]
		if !ok {
			http.Error(w, "URL signature expired", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, data)
	}))
	t.Cleanup(c.Close)
	return c
}

// photoAt is Dana's photo m1, at url.
func photoAt(url string) *slidetypes.Message {
	return &slidetypes.Message{
		ID: "m1", SenderFBID: danaFBID, TimestampMS: ms(time.Minute),
		Content: slidetypes.MessageContentWrapper{TypeName: "SlideMessageImageContent", Content: &slidetypes.MessageContentImage{
			Attachments: []*slidetypes.Attachment{{AttachmentCDNURL: url}},
		}},
	}
}

// threadPage is Instagram's answer to fetching a thread: its newest page, the photo m1
// at url.
func threadPage(url string) string {
	return fmt.Sprintf(`{"get_slide_thread_nullable":{"as_ig_direct_thread":{"slide_messages":{"edges":[{"node":{
		"id":"m1","sender_fbid":"%d","timestamp_ms":"%d",
		"content":{"__typename":"SlideMessageImageContent","attachments":[{"attachment_cdn_url":%q}]}}}]}}}}`,
		danaFBID, t0.Add(time.Minute).UnixMilli(), url)
}

// An attachment loads from where Instagram said it is. An address Instagram's servers
// refuse (they expire) is looked up again in the thread's newest page, while connected.
func TestAnAttachmentLoadsAgainOnceItsAddressExpired(t *testing.T) {
	t.Parallel()
	servers := newCDN(t, map[string]string{"/fresh": "the photo"})
	r := newRig(t)
	r.thread(dm())
	room, event, ctx := roomID(self, danaChat), eventID(self, danaChat, "m1"), context.Background()
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: photoAt(servers.URL + "/fresh")})
	if data, err := r.a.LoadImage(ctx, room, event); err != nil || string(data) != "the photo" {
		t.Fatalf("loaded %q, %v", data, err)
	}

	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: photoAt(servers.URL + "/expired")})
	if _, err := r.a.LoadImage(ctx, room, event); err == nil {
		t.Errorf("an expired address while not connected: %v", err)
	}
	f := r.connect()
	f.data["IGDThreadDetailQuery"] = threadPage(servers.URL + "/fresh")
	if data, err := r.a.LoadImage(ctx, room, event); err != nil || string(data) != "the photo" {
		t.Fatalf("after the address expired: %q, %v", data, err)
	}
	if got := f.called("IGDThreadDetailQuery"); len(got) != 1 || field(got[0], "thread_fbid") != danaChat {
		t.Errorf("looked up %v", got)
	}

	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: photoAt(servers.URL + "/expired")})
	f.data["IGDThreadDetailQuery"] = threadPage(servers.URL + "/expired")
	before := servers.asked.Load()
	if _, err := r.a.LoadImage(ctx, room, event); err == nil || !errors.Is(err, errMediaRefused) {
		t.Errorf("an address the thread's page does not renew: %v", err)
	}
	if n := servers.asked.Load() - before; n != 1 {
		t.Errorf("an address the page did not renew was asked for %d times", n)
	}
	r.delta(danaChat, &slidetypes.NewMessageEvent{Message: text("m2", danaFBID, 2*time.Minute, "no photo")})
	if _, err := r.a.LoadImage(ctx, room, eventID(self, danaChat, "m2")); !errors.Is(err, errNoMedia) {
		t.Errorf("a message with no attachment: %v", err)
	}
}

// pngOf is a w×h picture.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A file is uploaded, then sent naming the upload in its thread; it is cached at once
// as the account's, with its kind and size, and the caption goes after it. An upload
// Instagram names no file for sends nothing.
func TestAFileIsUploadedThenSent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	f := r.connect()
	r.thread(dm())
	room, ctx := roomID(self, danaChat), context.Background()
	path := filepath.Join(t.TempDir(), "IMG_0001.png")
	if err := os.WriteFile(path, pngOf(t, 40, 30), 0o600); err != nil {
		t.Fatal(err)
	}

	f.data["upload"] = `{"payload":{"metadata":[{"image_id":7001}]}}`
	f.data["IGDirectMediaSendMutation"] = `{"xig_direct_media_send_with_slide_messaging_response":{"id":"m5","timestamp_ms":"` +
		fmt.Sprint(t0.Add(5*time.Minute).UnixMilli()) + `"}}`
	f.data[sendText] = sent("m6", 6*time.Minute)
	if err := r.a.SendFile(ctx, room, path, "beach.png", " the beach "); err != nil {
		t.Fatal(err)
	}
	up := f.called("upload")
	if len(up) != 1 || up[0]["filename"] != "beach.png" || up[0]["type"] != "image/png" {
		t.Fatalf("uploaded %v", up)
	}
	sends := f.called("IGDirectMediaSendMutation")
	if len(sends) != 1 || field(sends[0], "attachment_fbid") != "7001" || field(sends[0], "thread_id") != danaChat ||
		field(sends[0], "offline_threading_id") == "" {
		t.Fatalf("sent %v", sends)
	}
	got, ok, err := r.cache.MessageByID(ctx, room, eventID(self, danaChat, "m5"))
	if err != nil || !ok || got.Sender != person(self) || got.Media == nil || got.Media.Type != domain.MediaImage ||
		got.Media.Width != 40 || got.Media.Height != 30 || got.Media.Name != "beach.png" || !got.Timestamp.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("cached %+v (%v, %v)", got, ok, err)
	}
	if texts := f.called(sendText); len(texts) != 1 || field(texts[0], "text.sensitive_string_value") != "the beach" {
		t.Errorf("caption sent as %v", texts)
	}

	f.data["upload"] = `{"payload":{"metadata":[{"filename":"beach.png"}]}}`
	if err := r.a.SendFile(ctx, room, path, "beach.png", ""); !errors.Is(err, errUploadNoID) {
		t.Errorf("an upload naming no file: %v", err)
	}
	if n := len(f.called("IGDirectMediaSendMutation")); n != 1 {
		t.Errorf("sent after an upload naming no file: %d sends", n)
	}

	r.c.mu.Lock()
	r.c.client = nil
	r.c.mu.Unlock()
	if err := r.a.SendFile(ctx, room, path, "beach.png", ""); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("not connected: %v", err)
	}
}
