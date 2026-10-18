package instagram

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/useragent"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message shows its first attachment (a photo, video, voice message, sticker or
// GIF); any further one is counted in its text. Its bytes are fetched when asked for,
// so media.file_json keeps where from: an address on Instagram's servers. Those
// expire; one Instagram refuses is looked up again in the thread's newest page.

// mediaMost bounds one attachment's download (and a file sent), and mediaSpan the
// time a download takes.
const (
	mediaMost = 100 << 20
	mediaSpan = 5 * time.Minute
)

// mediaClient fetches attachments from Instagram's servers.
var mediaClient = &http.Client{Timeout: mediaSpan}

// mediaSource is where an attachment's bytes are, and what they are.
type mediaSource struct {
	URL  string `json:"url"`
	Mime string `json:"mime,omitempty"`
}

var (
	errNoMedia       = errors.New("instagram: that message has no attachment kith can load")
	errMediaRefused  = errors.New("instagram: Instagram's servers did not hand the attachment over")
	errMediaTooLarge = errors.New("instagram: the attachment is too large to load")
)

// The formats Instagram's web app gets an attachment's kinds in.
const (
	photoMime   = "image/jpeg"
	videoMime   = "video/mp4"
	voiceMime   = "audio/mp4"
	stickerMime = "image/webp"
)

// attached is a photo's or video's first file as kith's media and where it is, the
// rest counted in body.
func attached(kind domain.MediaType, files []*slidetypes.Attachment, body string) (*domain.Media, mediaSource, string) {
	if len(files) == 0 || files[0] == nil {
		return nil, mediaSource{}, cmp.Or(body, "[an attachment kith cannot show]")
	}
	f := files[0]
	m := &domain.Media{Type: kind, Name: "photo", Mime: photoMime, Width: f.PreviewWidth, Height: f.PreviewHeight}
	if kind == domain.MediaVideo {
		m.Name, m.Mime = "video", videoMime
	}
	if more := len(files) - 1; more > 0 {
		body = strings.TrimSpace(fmt.Sprintf("%s\n(+%d more)", body, more))
	}
	return m, mediaSource{URL: cmp.Or(f.AttachmentCDNURL, f.PreviewCDNURL), Mime: m.Mime}, body
}

// voice is a voice message's media and where it is.
func voice(c *slidetypes.MessageContentAudio) (*domain.Media, mediaSource) {
	m := &domain.Media{Type: domain.MediaAudio, Name: domain.VoiceMessage, Mime: voiceMime}
	if len(c.AudioAttachments) == 0 || c.AudioAttachments[0] == nil {
		return m, mediaSource{}
	}
	return m, mediaSource{URL: c.AudioAttachments[0].AttachmentCDNURL, Mime: voiceMime}
}

// animated is a sticker or GIF: a sticker (or a GIF with no video) as its picture, a
// GIF as its video; its alt text is the body.
func animated(c *slidetypes.MessageContentAnimatedMedia) (*domain.Media, mediaSource, string) {
	if len(c.AnimatedMedia) == 0 || c.AnimatedMedia[0] == nil {
		return nil, mediaSource{}, "[GIF]"
	}
	g := c.AnimatedMedia[0]
	m := &domain.Media{Type: domain.MediaImage, Name: "GIF", Mime: stickerMime, Width: g.PreviewWidth, Height: g.PreviewHeight}
	if g.IsSticker {
		m.Name = "sticker"
	}
	switch {
	case g.AttachmentWebpURL != "" && (g.IsSticker || g.AttachmentMP4URL == ""):
		return m, mediaSource{URL: g.AttachmentWebpURL, Mime: stickerMime}, g.AltText
	case g.AttachmentMP4URL != "":
		m.Type, m.Mime = domain.MediaVideo, videoMime
		return m, mediaSource{URL: g.AttachmentMP4URL, Mime: videoMime}, g.AltText
	}
	m.Mime = ""
	return m, mediaSource{URL: g.PreviewCDNURL}, g.AltText
}

// keepSource records where msg's attachment is, once msg is cached.
func (a *Adapter) keepSource(ctx context.Context, msg domain.Message, source mediaSource) {
	if a.cache == nil || msg.Media == nil || source.URL == "" {
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

// source is where a cached message's attachment is.
func (a *Adapter) source(ctx context.Context, room domain.RoomID, event domain.EventID) (mediaSource, error) {
	_, raw, ok, err := a.cache.MediaSource(ctx, room, event)
	if err != nil {
		return mediaSource{}, fmt.Errorf("instagram: read the attachment's source: %w", err)
	}
	var s mediaSource
	if !ok || json.Unmarshal([]byte(raw), &s) != nil || s.URL == "" {
		return mediaSource{}, fmt.Errorf("%w: %s", errNoMedia, event)
	}
	return s, nil
}

// LoadImage fetches a message's attachment from Instagram's servers. One whose
// address expired is looked up again in the thread's newest page (as the account
// connected); an older one's stays expired. It caches nothing; the client's media
// cache does.
func (a *Adapter) LoadImage(ctx context.Context, room domain.RoomID, event domain.EventID) ([]byte, error) {
	if a.cache == nil {
		return nil, errNoMedia
	}
	s, err := a.source(ctx, room, event)
	if err != nil {
		return nil, err
	}
	data, err := download(ctx, s)
	if !errors.Is(err, errMediaRefused) {
		if err != nil {
			return nil, fmt.Errorf("instagram: download the attachment of %s: %w", event, err)
		}
		return data, nil
	}
	self, chat, _ := threadOf(room)
	c, client := a.connected(self)
	if client == nil {
		return nil, fmt.Errorf("instagram: the attachment of %s expired; it can be fetched again once the account connects: %w", event, err)
	}
	a.fetchRecent(ctx, c, client, chat)
	fresh, ferr := a.source(ctx, room, event)
	if ferr != nil || fresh.URL == s.URL {
		return nil, fmt.Errorf("instagram: the attachment of %s expired, and is too old to be fetched again: %w", event, err)
	}
	if data, err = download(ctx, fresh); err != nil {
		return nil, fmt.Errorf("instagram: download the attachment of %s: %w", event, err)
	}
	return data, nil
}

// download fetches an attachment as Instagram's web app does.
func download(ctx context.Context, s mediaSource) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare: %w", err)
	}
	dest := "empty"
	if kind, _, _ := strings.Cut(s.Mime, "/"); kind == "image" || kind == "video" || kind == "audio" {
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
