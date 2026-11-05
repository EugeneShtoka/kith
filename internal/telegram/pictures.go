package telegram

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
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
// whose photo changed. People's are too, named by the users that come with messages.

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
	a.mu.Lock()
	s := a.sweeper(self)
	a.mu.Unlock()
	s.rooms.Later(a.runContext(), photoWants(client, photos))
}

// seePeople has self's sweeper keep the pictures of the users an answer named, with
// every one named before in this run: a sweep downloads only photos not kept.
func (a *Adapter) seePeople(self int64, users map[int64]*tg.User) {
	if a.cache == nil {
		return
	}
	i := slices.IndexFunc(a.connected(), func(c conn) bool { return c.user == self })
	if i < 0 {
		return
	}
	client := a.connected()[i].client
	a.mu.Lock()
	s := a.sweeper(self)
	news := false
	for id, u := range users {
		if u.Min {
			continue // shortcut: a min user's photo is reached only through its message; they show initials until a full user names them
		}
		p := photo{peer: &tg.InputPeerUser{UserID: id, AccessHash: u.AccessHash}, id: userPhoto(u)}
		if old, ok := s.seen[personID(id)]; !ok || old.id != p.id {
			s.seen[personID(id)], news = p, true
		}
	}
	if !news {
		a.mu.Unlock()
		return
	}
	seen := maps.Clone(s.seen)
	a.mu.Unlock()
	s.people.Later(a.runContext(), photoWants(client, seen))
}

// photoWants are photos as a sweep downloads them: those Telegram refuses on their own
// are asked for again with the next sweep.
func photoWants[K ~string](client *telegram.Client, photos map[K]photo) map[K]pictures.Want {
	wants := make(map[K]pictures.Want, len(photos))
	for key, p := range photos {
		wants[key] = pictures.Want{Version: p.version(), Fetch: func(ctx context.Context) ([]byte, error) {
			var out bytes.Buffer
			loc := &tg.InputPeerPhotoFileLocation{Peer: p.peer, PhotoID: p.id}
			if _, err := downloader.NewDownloader().Download(client.API(), loc).Stream(ctx, &out); err != nil {
				if rpc, ok := tgerr.As(err); ok && rpc.Code == 400 {
					// shortcut: a photo Telegram refuses is asked for again each listing; keep the refusal if that is ever many.
					return nil, fmt.Errorf("telegram: the picture of %s: %w: %w", key, pictures.ErrRefused, err)
				}
				return nil, fmt.Errorf("telegram: download the picture of %s: %w", key, err)
			}
			return out.Bytes(), nil
		}}
	}
	return wants
}

// sweepers are an account's picture sweepers, its chats' and its people's, and every
// person an answer named this run.
type sweepers struct {
	rooms  pictures.Sweeper[domain.RoomID]
	people pictures.Sweeper[string]
	seen   map[string]photo
}

// sweeper is self's picture sweepers, made on first use; a.mu is held.
func (a *Adapter) sweeper(self int64) *sweepers {
	s := a.sweepers[self]
	if s == nil {
		changed := func() {
			if a.onRoomsChanged != nil {
				a.onRoomsChanged()
			}
		}
		failed := func(err error) { a.log.Debug("download pictures stopped", "user", self, "err", err) }
		s = &sweepers{
			rooms:  pictures.Sweeper[domain.RoomID]{Store: a.cache, Changed: changed, Failed: failed},
			people: pictures.Sweeper[string]{Store: a.cache.PersonPictures(), Changed: changed, Failed: failed},
			seen:   map[string]photo{},
		}
		a.sweepers[self] = s
	}
	return s
}
