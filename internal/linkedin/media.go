package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message shows its first attachment (a photo, video, voice message, file or GIF);
// any further one is named in its text. The attachment is fetched from LinkedIn with
// the account's session when asked for, so media.file_json keeps {"linkedin": url}.
// LinkedIn's addresses expire; one that has stops loading.

type mediaSource struct {
	URL string `json:"linkedin"`
}

// attachment is a render content as kith's media and the address to load it from;
// nil for one that is not an attachment (a reply, a forward, a shared post).
func attachment(rc *linkedingo.RenderContent) (*domain.Media, string) {
	switch {
	case rc.VectorImage != nil:
		media := &domain.Media{Type: domain.MediaImage, Name: "photo"}
		for _, a := range rc.VectorImage.Artifacts {
			if a.Height > media.Height {
				media.Width, media.Height = a.Width, a.Height
			}
		}
		return media, rc.VectorImage.GetLargestArtifactURL()
	case rc.Video != nil:
		media := &domain.Media{Type: domain.MediaVideo, Name: "video"}
		url := ""
		best := -1
		for _, s := range rc.Video.ProgressiveStreams {
			if len(s.StreamingLocations) > 0 && s.BitRate > best {
				best, url = s.BitRate, s.StreamingLocations[0].URL
				media.Mime, media.Size, media.Width, media.Height = s.MediaType, s.Size, s.Width, s.Height
			}
		}
		return media, url
	case rc.Audio != nil:
		return &domain.Media{Type: domain.MediaAudio, Name: domain.VoiceMessage}, rc.Audio.URL
	case rc.File != nil:
		return fileMedia(rc.File.Name, rc.File.MediaType, rc.File.ByteSize), rc.File.URL
	case rc.ExternalMedia != nil:
		m := rc.ExternalMedia.Media
		return &domain.Media{Type: domain.MediaImage, Name: "GIF", Mime: "image/gif", Width: m.OriginalWidth, Height: m.OriginalHeight}, m.URL
	}
	return nil, ""
}

// fileMedia is a file as kith shows it, its kind from its type.
func fileMedia(name, mimeType string, size int) *domain.Media {
	media := &domain.Media{Type: domain.MediaFile, Name: name, Mime: mimeType, Size: size}
	if media.Name == "" {
		media.Name = "file"
	}
	switch kind, _, _ := strings.Cut(mimeType, "/"); kind {
	case "image":
		media.Type = domain.MediaImage
	case "video":
		media.Type = domain.MediaVideo
	case "audio":
		media.Type = domain.MediaAudio
	}
	return media
}

// keepSources records how to load the attachments of msgs that were cached; a
// message with none keeps nothing.
func (a *Adapter) keepSources(ctx context.Context, c *conn, msgs []domain.Message) {
	for i := range msgs {
		url, ok := c.inbox.sources[msgs[i].ID]
		if !ok {
			continue
		}
		delete(c.inbox.sources, msgs[i].ID)
		raw, err := json.Marshal(mediaSource{URL: url})
		if err != nil {
			continue
		}
		if err := a.cache.SaveMediaSource(ctx, msgs[i].ID, msgs[i].RoomID, "", string(raw)); err != nil {
			a.log.Warn("keep an attachment's source failed", "room", msgs[i].RoomID, "err", err)
		}
	}
}

// LoadImage downloads a message's attachment with its account's session. It caches
// nothing; the client's media cache does.
func (a *Adapter) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	_, client, _, err := a.client(roomID)
	if err != nil {
		return nil, err
	}
	if a.cache == nil {
		return nil, fmt.Errorf("linkedin: no cache to find the attachment of %s in", eventID)
	}
	_, raw, ok, err := a.cache.MediaSource(ctx, roomID, eventID)
	if err != nil {
		return nil, fmt.Errorf("linkedin: read the attachment's source: %w", err)
	}
	var source mediaSource
	if !ok || json.Unmarshal([]byte(raw), &source) != nil || source.URL == "" {
		return nil, fmt.Errorf("linkedin: %s has no attachment kith can load", eventID)
	}
	var data []byte
	err = a.ask(ctx, "downloading an attachment", func(ctx context.Context) (err error) {
		data, err = client.DownloadBytes(ctx, source.URL)
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return nil, fmt.Errorf("linkedin: download the attachment of %s (LinkedIn's links expire): %w", eventID, err)
	}
	return data, nil
}

