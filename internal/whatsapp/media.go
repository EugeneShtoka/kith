package whatsapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Attachments are decrypted with keys in the message, so kith keeps the media part in
// the media table's file_json as {"whatsapp": kind, "proto": base64}.

type mediaSource struct {
	Kind  string `json:"whatsapp"`
	Proto string `json:"proto"`
}

// Attachment kinds.
const (
	kindImage    = "image"
	kindVideo    = "video"
	kindAudio    = "audio"
	kindDocument = "document"
	kindSticker  = "sticker"
)

// attachment is a message's media and how to load it; nil when it has none.
func attachment(msg *waE2E.Message) (*domain.Media, *mediaSource) {
	var (
		media domain.Media
		part  proto.Message
		kind  string
	)
	switch {
	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		media = domain.Media{Type: domain.MediaImage, Mime: m.GetMimetype(), Width: int(m.GetWidth()), Height: int(m.GetHeight()), Size: bytesOf(m.GetFileLength())}
		part, kind = m, kindImage
	case msg.GetStickerMessage() != nil:
		m := msg.GetStickerMessage()
		media = domain.Media{Type: domain.MediaImage, Mime: m.GetMimetype(), Width: int(m.GetWidth()), Height: int(m.GetHeight()), Size: bytesOf(m.GetFileLength())}
		part, kind = m, kindSticker
	case msg.GetVideoMessage() != nil:
		m := msg.GetVideoMessage()
		media = domain.Media{Type: domain.MediaVideo, Mime: m.GetMimetype(), Width: int(m.GetWidth()), Height: int(m.GetHeight()), Size: bytesOf(m.GetFileLength())}
		part, kind = m, kindVideo
	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		media = domain.Media{Type: domain.MediaAudio, Mime: m.GetMimetype(), Size: bytesOf(m.GetFileLength())}
		if m.GetPTT() {
			media.Name = domain.VoiceMessage
		}
		part, kind = m, kindAudio
	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		media = domain.Media{Type: domain.MediaFile, Name: m.GetFileName(), Mime: m.GetMimetype(), Size: bytesOf(m.GetFileLength())}
		part, kind = m, kindDocument
	default:
		return nil, nil
	}
	encoded, err := proto.Marshal(part)
	if err != nil {
		return &media, nil
	}
	return &media, &mediaSource{Kind: kind, Proto: base64.StdEncoding.EncodeToString(encoded)}
}

func (s mediaSource) downloadable() (whatsmeow.DownloadableMessage, error) {
	raw, err := base64.StdEncoding.DecodeString(s.Proto)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: a kept attachment does not decode: %w", err)
	}
	var part interface {
		proto.Message
		whatsmeow.DownloadableMessage
	}
	switch s.Kind {
	case kindImage:
		part = &waE2E.ImageMessage{}
	case kindSticker:
		part = &waE2E.StickerMessage{}
	case kindVideo:
		part = &waE2E.VideoMessage{}
	case kindAudio:
		part = &waE2E.AudioMessage{}
	case kindDocument:
		part = &waE2E.DocumentMessage{}
	default:
		return nil, fmt.Errorf("whatsapp: a kept attachment of an unknown kind %q", s.Kind)
	}
	if err := proto.Unmarshal(raw, part); err != nil {
		return nil, fmt.Errorf("whatsapp: a kept attachment does not decode: %w", err)
	}
	return part, nil
}

func (a *Adapter) keepSource(ctx context.Context, msg domain.Message, source *mediaSource) {
	if source == nil || a.cache == nil {
		return
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return
	}
	if err := a.cache.SaveMediaSource(ctx, msg.ID, msg.RoomID, "", string(encoded)); err != nil {
		a.log.Warn("keep an attachment's source failed", "room", msg.RoomID, "err", err)
	}
}

// LoadImage downloads and decrypts a message's attachment; caching is the client's.
func (a *Adapter) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	if a.cache == nil {
		return nil, fmt.Errorf("whatsapp: no cache to find the attachment of %s in", eventID)
	}
	_, fileJSON, ok, err := a.cache.MediaSource(ctx, roomID, eventID)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read the attachment's source: %w", err)
	}
	var source mediaSource
	if !ok || json.Unmarshal([]byte(fileJSON), &source) != nil || source.Kind == "" {
		return nil, fmt.Errorf("whatsapp: %s has no attachment kith can load", eventID)
	}
	part, err := source.downloadable()
	if err != nil {
		return nil, err
	}
	_, client, connected := a.clientFor(domain.ParseID(string(roomID)).Account)
	if !connected {
		return nil, fmt.Errorf("whatsapp: account %s is not connected: %w", domain.ParseID(string(roomID)).Account, errNetworkOff)
	}
	data, err := client.Download(ctx, part)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: download the attachment of %s: %w", eventID, err)
	}
	return data, nil
}

