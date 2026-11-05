package telegram

import (
	"bytes"
	"context"
	"fmt"
	"strconv"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// Pictures are kept in the cache by Telegram's photo ID, which every listing names:
// after each listing, the account's sweeper (internal/pictures) downloads the chats'
// whose photo changed.

// photo is a chat's picture as a listing names it: its peer, to download it by, and
// Telegram's ID for it, 0 for none.
type photo struct {
	peer tg.InputPeerClass
	id   int64
}

// version is the photo's ID as the cache keeps it, "" for none.
func (p photo) version() string {
	if p.id == 0 {
		return ""
	}
	return strconv.FormatInt(p.id, 10)
}

func userPhoto(u *tg.User) int64 {
	if p, ok := u.Photo.(*tg.UserProfilePhoto); ok {
		return p.PhotoID
	}
	return 0
}

func chatPhoto(c tg.ChatPhotoClass) int64 {
	if p, ok := c.(*tg.ChatPhoto); ok {
		return p.PhotoID
	}
	return 0
}

// picturesLater has self's sweeper keep the pictures a listing of its chats named, off
// the listing. A photo Telegram refuses on its own is asked for again with the next
// listing.
func (a *Adapter) picturesLater(self int64, client *telegram.Client, photos map[domain.RoomID]photo) {
	if a.cache == nil {
		return
	}
	wants := make(map[domain.RoomID]pictures.Want, len(photos))
	for room, p := range photos {
		wants[room] = pictures.Want{Version: p.version(), Fetch: func(ctx context.Context) ([]byte, error) {
			var out bytes.Buffer
			loc := &tg.InputPeerPhotoFileLocation{Peer: p.peer, PhotoID: p.id}
			if _, err := downloader.NewDownloader().Download(client.API(), loc).Stream(ctx, &out); err != nil {
				if rpc, ok := tgerr.As(err); ok && rpc.Code == 400 {
					// shortcut: a photo Telegram refuses is asked for again each listing; keep the refusal if that is ever many.
					return nil, fmt.Errorf("telegram: the picture of %s: %w: %w", room, pictures.ErrRefused, err)
				}
				return nil, fmt.Errorf("telegram: download the picture of %s: %w", room, err)
			}
			return out.Bytes(), nil
		}}
	}
	a.sweeper(self).Later(a.runContext(), wants)
}

// sweeper is self's picture sweeper, made on first use.
func (a *Adapter) sweeper(self int64) *pictures.Sweeper {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sweepers[self]
	if s == nil {
		s = &pictures.Sweeper{
			Store: a.cache,
			Changed: func() {
				if a.onRoomsChanged != nil {
					a.onRoomsChanged()
				}
			},
			Failed: func(err error) { a.log.Debug("download pictures stopped", "user", self, "err", err) },
		}
		a.sweepers[self] = s
	}
	return s
}
