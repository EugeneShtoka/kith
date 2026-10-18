package instagram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // a GIF's size
	_ "image/jpeg" // a photo's size
	_ "image/png"  // a picture's size
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A file is uploaded to Instagram, then sent as a message naming the upload. An
// attachment carries no text: what you typed goes after it, as its own message. The
// sent file is cached at once; its echo brings where to load it from.

var (
	errFileTooLarge = fmt.Errorf("instagram: a file is at most %d MiB", mediaMost>>20)
	errUploadNoID   = errors.New("instagram: Instagram took the upload but named no file")
)

// SendFile sends the file at path under name, then caption.
func (a *Adapter) SendFile(ctx context.Context, room domain.RoomID, path, name, caption string) error {
	c, client, chat, err := a.sender(room)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) //nolint:gosec // the file the person chose to send
	if err != nil {
		return fmt.Errorf("instagram: read %s: %w", path, err)
	}
	if len(data) > mediaMost {
		return errFileTooLarge
	}
	media := fileMediaOf(name, data)
	thread, _ := strconv.ParseInt(chat, 10, 64) // the upload names its thread as its referrer
	up, err := client.GetHTTP().SendMercuryUploadRequest(ctx, thread, &httpclient.MercuryUploadMedia{
		Filename: media.Name, MimeType: media.Mime, MediaData: data, IsVoiceClip: media.Type == domain.MediaAudio,
	})
	if err != nil {
		return fmt.Errorf("instagram: upload %s: %w", media.Name, err)
	}
	id := up.Payload.RealMetadata.GetFbId()
	if id == 0 {
		return errUploadNoID
	}
	resp, err := client.SendMedia(ctx, &slidetypes.SendMediaRequest{
		AttachmentFBID: strconv.FormatInt(id, 10), ThreadID: chat, OfflineThreadingID: a.otid(""),
	})
	if err != nil {
		return fmt.Errorf("instagram: send %s: %w", media.Name, err)
	}
	if sent := resp.GetMessage(); sent.ID != "" {
		if err := a.keepSent(ctx, c, chat, sent, domain.Message{Media: media}); err != nil {
			return err
		}
	}
	if caption = strings.TrimSpace(caption); caption != "" {
		return a.Send(ctx, room, domain.Draft{Body: caption})
	}
	return nil
}

// fileMediaOf is a file sent as name: its kind and type from its contents (then its
// name), a picture's size read from it.
func fileMediaOf(name string, data []byte) *domain.Media {
	mimeType := http.DetectContentType(data)
	if mimeType == "application/octet-stream" || strings.HasPrefix(mimeType, "text/plain") {
		if byName := mime.TypeByExtension(filepath.Ext(name)); byName != "" {
			mimeType = byName
		}
	}
	media := &domain.Media{Type: domain.MediaFile, Name: name, Mime: mimeType, Size: len(data)}
	switch kind, _, _ := strings.Cut(mimeType, "/"); kind {
	case "image":
		media.Type = domain.MediaImage
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			media.Width, media.Height = cfg.Width, cfg.Height
		}
	case "video":
		media.Type = domain.MediaVideo
	case "audio":
		media.Type = domain.MediaAudio
	}
	return media
}
