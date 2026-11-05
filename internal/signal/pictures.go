package signal

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// A chat's picture is its person's (their profile's, else the phone's contact photo) or
// the group's own, as the helper asks Signal for it (Picture). Signal names no version
// in what kith hears, so each chat is asked at most every pictureEvery, by the account's
// sweeper (internal/pictures), when the account connects and when a chat appears.

// pictureEvery is how often a chat's picture is asked for again.
const pictureEvery = 24 * time.Hour

// picturesLater has self's sweeper keep its cached chats' pictures; nothing while the
// helper is down.
func (a *Adapter) picturesLater(self string) {
	if a.cache == nil {
		return
	}
	a.mu.Lock()
	h, ctx := a.helper, a.run
	a.mu.Unlock()
	if h == nil || ctx == nil {
		return
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		a.log.Warn("read the chats to ask pictures of failed", "err", err)
		return
	}
	wants := map[domain.RoomID]pictures.Want{}
	for i := range rooms {
		chat, group, ok := chatOf(self, rooms[i].ID)
		if !ok {
			continue
		}
		wants[rooms[i].ID] = askPicture(h, self, chat, group)
	}
	a.sweeper(self).Later(ctx, wants)
}

// askPicture is a chat's picture as the helper tells it.
func askPicture(h *helper, self, chat string, group bool) pictures.Want {
	return pictures.Want{Every: pictureEvery, Ask: func(ctx context.Context, kept string) (string, []byte, error) {
		req := &signalv1.PictureRequest{Account: self, Kept: kept}
		if group {
			req.Group = chat
		} else {
			req.Person = chat
		}
		resp, err := h.client.Picture(ctx, connect.NewRequest(req))
		if err != nil {
			return "", nil, fmt.Errorf("signal: ask for a chat's picture: %w", err)
		}
		return resp.Msg.GetVersion(), resp.Msg.GetData(), nil
	}}
}

// sweeper is self's picture sweeper, made on first use.
func (a *Adapter) sweeper(self string) *pictures.Sweeper[domain.RoomID] {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sweepers[self]
	if s == nil {
		s = &pictures.Sweeper[domain.RoomID]{
			Store:   a.cache,
			Changed: a.roomsChanged,
			Failed:  func(err error) { a.log.Debug("ask for pictures stopped", "account", self, "err", err) },
		}
		a.sweepers[self] = s
	}
	return s
}
