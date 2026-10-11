package telegram

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Attachments are photos or documents. File references expire, so LoadImage refetches
// the message and downloads from its current media; the client's media cache caches.

// attachment converts a message's media; nil plus a label for unsupported kinds
// (location, poll, animated sticker).
func attachment(media tg.MessageMediaClass) (*domain.Media, string) {
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			return nil, "[photo]" // one that destroyed itself
		}
		size, ok := largest(photo)
		if !ok {
			return nil, "[photo]"
		}
		return &domain.Media{Type: domain.MediaImage, Mime: "image/jpeg", Width: size.w, Height: size.h, Size: size.bytes}, ""
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return nil, "[file]"
		}
		return document(doc)
	case *tg.MessageMediaWebPage:
		return nil, ""
	}
	return nil, mediaLabel(media)
}

// document classifies a document by its attributes.
func document(doc *tg.Document) (*domain.Media, string) {
	media := &domain.Media{Type: domain.MediaFile, Mime: doc.MimeType, Size: int(doc.Size)}
	sticker, video := "", false
	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeFilename:
			media.Name = a.FileName
		case *tg.DocumentAttributeImageSize:
			media.Width, media.Height = a.W, a.H
		case *tg.DocumentAttributeVideo:
			media.Width, media.Height, video = a.W, a.H, true
		case *tg.DocumentAttributeAnimated:
			video = true
		case *tg.DocumentAttributeSticker:
			sticker = a.Alt
		case *tg.DocumentAttributeAudio:
			media.Type = domain.MediaAudio
			if a.Voice {
				media.Name = domain.VoiceMessage
			} else if media.Name == "" {
				media.Name = strings.TrimSpace(a.Performer + " " + a.Title)
			}
		}
	}
	switch {
	case sticker != "" || doc.MimeType == "application/x-tgsticker":
		if doc.MimeType != "image/webp" { // animated: Lottie or video, nothing to draw
			return nil, strings.TrimSpace("[sticker] " + sticker)
		}
		media.Type = domain.MediaImage
	case video: // a video's sound track does not make it audio
		media.Type = domain.MediaVideo
	case strings.HasPrefix(doc.MimeType, "image/") && doc.MimeType != "image/svg+xml":
		media.Type = domain.MediaImage
	}
	return media, ""
}

type photoSize struct {
	kind        string
	w, h, bytes int
}

// largest is a photo's largest size, the one kith downloads.
func largest(photo *tg.Photo) (photoSize, bool) {
	var best photoSize
	for _, s := range photo.Sizes {
		var size photoSize
		switch s := s.(type) {
		case *tg.PhotoSize:
			size = photoSize{s.Type, s.W, s.H, s.Size}
		case *tg.PhotoSizeProgressive:
			size = photoSize{kind: s.Type, w: s.W, h: s.H}
			if n := len(s.Sizes); n > 0 {
				size.bytes = s.Sizes[n-1]
			}
		default:
			continue // a thumbnail inlined, or a path
		}
		if size.w*size.h > best.w*best.h {
			best = size
		}
	}
	return best, best.kind != ""
}

// fileLocation is an attachment's current download location.
func fileLocation(media tg.MessageMediaClass) (tg.InputFileLocationClass, bool) {
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			return nil, false
		}
		size, ok := largest(photo)
		return &tg.InputPhotoFileLocation{
			ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: size.kind,
		}, ok
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return nil, false
		}
		return &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}, true
	}
	return nil, false
}

// mediaLabel is placeholder text for an unsupported attachment.
func mediaLabel(media tg.MessageMediaClass) string {
	switch m := media.(type) {
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "[location]"
	case *tg.MessageMediaContact:
		return "[contact] " + strings.TrimSpace(m.FirstName+" "+m.LastName)
	case *tg.MessageMediaPoll:
		return "[poll] " + m.Poll.Question.Text
	case *tg.MessageMediaDice:
		return "[dice] " + m.Emoticon + " " + strconv.Itoa(m.Value)
	}
	return "[attachment]"
}

// LoadImage refetches the message for a fresh file reference and downloads its media.
func (a *Adapter) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return nil, err
	}
	id, ok := messageNumber(roomID, eventID)
	if !ok {
		return nil, fmt.Errorf("telegram: %s is no message of %s", eventID, roomID)
	}
	m, _, err := a.fetchRaw(ctx, ch, id)
	if err != nil {
		return nil, fmt.Errorf("telegram: fetch %s: %w", eventID, err)
	}
	msg, ok := m.(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("telegram: %s has no attachment kith can load", eventID)
	}
	loc, ok := fileLocation(msg.Media)
	if !ok {
		return nil, fmt.Errorf("telegram: %s has no attachment kith can load", eventID)
	}
	var out bytes.Buffer
	if _, err := downloader.NewDownloader().Download(ch.conn.client.API(), loc).Stream(ctx, &out); err != nil {
		return nil, fmt.Errorf("telegram: download the attachment of %s: %w", eventID, err)
	}
	return out.Bytes(), nil
}

// SendFile uploads and sends a file with a Markdown caption: images as photos, the
// rest as documents (Telegram renders audio/video by MIME type).
func (a *Adapter) SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own file, picked by them to send
	if err != nil {
		return fmt.Errorf("telegram: read %s: %w", path, err)
	}
	name := filepath.Base(path)
	file, err := uploader.NewUploader(ch.conn.client.API()).FromBytes(ctx, name, data)
	if err != nil {
		return fmt.Errorf("telegram: upload %s: %w", name, err)
	}
	text, entities, _ := outgoing(domain.Draft{Body: caption}, a.userHash(ctx, ch.conn))
	req := &tg.MessagesSendMediaRequest{Peer: ch.peer, Media: inputMedia(file, name, data), Message: text, RandomID: randomID("")}
	if len(entities) > 0 {
		req.SetEntities(entities)
	}
	res, err := ch.conn.client.API().MessagesSendMedia(ctx, req)
	if err != nil {
		return fmt.Errorf("telegram: send %s to %s: %w", name, roomID, err)
	}
	if ch.conn.live != nil {
		_ = ch.conn.live.manager.Handle(ctx, res)
	}
	id, _ := sentAs(res, req.RandomID)
	for _, m := range updatedMessages(res) {
		if m.GetID() != id {
			continue
		}
		if sent, ok := incoming(ch.conn.user, m, peerEntities(res)); ok && sent.RoomID == roomID {
			return a.arrived(ctx, ch.conn.account, ch.conn.user, sent)
		}
	}
	return nil // sent; it arrives through the updates
}

// inputMedia is a photo for images Telegram renders, else a named, typed document.
func inputMedia(file tg.InputFileClass, name string, data []byte) tg.InputMediaClass {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	mimeType, _, _ = strings.Cut(mimeType, ";")
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp":
		return &tg.InputMediaUploadedPhoto{File: file}
	}
	return &tg.InputMediaUploadedDocument{
		File: file, MimeType: mimeType,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}},
	}
}
