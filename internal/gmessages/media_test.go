package gmessages

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func mediaPart(id, mime, name string) *gmproto.MessageInfo {
	media := &gmproto.MediaContent{MediaID: id, MimeType: mime, MediaName: name, Size: 2048,
		Dimensions: &gmproto.Dimensions{Width: 640, Height: 480}}
	if id != "" {
		media.DecryptionKey = []byte("an invented key, 32 bytes long..")
	}
	return &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MediaContent{MediaContent: media}}
}

// A message shows its first attachment on Google's servers as its attachment, its text
// as the caption, and names the rest; one with no text is its attachment's name; one
// not on the servers stays a label. Where the shown one is, and its key, are kept
// beside the cached message, and loading it asks the phone's account.
func TestAnAttachmentShowsAndIsKept(t *testing.T) {
	t.Parallel()

	a, c, cache, _ := cachedAdapter(t)
	ctx := context.Background()
	a.takeConversations(ctx, c, []*gmproto.Conversation{active(danaDM, "", false, 10, false, pMe, pDana)})
	photo := &gmproto.Message{MessageID: "m1", ConversationID: danaDM, ParticipantID: "2", Timestamp: 1_000_000,
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE},
		MessageInfo:   []*gmproto.MessageInfo{mediaPart("media-1", "image/jpeg", "beach.jpg")}}
	captioned := &gmproto.Message{MessageID: "m2", ConversationID: danaDM, ParticipantID: "2", Timestamp: 2_000_000,
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE},
		MessageInfo: []*gmproto.MessageInfo{
			mediaPart("media-2", "video/mp4", ""), mediaPart("media-3", "image/png", "b.png"),
			{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: "from today"}}},
		}}
	unloaded := &gmproto.Message{MessageID: "m3", ConversationID: danaDM, ParticipantID: "2", Timestamp: 3_000_000,
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_COMPLETE},
		MessageInfo:   []*gmproto.MessageInfo{mediaPart("", "image/jpeg", "")}}

	in := c.inbox
	if msg, _ := in.message(photo); msg.Media == nil || msg.Media.Type != domain.MediaImage || msg.Media.Width != 640 ||
		msg.Body != "beach.jpg" || msg.Caption() != "" {
		t.Errorf("a photo: %+v %+v", msg, msg.Media)
	}
	if msg, _ := in.message(captioned); msg.Media == nil || msg.Media.Type != domain.MediaVideo || msg.Media.Name != "[video]" ||
		msg.Caption() != "[photo]\nfrom today" {
		t.Errorf("a captioned video: %q %+v", msg.Body, msg.Media)
	}
	if msg, _ := in.message(unloaded); msg.Media != nil || msg.Body != "[photo]" {
		t.Errorf("an attachment not on the servers: %q %+v", msg.Body, msg.Media)
	}

	a.takeMessages(ctx, c, []*gmproto.Message{photo, captioned, unloaded}, true)
	room := roomID(self, danaDM)
	for id, want := range map[string]string{"m1": "media-1", "m2": "media-2", "m3": ""} {
		_, raw, ok, err := cache.MediaSource(ctx, room, eventID(self, danaDM, id))
		var src mediaSource
		if ok {
			err = errors.Join(err, json.Unmarshal([]byte(raw), &src))
		}
		if err != nil || src.ID != want || (want != "" && len(src.Key) == 0) {
			t.Errorf("%s's source: %+v (%v, %v)", id, src, ok, err)
		}
	}
	if _, err := a.LoadImage(ctx, room, eventID(self, danaDM, "m3")); !errors.Is(err, errNoMedia) {
		t.Errorf("loading what is not on the servers: %v", err)
	}
	if _, err := a.LoadImage(ctx, room, eventID(self, danaDM, "m1")); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("loading with the phone's account not connected: %v", err)
	}
}

// A file's content type comes from its contents, then its name, without parameters.
func TestAFilesTypeIsRead(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"photo.bin":  {[]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), "image/jpeg"},
		"notes.pdf":  {[]byte("plain words"), "application/pdf"},
		"notes":      {[]byte("plain words"), "text/plain"},
		"blob.nokey": {[]byte{0, 1, 2}, "application/octet-stream"},
	} {
		if got := fileMedia(name, tc.data); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
