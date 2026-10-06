package matrix

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// saveMediaSource records an attachment's fetch source for on-demand download. Best-effort.
func (b *InProc) saveMediaSource(ctx context.Context, roomID domain.RoomID, evt *event.Event) {
	if b.cache == nil {
		return
	}
	content := evt.Content.AsMessage()
	target := domain.EventID(evt.ID)
	if replaced := content.RelatesTo.GetReplaceID(); replaced != "" {
		if content.NewContent == nil {
			return
		}
		// An edit bringing an attachment: its source is the replaced message's, which
		// is the one the timeline shows.
		content, target = content.NewContent, domain.EventID(replaced)
	}
	mxc, fileJSON := mediaSource(content)
	if mxc == "" {
		return
	}
	b.warnIf(ctx, b.cache.SaveMediaSource(ctx, target, roomID, mxc, fileJSON), "cache media source", "room", roomID, "event", target)
}

// LoadImage downloads (and decrypts) a message's attachment. It caches nothing: the
// client's media cache (internal/media) is the cache.
func (b *InProc) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	if b.cache == nil {
		return nil, fmt.Errorf("matrix: no cache to resolve media %s", eventID)
	}
	mxc, fileJSON, ok, err := b.cache.MediaSource(ctx, roomID, eventID)
	if err != nil {
		return nil, fmt.Errorf("matrix: media source: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("matrix: no media source for %s", eventID)
	}
	uri, err := id.ParseContentURI(mxc)
	if err != nil {
		return nil, fmt.Errorf("matrix: parse mxc %q: %w", mxc, err)
	}
	data, err := b.client.DownloadBytes(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("matrix: download media: %w", err)
	}
	if fileJSON != "" {
		var ef event.EncryptedFileInfo
		if err := json.Unmarshal([]byte(fileJSON), &ef); err != nil {
			return nil, fmt.Errorf("matrix: parse encrypted file: %w", err)
		}
		if err := ef.DecryptInPlace(data); err != nil {
			return nil, fmt.Errorf("matrix: decrypt media: %w", err)
		}
	}
	return data, nil
}