// SendFile uploads a file and sends it with caption as its text: a video as a video,
// audio as a voice message, anything else (a photo too, as LinkedIn's web client and
// the bridge send them) as a file. The answer is the whole message, kept at once.
func (a *Adapter) SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error {
	c, client, thread, err := a.client(roomID)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own file, picked by them to send
	if err != nil {
		return fmt.Errorf("linkedin: read %s: %w", path, err)
	}
	name := filepath.Base(path)
	if len(data) == 0 {
		return fmt.Errorf("linkedin: %s is empty, and LinkedIn takes no empty files", name)
	}
	mimeType := mimeOf(name, data)
	kind, render := uploadKind(mimeType)
	var urn linkedingo.URN
	err = a.ask(ctx, "uploading a file", func(ctx context.Context) (err error) {
		urn, err = client.UploadMedia(ctx, kind, name, mimeType, len(data), bytes.NewReader(data))
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("linkedin: upload %s: %w", name, err)
	}
	var sent *linkedingo.MessageSentResponse
	err = a.ask(ctx, "sending a file", func(ctx context.Context) (err error) {
		sent, err = client.SendMessage(ctx, conversationURN(c.profile, thread),
			linkedingo.SendMessageBody{Text: caption}, []linkedingo.SendRenderContent{render(urn, name, mimeType, len(data))}, newToken())
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("linkedin: send %s to %s: %w", name, roomID, err)
	}
	c.applying.Lock()
	defer c.applying.Unlock()
	if msg, ok := c.inbox.message(thread, &sent.Data); ok {
		a.store(ctx, c, change{live: []domain.Message{msg}})
	}
	return nil
}

// mimeOf is a file's type by its name, else by its first bytes.
func mimeOf(name string, data []byte) string {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	mimeType, _, _ = strings.Cut(mimeType, ";")
	return mimeType
}

// uploadKind is how LinkedIn takes a file of mimeType: the upload's type, and the
// render content that sends it (as the bridge builds them).
func uploadKind(mimeType string) (linkedingo.MediaUploadType, func(urn linkedingo.URN, name, mimeType string, size int) linkedingo.SendRenderContent) {
	asFile := func(urn linkedingo.URN, name, mimeType string, size int) linkedingo.SendRenderContent {
		return linkedingo.SendRenderContent{File: &linkedingo.SendFile{AssetURN: urn, ByteSize: size, MediaType: mimeType, Name: name}}
	}
	switch {
	case strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml":
		return linkedingo.MediaUploadTypePhotoAttachment, asFile
	case strings.HasPrefix(mimeType, "video/"):
		return linkedingo.MediaUploadTypeVideoAttachment, func(urn linkedingo.URN, _, mimeType string, size int) linkedingo.SendRenderContent {
			// The web client names the stream by a blob address it made up; the bridge
			// does the same.
			return linkedingo.SendRenderContent{Video: &linkedingo.SendVideo{
				Media: urn, TrackingID: urn,
				Thumbnail: linkedingo.SendThumbnail{Artifacts: []linkedingo.SendArtifacts{{}}},
				ProgressiveStreams: []linkedingo.SendProgressiveStreams{{
					MediaType: mimeType, Size: size,
					StreamingLocations: []linkedingo.SendURL{{URL: "blob:https://www.linkedin.com/" + newToken()}},
				}},
			}}
		}
	case strings.HasPrefix(mimeType, "audio/"):
		return linkedingo.MediaUploadTypeVoiceMessage, func(urn linkedingo.URN, _, _ string, size int) linkedingo.SendRenderContent {
			return linkedingo.SendRenderContent{Audio: &linkedingo.SendAudio{AssetURN: urn, ByteSize: size}}
		}
	}
	return linkedingo.MediaUploadTypeFileAttachment, asFile
}
