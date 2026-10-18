package signal_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// carrying is a message with attachments, each at its pointer.
func carrying(ev *signalv1.EventsResponse, pointers ...string) *signalv1.EventsResponse {
	for _, p := range pointers {
		ev.GetMessage().Attachments = append(ev.GetMessage().Attachments, &signalv1.Attachment{
			ContentType: "image/jpeg", FileName: p + ".jpg", Size: 1024, Pointer: []byte(p),
		})
	}
	return ev
}

// uploads is every Upload the helper was asked for, in order.
func (r *rig) uploads() []*signalv1.UploadRequest {
	b, _ := os.ReadFile(r.sent)
	var out []*signalv1.UploadRequest
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		req := &signalv1.UploadRequest{}
		if line != "" && protojson.Unmarshal([]byte(line), req) == nil && req.GetFileName() != "" {
			out = append(out, req)
		}
	}
	return out
}

// An attachment loads through the helper, by the pointer its message came with; the
// first of several shows and the rest are counted. One Signal no longer has says so.
func TestAnAttachmentLoadsThroughTheHelper(t *testing.T) {
	r := connected(t,
		carrying(msg(dana, dana, 1700000000100, "the trail"), "p1", "p2"),
		carrying(msg(dana, dana, 1700000000200, ""), "gone"),
		msg(dana, dana, 1700000000300, "no photo"),
	)
	ctx, room := context.Background(), direct(dana)
	event := func(sent string) domain.EventID {
		return domain.EventID(string(room) + "/" + dana + "." + sent)
	}
	msgs := r.messages(room)
	if len(msgs) != 3 || msgs[0].Media == nil || msgs[0].Media.Type != domain.MediaImage || msgs[0].Media.Name != "p1.jpg" ||
		!strings.Contains(msgs[0].Body, "+1 more") {
		t.Fatalf("cached %+v", msgs)
	}
	if data, err := r.a.LoadImage(ctx, room, event("1700000000100")); err != nil || string(data) != "bytes of p1" {
		t.Errorf("loaded %q, %v", data, err)
	}
	if _, err := r.a.LoadImage(ctx, room, event("1700000000200")); err == nil || !strings.Contains(err.Error(), "for a while only") {
		t.Errorf("a gone attachment: %v", err)
	}
	if _, err := r.a.LoadImage(ctx, room, event("1700000000300")); err == nil {
		t.Error("loaded an attachment from a message without one")
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

// A file is uploaded with its name, type and a picture's size, then sent carrying it,
// with the caption as its text and the chat's timer; it is cached as the account's
// own, and loads back. Without a connection nothing is uploaded.
func TestAFileIsUploadedThenSent(t *testing.T) {
	r := connected(t, timed(msg(dana, dana, 1700000000100, "send the photo?"), hour, 2))
	ctx, room := context.Background(), direct(dana)
	path := filepath.Join(t.TempDir(), "IMG_0001.png")
	if err := os.WriteFile(path, pngOf(t, 40, 30), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.a.SendFile(ctx, room, path, "beach.png", " the beach "); err != nil {
		t.Fatal(err)
	}
	ups := r.uploads()
	if len(ups) != 1 || ups[0].GetAccount() != me || ups[0].GetFileName() != "beach.png" || ups[0].GetContentType() != "image/png" ||
		ups[0].GetWidth() != 40 || ups[0].GetHeight() != 30 {
		t.Fatalf("uploaded %v", ups)
	}
	sends := r.sends()
	if len(sends) != 1 || sends[0].GetBody() != "the beach" || len(sends[0].GetAttachments()) != 1 ||
		string(sends[0].GetAttachments()[0].GetPointer()) != "up:beach.png" || sends[0].GetTimer() != hour || sends[0].GetTimerVersion() != 2 {
		t.Fatalf("sent %v", sends)
	}
	msgs := r.messages(room)
	own := msgs[len(msgs)-1]
	if own.Sender != "signal:"+me || own.Media == nil || own.Media.Type != domain.MediaImage || own.Body != "the beach" {
		t.Fatalf("cached %+v", own)
	}
	if data, err := r.a.LoadImage(ctx, room, own.ID); err != nil || string(data) != "bytes of up:beach.png" {
		t.Errorf("own file loaded %q, %v", data, err)
	}

	gid := domain.RoomID("signal:" + other + "/" + base64.RawURLEncoding.EncodeToString([]byte{1}))
	if err := r.a.SendFile(ctx, gid, path, "beach.png", ""); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("an account not connected: %v", err)
	}
	if n := len(r.uploads()); n != 1 {
		t.Errorf("uploaded without a connection: %d uploads", n)
	}
}
