//go:build signal

package meow

import (
	"testing"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"google.golang.org/protobuf/proto"
)

// A sticker goes as its picture, a WebP when Signal names no type; one with no picture
// reads as its emoji.
func TestAStickerGoesAsItsPicture(t *testing.T) {
	t.Parallel()
	drawn := &signalpb.DataMessage_Sticker{Emoji: new("🐱"), Data: &signalpb.AttachmentPointer{
		AttachmentIdentifier: &signalpb.AttachmentPointer_CdnKey{CdnKey: "k"}, Size: new(uint32(42)),
	}}
	files, body := sticker(drawn, nil, "")
	if len(files) != 1 || body != "" {
		t.Fatalf("files %v, body %q; want the picture alone", files, body)
	}
	f := files[0]
	var pointer signalpb.AttachmentPointer
	if err := proto.Unmarshal(f.GetPointer(), &pointer); err != nil || pointer.GetCdnKey() != "k" {
		t.Errorf("pointer %v (%v), want the sticker's", &pointer, err)
	}
	if f.GetContentType() != "image/webp" || f.GetFileName() != "sticker" || f.GetSize() != 42 {
		t.Errorf("attachment %v, want a 42-byte image/webp named sticker", f)
	}

	files, body = sticker(&signalpb.DataMessage_Sticker{Emoji: new("🐱")}, nil, "")
	if len(files) != 0 || body != "[sticker] 🐱" {
		t.Errorf("no picture: files %v, body %q; want [sticker] 🐱", files, body)
	}
}
