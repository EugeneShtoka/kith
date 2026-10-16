package meta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/useragent"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message shows its first attachment (a photo, video, voice message, file or
// sticker); any further one is named in its text. Its bytes are fetched when asked
// for, so media.file_json keeps where from: a plain thread's attachment is an address
// on Meta's servers, which expires; an encrypted chat's is the encrypted file's path
// and the keys to open it.

// mediaMost bounds one attachment's download, and mediaSpan the time it takes.
const (
	mediaMost = 100 << 20
	mediaSpan = 5 * time.Minute
)

// mediaClient fetches plain threads' attachments from Meta's servers.
var mediaClient = &http.Client{Timeout: mediaSpan}

// mediaSource is where an attachment's bytes are: URL for a plain one, Encrypted for
// an encrypted chat's.
type mediaSource struct {
	URL       string           `json:"meta,omitempty"`
	Mime      string           `json:"mime,omitempty"`
	Encrypted *encryptedSource `json:"e2ee,omitempty"`
}

// encryptedSource is an encrypted attachment's path and keys, as whatsmeow downloads it.
type encryptedSource struct {
	Kind      whatsmeow.MediaType `json:"kind"`
	Path      string              `json:"path"`
	Key       []byte              `json:"key"`
	SHA256    []byte              `json:"sha256"`
	EncSHA256 []byte              `json:"enc_sha256"`
}

var (
	errNoMedia       = errors.New("meta: that message has no attachment kith can load")
	errMediaRefused  = errors.New("meta: Meta's servers did not hand the attachment over")
	errMediaTooLarge = errors.New("meta: the attachment is too large to load")
)

// blobMedia is a plain thread's attachment as kith's media and where to load it from.
func blobMedia(b *table.LSInsertBlobAttachment) (*domain.Media, mediaSource) {
	url, mime := b.PlayableUrl, b.PlayableUrlMimeType
	width, height := 0, 0
	if url == "" {
		url, mime = b.PreviewUrl, b.PreviewUrlMimeType
		width, height = int(b.PreviewWidth), int(b.PreviewHeight)
	}
	if mime == "" {
		mime = b.AttachmentMimeType
	}
	media := &domain.Media{Name: b.Filename, Mime: mime, Size: int(b.Filesize), Width: width, Height: height}
	switch b.AttachmentType {
	case table.AttachmentTypeImage, table.AttachmentTypeAnimatedImage, table.AttachmentTypeEphemeralImage:
		media.Type = domain.MediaImage
	case table.AttachmentTypeVideo, table.AttachmentTypeEphemeralVideo:
		media.Type = domain.MediaVideo
	case table.AttachmentTypeAudio:
		media.Type, media.Name = domain.MediaAudio, domain.VoiceMessage
	default:
		media.Type = domain.MediaFile
	}
	if media.Name == "" {
		media.Name = mediaName(media.Type)
	}
	return media, mediaSource{URL: url, Mime: mime}
}

// stickerMedia is a plain thread's sticker, shown as its picture.
func stickerMedia(s *table.LSInsertStickerAttachment) (*domain.Media, mediaSource) {
	url, mime := s.PreviewUrl, s.PreviewUrlMimeType
	if url == "" {
		url, mime = s.PlayableUrl, s.PlayableUrlMimeType
	}
	return &domain.Media{
		Type: domain.MediaImage, Name: "sticker", Mime: mime, Width: int(s.PreviewWidth), Height: int(s.PreviewHeight),
	}, mediaSource{URL: url, Mime: mime}
}

// mediaName names an attachment that came without a file name.
func mediaName(kind domain.MediaType) string {
	switch kind {
	case domain.MediaImage:
		return "photo"
	case domain.MediaVideo:
		return "video"
	case domain.MediaAudio:
		return domain.VoiceMessage
	case domain.MediaFile:
	}
	return "file"
}

// encryptedMedia is an encrypted chat's attachment as kith's media, where to load it
// from, and its caption; nil for content that is no attachment, or one that cannot be
// read (its transport does not decode, or names no file).
func encryptedMedia(c *waConsumerApplication.ConsumerApplication_Content) (*domain.Media, mediaSource, string) {
	media, transport, kind, caption := encryptedKind(c)
	integral := transport.GetIntegral()
	if media == nil || integral.GetDirectPath() == "" || len(integral.GetMediaKey()) == 0 {
		return nil, mediaSource{}, ""
	}
	media.Mime = transport.GetAncillary().GetMimetype()
	media.Size = int(transport.GetAncillary().GetFileLength()) // #nosec G115 -- a file's length
	return media, mediaSource{Mime: media.Mime, Encrypted: &encryptedSource{
		Kind: kind, Path: integral.GetDirectPath(), Key: integral.GetMediaKey(),
		SHA256: integral.GetFileSHA256(), EncSHA256: integral.GetFileEncSHA256(),
	}}, strings.TrimSpace(caption)
}

