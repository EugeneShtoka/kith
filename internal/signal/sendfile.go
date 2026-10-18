package signal

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"  // a GIF's size
	_ "image/jpeg" // a photo's size
	_ "image/png"  // a picture's size
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// fileMost is the largest file Signal takes.
const fileMost = 100 << 20

var errFileTooLarge = fmt.Errorf("signal: a file is at most %d MiB", fileMost>>20)

// SendFile sends the file at path under name, with caption as its text: uploaded
// through the helper, then sent as a message carrying it (and the chat's timer), and
// cached as the account's own.
func (a *Adapter) SendFile(ctx context.Context, room domain.RoomID, path, name, caption string) error {
	self, chat, group, err := a.chatTo(room)
	if err != nil {
		return err
	}
	h, err := a.sender(self)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) //nolint:gosec // the file the person chose to send
	if err != nil {
		return fmt.Errorf("signal: read %s: %w", path, err)
	}
	if len(data) > fileMost {
		return errFileTooLarge
	}
	up := uploadOf(name, data)
	up.Account = self
	uploaded, err := h.client.Upload(ctx, connect.NewRequest(up))
	if err != nil {
		return fmt.Errorf("signal: upload %s: %w", name, err)
	}
	file := uploaded.Msg.GetAttachment()
	sent := a.sentAt("")
	req := &signalv1.SendRequest{
		Account: self, Chat: chat, Group: group, Body: strings.TrimSpace(caption), Sent: sent,
		Attachments: []*signalv1.Attachment{file},
	}
	req.Timer, req.TimerVersion = a.timerOf(ctx, room)
	if _, err := h.client.Send(ctx, connect.NewRequest(req)); err != nil {
		return fmt.Errorf("signal: send %s to %s: %w", name, room, err)
	}
	return a.takeMessage(ctx, self, &signalv1.Message{
		Chat: chat, Group: group, Sender: self, Sent: sent, Received: sent, Body: req.GetBody(),
		Attachments: req.GetAttachments(), Timer: req.GetTimer(), TimerVersion: req.GetTimerVersion(),
	})
}

// uploadOf is a file sent as name: its type from its contents (then its name), a
// picture's size read from it.
func uploadOf(name string, data []byte) *signalv1.UploadRequest {
	mimeType := http.DetectContentType(data)
	if mimeType == "application/octet-stream" || strings.HasPrefix(mimeType, "text/plain") {
		if byName := mime.TypeByExtension(filepath.Ext(name)); byName != "" {
			mimeType = byName
		}
	}
	up := &signalv1.UploadRequest{Data: data, ContentType: mimeType, FileName: name}
	if strings.HasPrefix(mimeType, "image/") {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			up.Width, up.Height = uint32(cfg.Width), uint32(cfg.Height) //nolint:gosec // a picture's size
		}
	}
	return up
}
