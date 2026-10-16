package meta

import (
	"bytes"
	"cmp"
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
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A file goes the way the chat's messages come: to a plain thread it is uploaded to
// Meta and sent as a message naming the upload; to an encrypted chat it is encrypted,
// uploaded over the encrypted connection, and sent as a photo, video, voice message or
// file. What you typed goes with it, as its caption (a photo's and a video's) or its
// text. The sent message is cached at once.

var (
	errFileTooLarge = fmt.Errorf("meta: a file is at most %d MiB", mediaMost>>20)
	errUploadNoID   = errors.New("meta: Meta took the upload but named no file")
)

// SendFile sends the file at path under name (its own, or a neutral one where names
// are anonymized) with caption.
func (a *Adapter) SendFile(ctx context.Context, room domain.RoomID, path, name, caption string) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAThread, room)
	}
	c := a.connOf(self)
	if c == nil {
		return errNotConnected
	}
	data, err := os.ReadFile(path) //nolint:gosec // the file the person chose to send
	if err != nil {
		return fmt.Errorf("meta: read %s: %w", path, err)
	}
	if len(data) > mediaMost {
		return errFileTooLarge
	}
	media := fileMediaOf(name, data)
	c.applying.Lock()
	t := c.inbox.threads[thread]
	encrypted, group, request := t != nil && t.kind.IsWhatsApp(), t != nil && !t.direct(), t != nil && t.folder == "pending"
	c.applying.Unlock()
	var sent domain.Message
	var source mediaSource
	if encrypted {
		sent, source, err = a.fileEncrypted(ctx, c, thread, group, data, media, strings.TrimSpace(caption))
	} else {
		sent, err = a.filePlain(ctx, c, thread, request, data, media, strings.TrimSpace(caption))
	}
	if err != nil {
		return err
	}
	if a.cache != nil {
		a.cacheMessages(ctx, c, map[domain.RoomID][]domain.Message{room: {sent}}, []domain.Message{sent}, map[domain.RoomID]bool{})
		a.keepSource(ctx, sent, source) // a plain one's comes with Meta's echo
		a.recount(ctx, room)
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

// filePlain uploads a file to Meta and sends it to a plain thread; request marks a
// message request, which replying accepts.
func (a *Adapter) filePlain(ctx context.Context, c *conn, thread int64, request bool, data []byte, media *domain.Media, caption string) (domain.Message, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return domain.Message{}, errNotConnected
	}
	otid := methods.GenerateEpochID()
	task := &socket.SendMessageTask{
		ThreadId: thread, Otid: otid, Source: table.MESSENGER_INBOX_IN_THREAD, InitiatingSource: table.FACEBOOK_INBOX,
		SendType: table.MEDIA, SyncGroup: 1, Text: caption,
	}
	if request {
		task.Source = table.MESSENGER_INBOX_PENDING_REQUESTS
	}
	var resp *table.LSTable
	err := a.guard("sending a file", func() error {
		if err := client.WaitUntilCanSendMessages(ctx, sendWait); err != nil {
			return fmt.Errorf("%w: %w", errNotConnected, err)
		}
		up, err := client.GetHTTP().SendMercuryUploadRequest(ctx, thread, &httpclient.MercuryUploadMedia{
			Filename: media.Name, MimeType: media.Mime, MediaData: data, IsVoiceClip: media.Type == domain.MediaAudio,
		})
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		id := up.Payload.RealMetadata.GetFbId()
		if id == 0 {
			return errUploadNoID
		}
		task.AttachmentFBIds = []int64{id}
		resp, err = client.ExecuteTasks(ctx, task)
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("meta: send %s: %w", media.Name, err)
	}
	mid, err := sentID(resp, otid)
	if err != nil {
		return domain.Message{}, err
	}
	msg := domain.Message{
		ID: eventID(c.metaID, thread, mid), RoomID: roomID(c.metaID, thread), Sender: personID(c.metaID),
		Body: cmp.Or(caption, media.Name), Media: media, Timestamp: time.Now(),
	}
	if ms, err := methods.ParseMessageID(mid); err == nil && ms > 0 {
		msg.Timestamp = time.UnixMilli(ms)
	}
	return msg, nil
}

// fileEncrypted encrypts and uploads a file over the encrypted connection and sends it
// to an encrypted chat, returning the message and where its attachment is.
func (a *Adapter) fileEncrypted(ctx context.Context, c *conn, thread int64, group bool, data []byte, media *domain.Media, caption string) (domain.Message, mediaSource, error) {
	c.mu.Lock()
	wa := c.e2ee
	c.mu.Unlock()
	if wa == nil {
		return domain.Message{}, mediaSource{}, errNoEncrypted
	}
	chat := types.JID{User: itoa(thread), Server: types.MessengerServer}
	if group {
		chat.Server = types.GroupServer
	}
	kind := uploadKind(media.Type)
	id := strconv.FormatInt(methods.GenerateEpochID(), 10)
	var source mediaSource
	err := a.guard("sending an encrypted file", func() error {
		up, err := wa.Upload(ctx, data, kind)
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		content, err := encryptedFile(up, media, caption)
		if err != nil {
			return err
		}
		source = mediaSource{Mime: media.Mime, Encrypted: &encryptedSource{
			Kind: kind, Path: up.DirectPath, Key: up.MediaKey, SHA256: up.FileSHA256, EncSHA256: up.FileEncSHA256,
		}}
		_, err = wa.SendFBMessage(ctx, chat, content, &waMsgApplication.MessageApplication_Metadata{}, whatsmeow.SendRequestExtra{ID: id})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return domain.Message{}, mediaSource{}, fmt.Errorf("meta: send %s to an encrypted chat: %w", media.Name, err)
	}
	return domain.Message{
		ID: eventID(c.metaID, thread, id), RoomID: roomID(c.metaID, thread), Sender: personID(c.metaID),
		Body: cmp.Or(caption, media.Name), Media: media, Timestamp: time.Now(),
	}, source, nil
}

// uploadKind is what a file is uploaded as, which its keys are derived for: the kind
// its message is read back as, or it cannot be opened.
func uploadKind(kind domain.MediaType) whatsmeow.MediaType {
	switch kind {
	case domain.MediaImage:
		return whatsmeow.MediaImage
	case domain.MediaVideo:
		return whatsmeow.MediaVideo
	case domain.MediaAudio:
		return whatsmeow.MediaAudio
	case domain.MediaFile:
	}
	return whatsmeow.MediaDocument
}

// encryptedFile is an uploaded file as the encrypted connection sends it: a photo or
// video with its caption, a voice message, or a file named as it was. Messenger's apps
// show media only with a thumbnail size, at most 400 on a side.
func encryptedFile(up whatsmeow.UploadResponse, media *domain.Media, caption string) (*waConsumerApplication.ConsumerApplication, error) {
	w, h := thumbSize(media.Width, media.Height)
	length := uint64(media.Size) // #nosec G115 -- a file's length, checked against mediaMost
	at := time.Now().Unix()
	transport := &waMediaTransport.WAMediaTransport{
		Integral: &waMediaTransport.WAMediaTransport_Integral{
			FileSHA256: up.FileSHA256, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256,
			DirectPath: &up.DirectPath, MediaKeyTimestamp: &at,
		},
		Ancillary: &waMediaTransport.WAMediaTransport_Ancillary{
			FileLength: &length, Mimetype: &media.Mime, ObjectID: &up.ObjectID,
			Thumbnail: &waMediaTransport.WAMediaTransport_Ancillary_Thumbnail{ThumbnailWidth: &w, ThumbnailHeight: &h},
		},
	}
	text := &waCommon.MessageText{Text: &caption}
	content := &waConsumerApplication.ConsumerApplication_Content{}
	var err error
	switch media.Type {
	case domain.MediaImage:
		msg := &waConsumerApplication.ConsumerApplication_ImageMessage{Caption: text}
		err = msg.Set(&waMediaTransport.ImageTransport{
			Integral:  &waMediaTransport.ImageTransport_Integral{Transport: transport},
			Ancillary: &waMediaTransport.ImageTransport_Ancillary{Width: &w, Height: &h},
		})
		content.Content = &waConsumerApplication.ConsumerApplication_Content_ImageMessage{ImageMessage: msg}
	case domain.MediaVideo:
		msg := &waConsumerApplication.ConsumerApplication_VideoMessage{Caption: text}
		err = msg.Set(&waMediaTransport.VideoTransport{
			Integral:  &waMediaTransport.VideoTransport_Integral{Transport: transport},
			Ancillary: &waMediaTransport.VideoTransport_Ancillary{Width: &w, Height: &h},
		})
		content.Content = &waConsumerApplication.ConsumerApplication_Content_VideoMessage{VideoMessage: msg}
	case domain.MediaAudio:
		voice := true
		msg := &waConsumerApplication.ConsumerApplication_AudioMessage{PTT: &voice}
		err = msg.Set(&waMediaTransport.AudioTransport{
			Integral:  &waMediaTransport.AudioTransport_Integral{Transport: transport},
			Ancillary: &waMediaTransport.AudioTransport_Ancillary{},
		})
		content.Content = &waConsumerApplication.ConsumerApplication_Content_AudioMessage{AudioMessage: msg}
	default:
		msg := &waConsumerApplication.ConsumerApplication_DocumentMessage{FileName: &media.Name}
		err = msg.Set(&waMediaTransport.DocumentTransport{
			Integral:  &waMediaTransport.DocumentTransport_Integral{Transport: transport},
			Ancillary: &waMediaTransport.DocumentTransport_Ancillary{},
		})
		content.Content = &waConsumerApplication.ConsumerApplication_Content_DocumentMessage{DocumentMessage: msg}
	}
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", media.Name, err)
	}
	return &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: content},
	}}, nil
}

// thumbSize is a picture's size scaled to at most 400 on a side; 400 square for one of
// no known size.
func thumbSize(width, height int) (uint32, uint32) {
	if width <= 0 || height <= 0 {
		return 400, 400
	}
	if width > 400 {
		height, width = height*400/width, 400
	}
	if height > 400 {
		width, height = width*400/height, 400
	}
	return uint32(max(width, 1)), uint32(max(height, 1)) // #nosec G115 -- at most 400
}