// SendFile uploads and sends a file with an optional caption: image, video or audio
// by type, else a named document.
func (a *Adapter) SendFile(ctx context.Context, roomID domain.RoomID, path, name, caption string) error {
	if err := a.mayPost(roomID); err != nil {
		return err
	}
	id := domain.ParseID(string(roomID))
	chat, err := types.ParseJID(id.Native)
	if err != nil {
		return fmt.Errorf("whatsapp: %s is not a chat: %w", roomID, err)
	}
	account, client, ok := a.clientFor(id.Account)
	if !ok {
		return fmt.Errorf("whatsapp: account %s is not connected: %w", id.Account, errNetworkOff)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own file, picked by them to send
	if err != nil {
		return fmt.Errorf("whatsapp: read %s: %w", path, err)
	}
	message, kind := fileMessage(name, data, caption)
	up, err := client.Upload(ctx, data, kind)
	if err != nil {
		return fmt.Errorf("whatsapp: upload %s: %w", filepath.Base(path), err)
	}
	withUpload(message, up)
	a.withTimer(ctx, roomID, message)
	resp, err := client.SendMessage(ctx, chat, message)
	if err != nil {
		return fmt.Errorf("whatsapp: send %s to %s: %w", filepath.Base(path), roomID, err)
	}
	media, source := attachment(message)
	sent := domain.Message{
		ID:     domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, account.Digits, resp.ID)),
		RoomID: roomID, Sender: domain.NativePerson(domain.ProtocolWhatsApp, selfOf(client).pn.String()),
		Body: caption, Media: media, Timestamp: resp.Timestamp,
	}
	if media != nil && media.Type == domain.MediaFile && caption == "" {
		sent.Body = media.Name
	}
	if a.cache != nil {
		a.record(ctx, account, sent, func() {})
		a.keepSource(ctx, sent, source)
	}
	emit(a, a.messages, sent)
	return nil
}

// fileMessage is a file's message and upload media type.
func fileMessage(name string, data []byte, caption string) (*waE2E.Message, whatsmeow.MediaType) {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	mimeType, _, _ = strings.Cut(mimeType, ";")
	var words *string
	if caption != "" {
		words = &caption
	}
	size := uint64(len(data))
	switch {
	case strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml":
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: &mimeType, Caption: words, FileLength: &size}}, whatsmeow.MediaImage
	case strings.HasPrefix(mimeType, "video/"):
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Mimetype: &mimeType, Caption: words, FileLength: &size}}, whatsmeow.MediaVideo
	case strings.HasPrefix(mimeType, "audio/"):
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: &mimeType, FileLength: &size}}, whatsmeow.MediaAudio
	}
	return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		Mimetype: &mimeType, FileName: &name, Title: &name, Caption: words, FileLength: &size,
	}}, whatsmeow.MediaDocument
}

// bytesOf converts WhatsApp's uint64 size.
func bytesOf(n uint64) int {
	if n > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(n)
}

func withUpload(message *waE2E.Message, up whatsmeow.UploadResponse) {
	url, path := up.URL, up.DirectPath
	switch {
	case message.GetImageMessage() != nil:
		m := message.GetImageMessage()
		m.URL, m.DirectPath, m.MediaKey, m.FileEncSHA256, m.FileSHA256 = &url, &path, up.MediaKey, up.FileEncSHA256, up.FileSHA256
	case message.GetVideoMessage() != nil:
		m := message.GetVideoMessage()
		m.URL, m.DirectPath, m.MediaKey, m.FileEncSHA256, m.FileSHA256 = &url, &path, up.MediaKey, up.FileEncSHA256, up.FileSHA256
	case message.GetAudioMessage() != nil:
		m := message.GetAudioMessage()
		m.URL, m.DirectPath, m.MediaKey, m.FileEncSHA256, m.FileSHA256 = &url, &path, up.MediaKey, up.FileEncSHA256, up.FileSHA256
	case message.GetDocumentMessage() != nil:
		m := message.GetDocumentMessage()
		m.URL, m.DirectPath, m.MediaKey, m.FileEncSHA256, m.FileSHA256 = &url, &path, up.MediaKey, up.FileEncSHA256, up.FileSHA256
	}
}
