package matrix

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	_ "image/gif"  // registered for DecodeConfig: dimensions come from the header
	_ "image/jpeg" // …
	_ "image/png"  // …

	_ "golang.org/x/image/webp" // … and WebP, which is what a sticker is

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Sending a file. Most rooms are encrypted, so encrypt-then-upload is the common
// path. The info block (mimetype, size, image dimensions) must be complete for other
// clients to draw it, and the msgtype comes from sniffed content, not the extension.
// The client passes an absolute path: both processes are the same user.

// maxSniff is how much is read to identify a file (what DetectContentType uses).
const maxSniff = 512

// SendFile uploads a file (encrypted where the room is) and posts it. An empty
// caption means the filename is the body.
func (b *InProc) SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error {
	file, err := os.Open(path) // #nosec G304 -- the user's own path, over a 0600 socket
	if err != nil {
		return fmt.Errorf("matrix: open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = file.Close() }() // opened read-only: nothing is lost if closing fails

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("matrix: stat %s: %w", filepath.Base(path), err)
	}
	if info.IsDir() {
		return fmt.Errorf("matrix: %s is a directory", filepath.Base(path))
	}
	if info.Size() == 0 {
		// An empty upload yields an attachment nobody can open.
		return fmt.Errorf("matrix: %s is empty", filepath.Base(path))
	}

	name := filepath.Base(path)
	mimetype, err := sniff(file, name)
	if err != nil {
		return err
	}
	content := attachmentContent(name, caption, mimetype, info.Size())
	addDimensions(file, &content)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("matrix: rewind %s: %w", name, err)
	}

	// Sealed across both steps: refused before any byte is uploaded, and the room
	// cannot gain a crypto machine between encrypting the file and sending its key.
	return b.sealed(ctx, roomID, func() error {
		if err := b.upload(ctx, roomID, file, info.Size(), &content); err != nil {
			return err
		}
		if _, err := b.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, &content); err != nil {
			return fmt.Errorf("matrix: send %s: %w", name, err)
		}
		return nil
	})
}

// upload puts the bytes on the homeserver, encrypted when the room is. Encrypted
// attachments set File (URL and key together), never a bare URL.
func (b *InProc) upload(ctx context.Context, roomID domain.RoomID, r io.Reader, size int64, content *event.MessageEventContent) error {
	if !b.roomEncrypted(ctx, roomID) {
		resp, err := b.client.UploadMedia(ctx, mautrix.ReqUploadMedia{
			Content:       r,
			ContentLength: size,
			ContentType:   content.Info.MimeType,
			FileName:      content.Body,
		})
		if err != nil {
			return fmt.Errorf("matrix: upload: %w", err)
		}
		content.URL = resp.ContentURI.CUString()
		return nil
	}

	// Streamed, so a large file is never held twice.
	crypt := attachment.NewEncryptedFile()
	encrypted := crypt.EncryptStream(r)

	resp, err := b.client.UploadMedia(ctx, mautrix.ReqUploadMedia{
		Content:       encrypted,
		ContentLength: size,
		ContentType:   "application/octet-stream", // what it now is, on the wire
	})
	if err != nil {
		_ = encrypted.Close() // ignored: cleanup; the upload error is the one to report
		return fmt.Errorf("matrix: upload (encrypted): %w", err)
	}
	// Close before reading the keys, not in a defer: mautrix fills in the SHA256 on
	// Close, and without it recipients cannot decrypt.
	if err := encrypted.Close(); err != nil {
		return fmt.Errorf("matrix: finish encrypting: %w", err)
	}
	content.File = &event.EncryptedFileInfo{
		EncryptedFile: *crypt,
		URL:           resp.ContentURI.CUString(),
	}
	return nil
}

// roomEncrypted reports whether a room is encrypted, failing closed (true) when the
// state store cannot answer. The store has no row for a plain room and none for a
// room whose state it has not seen yet (a new or reset store), so "not encrypted" is
// believed only for a room whose state it holds: every room has power levels.
func (b *InProc) roomEncrypted(ctx context.Context, roomID domain.RoomID) bool {
	if b.client == nil {
		return true
	}
	store := b.stateStore()
	if store == nil {
		return true
	}
	encrypted, err := store.IsEncrypted(ctx, id.RoomID(roomID))
	if err != nil {
		b.warnIf(ctx, err, "read room encryption state; treating it as encrypted", "room", roomID)
		return true
	}
	if encrypted {
		return true
	}
	levels, err := store.GetPowerLevels(ctx, id.RoomID(roomID))
	if err != nil || levels == nil {
		b.warnIf(ctx, err, "read room power levels; treating the room as encrypted", "room", roomID)
		return true // its state is not known yet
	}
	return false
}

// sniff identifies a file by its first bytes, falling back to the extension when
// detection is generic.
func sniff(f io.ReadSeeker, name string) (string, error) {
	head := make([]byte, maxSniff)
	n, err := f.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("matrix: read %s: %w", name, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("matrix: rewind %s: %w", name, err)
	}
	detected := http.DetectContentType(head[:n])
	if detected == "application/octet-stream" || strings.HasPrefix(detected, "text/plain") {
		if byExt := mime.TypeByExtension(filepath.Ext(name)); byExt != "" {
			return byExt, nil
		}
	}
	return detected, nil
}

// attachmentContent builds a file's event. With a caption the body is the caption and
// FileName the name; without one the body is the name.
func attachmentContent(name, caption, mimetype string, size int64) event.MessageEventContent {
	content := event.MessageEventContent{
		MsgType: msgTypeFor(mimetype),
		Body:    name,
		Info: &event.FileInfo{
			MimeType: mimetype,
			Size:     int(size),
		},
	}
	if caption != "" {
		content.Body = caption
		content.FileName = name
	}
	return content
}

// msgTypeFor maps a mimetype to a msgtype; unknown is m.file.
func msgTypeFor(mimetype string) event.MessageType {
	switch {
	case strings.HasPrefix(mimetype, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mimetype, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(mimetype, "audio/"):
		return event.MsgAudio
	default:
		return event.MsgFile
	}
}

// addDimensions fills in an image's width and height from its header; failure is silent.
func addDimensions(f io.ReadSeeker, content *event.MessageEventContent) {
	if content.MsgType != event.MsgImage {
		return
	}
	defer f.Seek(0, io.SeekStart) //nolint:errcheck // the caller rewinds before uploading
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return // ignored: an image we cannot measure is sent without dimensions
	}
	content.Info.Width, content.Info.Height = cfg.Width, cfg.Height
}

// RoomEncryption reports which of these rooms are encrypted, erring towards yes.
func (b *InProc) RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, roomID := range roomIDs {
		out[roomID] = b.roomEncrypted(ctx, roomID)
	}
	return out, nil
}
