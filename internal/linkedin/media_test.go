package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Each kind of attachment shows as kith's media, and the address to load it from is
// kept, for listed and live messages alike: a photo's largest version, a video's
// fullest stream; a file by its type; a further attachment is named in the text.
func TestAttachmentsShowAndKeepTheirSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	room := roomID(me, group)
	with := func(mid string, rc ...linkedingo.RenderContent) linkedingo.Message {
		m := message(group, mid, dana, "", 1000)
		m.RenderContent = rc
		return m
	}
	photo := with("p", linkedingo.RenderContent{VectorImage: &linkedingo.VectorImage{RootURL: "https://media.example/img/", Artifacts: []linkedingo.VectorArtifact{
		{FileIdentifyingURLPathSegment: "small", Width: 100, Height: 50}, {FileIdentifyingURLPathSegment: "large", Width: 800, Height: 400},
	}}})
	video := with("v", linkedingo.RenderContent{Video: &linkedingo.VideoPlayMetadata{ProgressiveStreams: []linkedingo.ProgressiveDownloadMetadata{
		{BitRate: 100, MediaType: "video/mp4", StreamingLocations: []linkedingo.StreamingLocation{{URL: "https://media.example/low"}}},
		{BitRate: 900, MediaType: "video/mp4", Size: 4096, Width: 640, Height: 360, StreamingLocations: []linkedingo.StreamingLocation{{URL: "https://media.example/high"}}},
	}}})
	voice := with("a", linkedingo.RenderContent{Audio: &linkedingo.AudioMetadata{URL: "https://media.example/voice"}})
	pdf := with("f", linkedingo.RenderContent{File: &linkedingo.FileAttachment{Name: "cv.pdf", MediaType: "application/pdf", ByteSize: 2048, URL: "https://media.example/cv"}})
	pictureFile := with("i", linkedingo.RenderContent{File: &linkedingo.FileAttachment{Name: "map.png", MediaType: "image/png", URL: "https://media.example/map"}})
	gif := with("g", linkedingo.RenderContent{ExternalMedia: &linkedingo.ExternalMedia{Media: linkedingo.ExternalProxyImage{URL: "https://gif.example/x", OriginalWidth: 200, OriginalHeight: 100}}})
	both := with("b", linkedingo.RenderContent{File: &linkedingo.FileAttachment{Name: "a.pdf", MediaType: "application/pdf", URL: "https://media.example/a"}},
		linkedingo.RenderContent{File: &linkedingo.FileAttachment{Name: "b.pdf"}})
	captioned := with("c", linkedingo.RenderContent{VectorImage: &linkedingo.VectorImage{RootURL: "https://media.example/c"}})
	captioned.Body = linkedingo.AttributedText{Text: "look at this"}

	a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(conversation(group, "G", 1000, 0, true, me, dana, eli),
		photo, video, pdf, pictureFile, gif, both, captioned)}, nil)
	a.event(ctx, c, liveMessage(voice, 1000))

	for mid, want := range map[string]struct {
		kind       domain.MediaType
		body, url  string
		w, h, size int
	}{
		"p": {domain.MediaImage, "photo", "https://media.example/img/large", 800, 400, 0},
		"v": {domain.MediaVideo, "video", "https://media.example/high", 640, 360, 4096},
		"a": {domain.MediaAudio, domain.VoiceMessage, "https://media.example/voice", 0, 0, 0},
		"f": {domain.MediaFile, "cv.pdf", "https://media.example/cv", 0, 0, 2048},
		"i": {domain.MediaImage, "map.png", "https://media.example/map", 0, 0, 0},
		"g": {domain.MediaImage, "GIF", "https://gif.example/x", 200, 100, 0},
		"b": {domain.MediaFile, "[file: b.pdf]", "https://media.example/a", 0, 0, 0},
		"c": {domain.MediaImage, "look at this", "https://media.example/c", 0, 0, 0},
	} {
		got, found, err := cache.MessageByID(ctx, room, msgID(group, mid))
		if err != nil || !found || got.Media == nil {
			t.Errorf("%s: cached %+v (%v, %v), want an attachment", mid, got, found, err)
			continue
		}
		if got.Media.Type != want.kind || got.Body != want.body || got.Media.Width != want.w || got.Media.Height != want.h || got.Media.Size != want.size {
			t.Errorf("%s: %q %+v, want %+v", mid, got.Body, got.Media, want)
		}
		_, raw, ok, err := cache.MediaSource(ctx, room, msgID(group, mid))
		var source mediaSource
		if err != nil || !ok || json.Unmarshal([]byte(raw), &source) != nil || source.URL != want.url {
			t.Errorf("%s: source %q (%v, %v), want %s", mid, raw, ok, err, want.url)
		}
	}
	if len(c.inbox.sources) != 0 {
		t.Errorf("sources left after caching: %v", c.inbox.sources)
	}
}

