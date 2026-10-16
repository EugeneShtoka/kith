package meta

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/useragent"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/armadilloutil"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// encryptedPhoto is an encrypted chat's photo, as the encrypted connection carries it.
func encryptedPhoto(t *testing.T, caption string) *waConsumerApplication.ConsumerApplication_Content {
	t.Helper()
	path, mime, size, width, height := "/v/t62/photo", "image/jpeg", uint64(2048), uint32(640), uint32(480)
	tr := &waMediaTransport.ImageTransport{
		Integral: &waMediaTransport.ImageTransport_Integral{Transport: &waMediaTransport.WAMediaTransport{
			Integral: &waMediaTransport.WAMediaTransport_Integral{
				MediaKey: []byte("key"), FileSHA256: []byte("sha"), FileEncSHA256: []byte("enc"), DirectPath: &path,
			},
			Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{Mimetype: &mime, FileLength: &size},
		}},
		Ancillary: &waMediaTransport.ImageTransport_Ancillary{Width: &width, Height: &height},
	}
	sub, err := armadilloutil.Marshal(tr, waConsumerApplication.ImageTransportVersion)
	if err != nil {
		t.Fatal(err)
	}
	img := &waConsumerApplication.ConsumerApplication_ImageMessage{Image: sub}
	if caption != "" {
		img.Caption = textContent(caption).GetMessageText()
	}
	return &waConsumerApplication.ConsumerApplication_Content{Content: &waConsumerApplication.ConsumerApplication_Content_ImageMessage{ImageMessage: img}}
}

// sourceOf is the source kept for a message's attachment.
func sourceOf(t *testing.T, a *Adapter, room domain.RoomID, event domain.EventID) (mediaSource, bool) {
	t.Helper()
	_, raw, ok, err := a.cache.MediaSource(context.Background(), room, event)
	if err != nil {
		t.Fatal(err)
	}
	var s mediaSource
	if ok && json.Unmarshal([]byte(raw), &s) != nil {
		t.Fatalf("source %q does not read back", raw)
	}
	return s, ok
}

// A plain thread's attachment and an encrypted chat's are shown as media, their
// caption as the text, and where to fetch each is kept once the message is cached:
// an address for a plain one, the path and keys for an encrypted one.
func TestAttachmentsAreShownAndTheirSourceKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.take(ctx, c, encryptedThread())
	plainRoom := roomID(me, eli)
	m := live(eli, eli, "mid.$p", "", 1000)
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(eli, table.ONE_TO_ONE, "Eli", "inbox", 1000, 0, 0)},
		LSInsertMessage:          []*table.LSInsertMessage{m},
		LSInsertBlobAttachment: []*table.LSInsertBlobAttachment{{
			ThreadKey: eli, MessageId: "mid.$p", AttachmentType: table.AttachmentTypeImage, Filename: "beach.jpg",
			PreviewUrl: "https://cdn.example/beach.jpg", PreviewUrlMimeType: "image/jpeg", PreviewWidth: 800, PreviewHeight: 600,
		}},
	})
	got, ok, err := cache.MessageByID(ctx, plainRoom, eventID(me, eli, "mid.$p"))
	if err != nil || !ok || got.Media == nil || got.Media.Type != domain.MediaImage || got.Media.Width != 800 || got.Body != "beach.jpg" {
		t.Fatalf("the plain photo cached as %+v (media %+v, %v, %v)", got, got.Media, ok, err)
	}
	if s, kept := sourceOf(t, a, plainRoom, got.ID); !kept || s.URL != "https://cdn.example/beach.jpg" || s.Encrypted != nil {
		t.Errorf("the plain photo's source: %+v (%v)", s, kept)
	}

	room := roomID(me, dana)
	a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(dana), "E1", time.UnixMilli(2000), encryptedPhoto(t, "the view")))
	got, ok, err = cache.MessageByID(ctx, room, eventID(me, dana, "E1"))
	if err != nil || !ok || got.Media == nil || got.Media.Type != domain.MediaImage || got.Media.Mime != "image/jpeg" || got.Media.Width != 640 || got.Body != "the view" {
		t.Fatalf("the encrypted photo cached as %+v (media %+v, %v, %v)", got, got.Media, ok, err)
	}
	s, ok := sourceOf(t, a, room, got.ID)
	if !ok || s.Encrypted == nil || s.Encrypted.Path != "/v/t62/photo" || string(s.Encrypted.Key) != "key" || s.Encrypted.Kind != whatsmeow.MediaImage {
		t.Errorf("the encrypted photo's source: %+v (%v)", s, ok)
	}
	if len(c.inbox.sources) != 0 {
		t.Errorf("sources left after caching: %v", c.inbox.sources)
	}
}

// A plain attachment loads from its address; one Meta's servers refuse, an expired
// link, is an error; a message with no source, or an encrypted one with its connection
// down, says why.
func TestLoadingAnAttachment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/expired" {
			http.Error(w, "gone", http.StatusForbidden)
			return
		}
		if r.Header.Get("User-Agent") != useragent.UserAgent {
			http.Error(w, "no browser", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("JPEG"))
	}))
	t.Cleanup(srv.Close)
	a, c, cache, _ := cachedAdapter(t)
	a.take(ctx, c, encryptedThread())
	room := roomID(me, eli)
	save := func(id domain.EventID, s mediaSource) {
		t.Helper()
		if err := cache.SaveMessages(ctx, room, []domain.Message{{ID: id, RoomID: room, Sender: personID(eli), Body: "photo", Media: &domain.Media{Type: domain.MediaImage}, Timestamp: time.UnixMilli(1)}}); err != nil {
			t.Fatal(err)
		}
		a.keepSource(ctx, domain.Message{ID: id, RoomID: room, Media: &domain.Media{}}, s)
	}
	save(eventID(me, eli, "mid.$ok"), mediaSource{URL: srv.URL + "/ok", Mime: "image/jpeg"})
	save(eventID(me, eli, "mid.$old"), mediaSource{URL: srv.URL + "/expired", Mime: "image/jpeg"})
	save(eventID(me, eli, "E2"), mediaSource{Mime: "image/jpeg", Encrypted: &encryptedSource{Kind: whatsmeow.MediaImage, Path: "/p", Key: []byte("k")}})
	if data, err := a.LoadImage(ctx, room, eventID(me, eli, "mid.$ok")); err != nil || string(data) != "JPEG" {
		t.Errorf("a plain photo: %q, %v", data, err)
	}
	if _, err := a.LoadImage(ctx, room, eventID(me, eli, "mid.$old")); !errors.Is(err, errMediaRefused) {
		t.Errorf("an expired link: %v, want refused", err)
	}
	if _, err := a.LoadImage(ctx, room, eventID(me, eli, "mid.$none")); !errors.Is(err, errNoMedia) {
		t.Errorf("a message with no source: %v, want none to load", err)
	}
	if _, err := a.LoadImage(ctx, room, eventID(me, eli, "E2")); !errors.Is(err, errNoEncrypted) {
		t.Errorf("an encrypted photo with encrypted chats not connected: %v", err)
	}
}
