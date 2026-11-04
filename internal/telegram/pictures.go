package telegram

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Pictures are kept in the cache (db.SetPicture) by Telegram's photo ID, which every
// listing names: after each listing, the chats whose photo changed are downloaded.

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

// pictureSweep is the pictures a listing named, and the client to download them by.
type pictureSweep struct {
	client *telegram.Client
	photos map[domain.RoomID]photo
}

// picturesLater keeps the pictures a listing of self's chats named, off the listing:
// one sweep per account at a time, a listing made meanwhile swept after it.
func (a *Adapter) picturesLater(self int64, client *telegram.Client, photos map[domain.RoomID]photo) {
	if a.cache == nil {
		return
	}
	a.mu.Lock()
	a.pictureWants[self] = pictureSweep{client: client, photos: photos}
	if a.sweeping[self] {
		a.mu.Unlock()
		return
	}
	a.sweeping[self] = true
	a.mu.Unlock()
	go func() {
		ctx := a.runContext()
		for {
			a.mu.Lock()
			want, ok := a.pictureWants[self]
			delete(a.pictureWants, self)
			if !ok {
				delete(a.sweeping, self)
				a.mu.Unlock()
				return
			}
			a.mu.Unlock()
			if err := a.sweepPictures(ctx, want); err != nil {
				a.log.Debug("download pictures stopped", "user", self, "err", err)
			}
		}
	}()
}

// sweepPictures keeps each picture the cache holds another version of. It stops at the
// first download that fails but for one Telegram refuses for that photo alone: the rest
// are tried again with the next listing.
func (a *Adapter) sweepPictures(ctx context.Context, sweep pictureSweep) error {
	checks, err := a.cache.PictureChecks(ctx)
	if err != nil {
		return fmt.Errorf("telegram: read the pictures kept: %w", err)
	}
	changed := false
	defer func() {
		if changed && a.onRoomsChanged != nil {
			a.onRoomsChanged()
		}
	}()
	rooms := make([]domain.RoomID, 0, len(sweep.photos))
	for room := range sweep.photos {
		rooms = append(rooms, room)
	}
	slices.Sort(rooms)
	for _, room := range rooms {
		p := sweep.photos[room]
		if check, ok := checks[room]; ok && check.ID == p.version() {
			continue
		}
		var data []byte
		if p.id != 0 {
			var out bytes.Buffer
			loc := &tg.InputPeerPhotoFileLocation{Peer: p.peer, PhotoID: p.id}
			if _, err := downloader.NewDownloader().Download(sweep.client.API(), loc).Stream(ctx, &out); err != nil {
				if rpc, ok := tgerr.As(err); ok && rpc.Code == 400 {
					// shortcut: a photo Telegram refuses is asked for again each listing; keep the refusal if that is ever many.
					a.log.Debug("a chat's picture was refused", "room", room, "err", err)
					continue
				}
				return fmt.Errorf("telegram: download the picture of %s: %w", room, err)
			}
			data = out.Bytes()
		}
		if err := a.cache.SetPicture(ctx, room, p.version(), data, time.Now()); err != nil {
			return fmt.Errorf("telegram: keep the picture of %s: %w", room, err)
		}
		changed = true
	}
	return nil
}
