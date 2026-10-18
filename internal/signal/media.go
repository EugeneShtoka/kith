package signal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message shows its first attachment; any further one is counted in its text. Its
// bytes are fetched when asked for, through the helper (Signal's files are encrypted,
// and opening them takes libsignal), so media.file_json keeps the attachment's pointer:
// where it is on Signal's CDN and its keys. Signal keeps an attachment for a while
// only; then it stops loading.

// mediaSource is where an attachment is: its pointer, as the helper reads it.
type mediaSource struct {
	Pointer []byte `json:"pointer"`
}

var (
	errNoMedia = errors.New("signal: that message has no attachment kith can load")
	errGone    = errors.New("signal: Signal no longer has that attachment (it keeps them for a while only)")
)

// keepSource records where msg's first attachment is, once msg is cached.
func (a *Adapter) keepSource(ctx context.Context, msg domain.Message, files []*signalv1.Attachment) {
	if a.cache == nil || msg.Media == nil || len(files) == 0 || len(files[0].GetPointer()) == 0 {
		return
	}
	raw, err := json.Marshal(mediaSource{Pointer: files[0].GetPointer()})
	if err != nil {
		return
	}
	if err := a.cache.SaveMediaSource(ctx, msg.ID, msg.RoomID, "", string(raw)); err != nil {
		a.log.Warn("keep an attachment's source failed", "room", msg.RoomID, "err", err)
	}
}

// LoadImage fetches a message's attachment through the helper. It caches nothing; the
// client's media cache does.
func (a *Adapter) LoadImage(ctx context.Context, room domain.RoomID, event domain.EventID) ([]byte, error) {
	if a.cache == nil {
		return nil, errNoMedia
	}
	_, raw, ok, err := a.cache.MediaSource(ctx, room, event)
	if err != nil {
		return nil, fmt.Errorf("signal: read the attachment's source: %w", err)
	}
	var s mediaSource
	if !ok || json.Unmarshal([]byte(raw), &s) != nil || len(s.Pointer) == 0 {
		return nil, fmt.Errorf("%w: %s", errNoMedia, event)
	}
	a.mu.Lock()
	h := a.helper
	a.mu.Unlock()
	if h == nil {
		return nil, fmt.Errorf("%w: Signal is not running", api.ErrNetworkOff)
	}
	resp, err := h.client.Download(ctx, connect.NewRequest(&signalv1.DownloadRequest{Pointer: s.Pointer}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil, fmt.Errorf("%w: %s", errGone, event)
	}
	if err != nil {
		return nil, fmt.Errorf("signal: download the attachment of %s: %w", event, err)
	}
	return resp.Msg.GetData(), nil
}
