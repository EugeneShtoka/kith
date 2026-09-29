package matrix

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
)

// The msgtype comes from the sniffed content, not the extension.
func TestMsgTypeForMimetype(t *testing.T) {
	t.Parallel()

	tests := map[string]event.MessageType{
		"image/png":                event.MsgImage,
		"image/webp":               event.MsgImage,
		"video/mp4":                event.MsgVideo,
		"audio/ogg":                event.MsgAudio,
		"application/pdf":          event.MsgFile,
		"application/octet-stream": event.MsgFile,
		"":                         event.MsgFile,
	}
	for mimetype, want := range tests {
		if got := msgTypeFor(mimetype); got != want {
			t.Errorf("msgTypeFor(%q) = %v, want %v", mimetype, got, want)
		}
	}
}

// Content first, extension as fallback for application/octet-stream.
func TestSniffPrefersContentThenExtension(t *testing.T) {
	t.Parallel()

	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32)
	if got, err := sniff(strings.NewReader(png), "not-a-png.txt"); err != nil || got != "image/png" {
		t.Errorf("sniff(png bytes, .txt name) = %q, %v — the bytes are the evidence", got, err)
	}
	opaque := strings.Repeat("\x01\x02\x03\x04", 40)
	got, err := sniff(strings.NewReader(opaque), "invoice.pdf")
	if err != nil {
		t.Fatalf("sniff: %v", err)
	}
	if !strings.HasPrefix(got, "application/pdf") {
		t.Errorf("sniff(opaque, .pdf) = %q, want the extension to answer when the bytes cannot", got)
	}
}

// With a caption the body is the caption; without one it is the filename.
func TestAttachmentContentCaption(t *testing.T) {
	t.Parallel()

	plain := attachmentContent("photo.png", "", "image/png", 1234)
	if plain.Body != "photo.png" || plain.FileName != "" {
		t.Errorf("no caption: body = %q, filename = %q, want the name as the body", plain.Body, plain.FileName)
	}
	if plain.Info.MimeType != "image/png" || plain.Info.Size != 1234 {
		t.Errorf("info = %+v, want the mimetype and size a receiving client needs", plain.Info)
	}
	captioned := attachmentContent("photo.png", "look at this", "image/png", 1234)
	if captioned.Body != "look at this" || captioned.FileName != "photo.png" {
		t.Errorf("captioned: body = %q, filename = %q", captioned.Body, captioned.FileName)
	}
}

// Image dimensions come from the header; a headerless file still sends.
func TestAddDimensions(t *testing.T) {
	t.Parallel()

	// A 1x1 PNG, as bytes.
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
		"\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82"
	content := attachmentContent("dot.png", "", "image/png", int64(len(png)))
	addDimensions(strings.NewReader(png), &content)
	if content.Info.Width != 1 || content.Info.Height != 1 {
		t.Errorf("dimensions = %dx%d, want 1x1 from the header", content.Info.Width, content.Info.Height)
	}

	notAnImage := attachmentContent("invoice.pdf", "", "application/pdf", 10)
	addDimensions(strings.NewReader("%PDF-1.4"), &notAnImage)
	if notAnImage.Info.Width != 0 {
		t.Error("a non-image should get no dimensions rather than a guess")
	}
}