// encryptedKind is an encrypted attachment's media, transport, kind and caption, by
// what it is; nil media for content that is none, or does not decode.
func encryptedKind(c *waConsumerApplication.ConsumerApplication_Content) (*domain.Media, *waMediaTransport.WAMediaTransport, whatsmeow.MediaType, string) {
	switch {
	case c.GetImageMessage() != nil:
		if tr, err := c.GetImageMessage().Decode(); err == nil {
			an := tr.GetAncillary()
			return &domain.Media{Type: domain.MediaImage, Name: "photo", Width: int(an.GetWidth()), Height: int(an.GetHeight())},
				tr.GetIntegral().GetTransport(), whatsmeow.MediaImage, c.GetImageMessage().GetCaption().GetText()
		}
	case c.GetVideoMessage() != nil:
		if tr, err := c.GetVideoMessage().Decode(); err == nil {
			an := tr.GetAncillary()
			return &domain.Media{Type: domain.MediaVideo, Name: "video", Width: int(an.GetWidth()), Height: int(an.GetHeight())},
				tr.GetIntegral().GetTransport(), whatsmeow.MediaVideo, c.GetVideoMessage().GetCaption().GetText()
		}
	case c.GetAudioMessage() != nil:
		if tr, err := c.GetAudioMessage().Decode(); err == nil {
			return &domain.Media{Type: domain.MediaAudio, Name: domain.VoiceMessage}, tr.GetIntegral().GetTransport(), whatsmeow.MediaAudio, ""
		}
	case c.GetDocumentMessage() != nil:
		if tr, err := c.GetDocumentMessage().Decode(); err == nil {
			name := c.GetDocumentMessage().GetFileName()
			if name == "" {
				name = "file"
			}
			return &domain.Media{Type: domain.MediaFile, Name: name}, tr.GetIntegral().GetTransport(), whatsmeow.MediaDocument, ""
		}
	case c.GetStickerMessage() != nil:
		// One only Meta's servers can hand over (a receiver-fetch sticker) stays a label.
		if tr, err := c.GetStickerMessage().Decode(); err == nil && tr.GetAncillary().GetReceiverFetchID() == "" {
			an := tr.GetAncillary()
			return &domain.Media{Type: domain.MediaImage, Name: "sticker", Width: int(an.GetWidth()), Height: int(an.GetHeight())},
				tr.GetIntegral().GetTransport(), whatsmeow.MediaImage, ""
		}
	}
	return nil, nil, "", ""
}

// keepSource records how to load msg's attachment, once msg is cached.
func (a *Adapter) keepSource(ctx context.Context, msg domain.Message, source mediaSource) {
	if a.cache == nil || msg.Media == nil || source.URL == "" && source.Encrypted == nil {
		return
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return
	}
	if err := a.cache.SaveMediaSource(ctx, msg.ID, msg.RoomID, "", string(raw)); err != nil {
		a.log.Warn("keep an attachment's source failed", "room", msg.RoomID, "err", err)
	}
}

// LoadImage fetches a message's attachment: a plain one from Meta's servers, an
// encrypted one over its account's encrypted connection. It caches nothing; the
// client's media cache does.
func (a *Adapter) LoadImage(ctx context.Context, room domain.RoomID, event domain.EventID) ([]byte, error) {
	if a.cache == nil {
		return nil, errNoMedia
	}
	_, raw, ok, err := a.cache.MediaSource(ctx, room, event)
	if err != nil {
		return nil, fmt.Errorf("meta: read the attachment's source: %w", err)
	}
	var source mediaSource
	if !ok || json.Unmarshal([]byte(raw), &source) != nil {
		return nil, fmt.Errorf("%w: %s", errNoMedia, event)
	}
	if source.Encrypted != nil {
		return a.loadEncrypted(ctx, room, event, source.Encrypted)
	}
	if source.URL == "" {
		return nil, fmt.Errorf("%w: %s", errNoMedia, event)
	}
	data, err := download(ctx, source.URL, source.Mime)
	if err != nil {
		return nil, fmt.Errorf("meta: download the attachment of %s (Meta's links expire): %w", event, err)
	}
	return data, nil
}

// download fetches a plain attachment as Messenger's web client does.
func download(ctx context.Context, url, mime string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare: %w", err)
	}
	dest := "empty"
	if kind, _, _ := strings.Cut(mime, "/"); kind == "image" || kind == "video" || kind == "audio" {
		dest = kind
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Dest", dest)
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("User-Agent", useragent.UserAgent)
	req.Header.Set("sec-ch-ua", useragent.SecCHUserAgent)
	req.Header.Set("sec-ch-ua-platform", useragent.SecCHPlatform)
	resp, err := mediaClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %s", errMediaRefused, resp.Status)
	}
	if resp.ContentLength > mediaMost {
		return nil, fmt.Errorf("%w: %d bytes", errMediaTooLarge, resp.ContentLength)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mediaMost+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if len(data) > mediaMost {
		return nil, fmt.Errorf("%w: over %d bytes", errMediaTooLarge, mediaMost)
	}
	return data, nil
}

// loadEncrypted downloads and opens an encrypted chat's attachment.
func (a *Adapter) loadEncrypted(ctx context.Context, room domain.RoomID, event domain.EventID, s *encryptedSource) ([]byte, error) {
	self, _, ok := threadOf(room)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNotAThread, room)
	}
	c := a.connOf(self)
	if c == nil {
		return nil, errNotConnected
	}
	c.mu.Lock()
	wa := c.e2ee
	c.mu.Unlock()
	if wa == nil {
		return nil, errNoEncrypted
	}
	path := s.Path
	integral := &waMediaTransport.WAMediaTransport_Integral{
		FileSHA256: s.SHA256, MediaKey: s.Key, FileEncSHA256: s.EncSHA256, DirectPath: &path,
	}
	var data []byte
	err := a.guard("downloading an encrypted attachment", func() error {
		var err error
		data, err = wa.DownloadFB(ctx, integral, s.Kind)
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return nil, fmt.Errorf("meta: download the attachment of %s: %w", event, err)
	}
	return data, nil
}
