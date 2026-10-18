//go:build signal

package meow

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"google.golang.org/protobuf/proto"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/signalhelper"
)

// Download fetches an attachment from Signal's CDN by its pointer and decrypts it.
func (n *Network) Download(ctx context.Context, pointer []byte) ([]byte, error) {
	p := &signalpb.AttachmentPointer{}
	if err := proto.Unmarshal(pointer, p); err != nil {
		return nil, fmt.Errorf("signal: read the attachment's pointer: %w", err)
	}
	data, err := signalmeow.DownloadAttachmentWithPointer(ctx, p, nil, nil)
	if errors.Is(err, signalmeow.ErrAttachmentNotFound) {
		return nil, signalhelper.ErrGone
	}
	if err != nil {
		return nil, fmt.Errorf("signal: download an attachment: %w", err)
	}
	return data, nil
}

// Upload encrypts a file and puts it on Signal's CDN from a connected account. Audio in
// AAC goes as a voice message, as Signal's apps record them.
func (n *Network) Upload(ctx context.Context, req *signalv1.UploadRequest) (*signalv1.Attachment, error) {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return nil, err
	}
	p, err := a.cli.UploadAttachment(ctx, req.GetData())
	if err != nil {
		return nil, fmt.Errorf("signal: upload %s: %w", req.GetFileName(), err)
	}
	p.ContentType, p.FileName = new(req.GetContentType()), new(req.GetFileName())
	if w, h := req.GetWidth(), req.GetHeight(); w > 0 && h > 0 {
		p.Width, p.Height = new(w), new(h)
	}
	if req.GetContentType() == "audio/aac" {
		p.Flags = new(uint32(signalpb.AttachmentPointer_VOICE_MESSAGE))
	}
	pointer, err := proto.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("signal: keep the attachment's pointer: %w", err)
	}
	return &signalv1.Attachment{
		ContentType: req.GetContentType(), FileName: req.GetFileName(), Size: uint64(len(req.GetData())), Pointer: pointer,
	}, nil
}
