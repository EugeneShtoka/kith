package gmessages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message shows its first attachment; any further one is named in its text. The phone
// puts attachments on Google's servers encrypted, each with its own key, so
// media.file_json keeps the attachment's ID and key, and its bytes are fetched when
// asked for. Sending a file uploads it the same way, then sends a message carrying it.

// mediaMost bounds one attachment, either way.
const mediaMost = 100 << 20

var (
	errNoMedia      = errors.New("gmessages: that message has no attachment kith can load")
	errFileTooLarge = fmt.Errorf("gmessages: the file is larger than %d MB", mediaMost>>20)
)

// mediaSource is where an attachment is on Google's servers, and its key.
type mediaSource struct {
	ID  string `json:"id"`
	Key []byte `json:"key"`
}

// loadable reports whether an attachment can be fetched: it is on Google's servers.
func loadable(media *gmproto.MediaContent) bool {
	return media.GetMediaID() != "" && len(media.GetDecryptionKey()) > 0
}

// mediaOf is an attachment as kith describes it; one with no name is named by its kind.
func mediaOf(media *gmproto.MediaContent) *domain.Media {
	m := &domain.Media{
		Type: mediaType(media.GetMimeType()), Name: media.GetMediaName(), Mime: media.GetMimeType(),
		Width: int(media.GetDimensions().GetWidth()), Height: int(media.GetDimensions().GetHeight()),
		Size: int(media.GetSize()),
	}
	if m.Name == "" {
		m.Name = mediaLabel(media)
	}
	return m
}

// mediaType is the kind of attachment a content type is.
func mediaType(mimeType string) domain.MediaType {
	switch kind, _, _ := strings.Cut(mimeType, "/"); kind {
	case "image":
		return domain.MediaImage
	case "video":
		return domain.MediaVideo
	case "audio":
		return domain.MediaAudio
	default:
		return domain.MediaFile
	}
}

// source is where m's shown attachment is, if it has one.
func source(m *gmproto.Message) (mediaSource, bool) {
	for _, info := range m.GetMessageInfo() {
		if media := info.GetMediaContent(); media != nil && loadable(media) {
			return mediaSource{ID: media.GetMediaID(), Key: media.GetDecryptionKey()}, true
		}
	}
	return mediaSource{}, false
}

// keepSources records where the attachments of msgs are, once they are cached.
func (a *Adapter) keepSources(ctx context.Context, c *conn, msgs []*gmproto.Message) {
	if a.cache == nil {
		return
	}
	for _, m := range msgs {
		src, ok := source(m)
		if !ok {
			continue
		}
		raw, err := json.Marshal(src)
		if err != nil {
			continue
		}
		room, event := roomID(c.self, m.GetConversationID()), eventID(c.self, m.GetConversationID(), m.GetMessageID())
		if err := a.cache.SaveMediaSource(ctx, event, room, "", string(raw)); err != nil {
			a.log.Warn("keep an attachment's source failed", "account", c.account.Name, "err", err)
		}
	}
}

// LoadImage fetches a message's attachment from Google's servers and decrypts it. It
// caches nothing; the client's media cache does.
func (a *Adapter) LoadImage(ctx context.Context, room domain.RoomID, event domain.EventID) ([]byte, error) {
	self, _, ok := conversationOf(room)
	if !ok || a.cache == nil {
		return nil, fmt.Errorf("%w: %s", errNoMedia, event)
	}
	_, raw, ok, err := a.cache.MediaSource(ctx, room, event)
	if err != nil {
		return nil, fmt.Errorf("gmessages: read the attachment's source: %w", err)
	}
	var src mediaSource
	if !ok || json.Unmarshal([]byte(raw), &src) != nil || src.ID == "" {
		return nil, fmt.Errorf("%w: %s", errNoMedia, event)
	}
	_, client, err := a.connected(self)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = a.guard("downloading an attachment", func() error {
		body, opened := client.DownloadMedia(src.ID, src.Key)
		if opened != nil {
			return opened //nolint:wrapcheck // wrapped below
		}
		defer func() { _ = body.Close() }()
		var read error
		data, read = io.ReadAll(io.LimitReader(body, mediaMost+1))
		return read //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return nil, fmt.Errorf("gmessages: download the attachment of %s: %w", event, err)
	}
	if len(data) > mediaMost {
		return nil, errFileTooLarge
	}
	return data, nil
}

// fileMedia is a file sent as name: its content type from its contents, then its name,
// with no parameters (Google types an upload by the bare type).
func fileMedia(name string, data []byte) string {
	mimeType := http.DetectContentType(data)
	if mimeType == "application/octet-stream" || strings.HasPrefix(mimeType, "text/plain") {
		if byName := mime.TypeByExtension(filepath.Ext(name)); byName != "" {
			mimeType = byName
		}
	}
	bare, _, _ := strings.Cut(mimeType, ";")
	return strings.TrimSpace(bare)
}

// SendFile uploads a file and hands the phone a message carrying it, with caption as
// its text. In an SMS chat it goes as MMS.
func (a *Adapter) SendFile(ctx context.Context, room domain.RoomID, path, name, caption string) error {
	self, conversation, ok := conversationOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	c, client, err := a.connected(self)
	if err != nil {
		return err
	}
	via, ok := c.route(conversation)
	if !ok {
		return errNotListed
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("gmessages: read %s: %w", path, err)
	}
	if info.Size() > mediaMost {
		return errFileTooLarge
	}
	data, err := os.ReadFile(path) //nolint:gosec // the file the person chose to send
	if err != nil {
		return fmt.Errorf("gmessages: read %s: %w", path, err)
	}
	var media *gmproto.MediaContent
	err = a.guard("uploading a file", func() (err error) {
		media, err = client.UploadMedia(data, name, fileMedia(name, data))
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("gmessages: upload %s: %w", name, err)
	}
	if mediaType(media.GetMimeType()) == domain.MediaImage {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			media.Dimensions = &gmproto.Dimensions{Width: int64(cfg.Width), Height: int64(cfg.Height)}
		}
	}
	tmp := tmpFor("")
	parts := []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MediaContent{MediaContent: media}}}
	if caption = strings.TrimSpace(caption); caption != "" {
		parts = append(parts, &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MessageContent{
			MessageContent: &gmproto.MessageContent{Content: caption},
		}})
	}
	req := &gmproto.SendMessageRequest{
		ConversationID: conversation,
		MessagePayload: &gmproto.MessagePayload{
			TmpID: tmp, TmpID2: tmp, ConversationID: conversation, ParticipantID: via.outgoing, MessageInfo: parts,
		},
		SIMPayload: via.sim, TmpID: tmp, ForceRCS: via.forceRCS,
	}
	return a.hand(ctx, client, tmp, req)
}
