package slack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// A DM's picture is its person's, kept by the hash of it their profile names: after each
// listing, the workspace's sweeper (internal/pictures) downloads the DMs' whose person's
// picture changed. Channels and group DMs have none.

// pictureBytes bounds a picture's download; Slack's 72 px avatars are a few KB.
const pictureBytes = 1 << 20

// picture is a user's picture as their profile names it: Slack's hash of it and where
// it is; zero for Slack's generated default, which the initials stand in for.
type picture struct{ hash, url string }

func pictureOf(u *slackgo.User) picture {
	p := u.Profile
	if !p.IsCustomImage || p.Image72 == "" {
		return picture{}
	}
	return picture{hash: cmpOr(p.AvatarHash, p.Image72), url: p.Image72}
}

// dmPictures is each listed DM's person's picture, of the people w knows.
func (w *workspace) dmPictures(conversations []slackgo.Channel) map[domain.RoomID]picture {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[domain.RoomID]picture{}
	for i := range conversations {
		c := &conversations[i]
		if p, ok := w.pictures[c.User]; ok && c.IsIM && !c.IsUserDeleted {
			out[roomID(w.creds.Team, c.ID)] = p
		}
	}
	return out
}

// pictureWants is what the sweeper is to keep of the DMs' pictures.
func pictureWants(pics map[domain.RoomID]picture) map[domain.RoomID]pictures.Want {
	wants := make(map[domain.RoomID]pictures.Want, len(pics))
	for room, p := range pics {
		wants[room] = pictures.Want{Version: p.hash, Fetch: func(ctx context.Context) ([]byte, error) {
			data, err := downloadPicture(ctx, p.url)
			if err != nil {
				return nil, fmt.Errorf("slack: download the picture of %s: %w", room, err)
			}
			return data, nil
		}}
	}
	return wants
}

// picturesLater has w's sweeper keep the pictures a listing named, off the listing.
func (a *Adapter) picturesLater(w *workspace, pics map[domain.RoomID]picture) {
	if a.cache == nil {
		return
	}
	a.mu.Lock()
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	if w.sweeper == nil {
		w.sweeper = &pictures.Sweeper{
			Store: a.cache,
			Changed: func() {
				if a.onRoomsChanged != nil {
					a.onRoomsChanged()
				}
			},
			Failed: func(err error) { a.log.Debug("download pictures stopped", "account", w.account.Name, "err", err) },
		}
	}
	s := w.sweeper
	w.mu.Unlock()
	s.Later(ctx, pictureWants(pics))
}

// downloadPicture fetches an avatar from Slack's CDN, which serves it to anyone.
func downloadPicture(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("a picture's URL: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, pictureBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if len(data) > pictureBytes {
		return nil, fmt.Errorf("a picture of over %d bytes", pictureBytes)
	}
	return data, nil
}