// Loading and sending need the account connected; loading needs a kept source, and an
// empty or unreadable file is not sent.
func TestAttachmentsNeedLinkedIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, _, _ := cachedAdapter(t)
	room := roomID(me, group)
	a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(conversation(group, "G", 1000, 0, true, me, dana, eli),
		message(group, "m1", dana, "hi", 1000))}, nil)
	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LoadImage(ctx, room, msgID(group, "m1")); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("loading while not connected = %v", err)
	}
	if err := a.SendFile(ctx, room, file, filepath.Base(file), ""); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("sending while not connected = %v", err)
	}
	c.client = &linkedingo.Client{} // connected, as far as the checks go: they come first
	if _, err := a.LoadImage(ctx, room, msgID(group, "m1")); err == nil || !strings.Contains(err.Error(), "no attachment") {
		t.Errorf("loading a message with no attachment = %v", err)
	}
	empty := filepath.Join(t.TempDir(), "nothing.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.SendFile(ctx, room, empty, filepath.Base(empty), ""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("sending an empty file = %v", err)
	}
	if err := a.SendFile(ctx, room, filepath.Join(t.TempDir(), "gone.png"), "gone.png", ""); err == nil {
		t.Error("sending a file that is not there was taken")
	}
}

// A file goes to LinkedIn as its web client sends it: a video as a video with a stream,
// audio as a voice message, anything else, a photo too, as a named file.
func TestFilesAreSentAsTheirKind(t *testing.T) {
	t.Parallel()
	urn := linkedingo.NewURN("urn:li:digitalmediaAsset:1")
	for name, want := range map[string]struct {
		upload linkedingo.MediaUploadType
		check  func(linkedingo.SendRenderContent) bool
	}{
		"photo.JPG": {linkedingo.MediaUploadTypePhotoAttachment, func(r linkedingo.SendRenderContent) bool {
			return r.File != nil && r.File.Name == "photo.JPG" && r.File.MediaType == "image/jpeg" && r.File.ByteSize == 7
		}},
		"clip.mp4": {linkedingo.MediaUploadTypeVideoAttachment, func(r linkedingo.SendRenderContent) bool {
			return r.Video != nil && r.Video.Media == urn && len(r.Video.ProgressiveStreams) == 1 &&
				r.Video.ProgressiveStreams[0].MediaType == "video/mp4" && r.Video.ProgressiveStreams[0].Size == 7 &&
				strings.HasPrefix(r.Video.ProgressiveStreams[0].StreamingLocations[0].URL, "blob:https://www.linkedin.com/")
		}},
		"note.m4a": {linkedingo.MediaUploadTypeVoiceMessage, func(r linkedingo.SendRenderContent) bool {
			return r.Audio != nil && r.Audio.AssetURN == urn && r.Audio.ByteSize == 7
		}},
		"logo.svg": {linkedingo.MediaUploadTypeFileAttachment, func(r linkedingo.SendRenderContent) bool { return r.File != nil }},
		"cv.pdf":   {linkedingo.MediaUploadTypeFileAttachment, func(r linkedingo.SendRenderContent) bool { return r.File != nil && r.File.Name == "cv.pdf" }},
		"noext": {linkedingo.MediaUploadTypeFileAttachment, func(r linkedingo.SendRenderContent) bool {
			return r.File != nil && r.File.MediaType == "text/plain"
		}},
	} {
		mimeType := mimeOf(name, []byte("bytes 7"))
		kind, render := uploadKind(mimeType)
		if kind != want.upload || !want.check(render(urn, name, mimeType, 7)) {
			t.Errorf("%s (%s): uploaded as %s, sent as %+v", name, mimeType, kind, render(urn, name, mimeType, 7))
		}
	}
}
