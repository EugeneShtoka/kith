package slack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A DM's picture is its person's (db.SetPicture), kept by the hash of it their profile
// names: after each listing, the DMs whose person's picture changed are downloaded.
// Channels and group DMs have none.

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

// picturesLater keeps the pictures a listing of w named, off the listing: one sweep per
// workspace at a time, a listing made meanwhile swept after it.
func (a *Adapter) picturesLater(w *workspace, pictures map[domain.RoomID]picture) {
	if a.cache == nil {
		return
	}
	w.mu.Lock()
	w.pictureWant = pictures
	if w.sweeping {
		w.mu.Unlock()
		return
	}
	w.sweeping = true
	w.mu.Unlock()
	a.mu.Lock()
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		for {
			w.mu.Lock()
			want := w.pictureWant
			w.pictureWant = nil
			if want == nil {
				w.sweeping = false
				w.mu.Unlock()
				return
			}
			w.mu.Unlock()
			if err := a.sweepPictures(ctx, want); err != nil {
				a.log.Debug("download pictures stopped", "account", w.account.Name, "err", err)
			}
		}
	}()
}

// sweepPictures keeps each picture the cache holds another version of, stopping at the
// first download that fails: the rest are tried again with the next listing.
func (a *Adapter) sweepPictures(ctx context.Context, pictures map[domain.RoomID]picture) error {
	checks, err := a.cache.PictureChecks(ctx)
	if err != nil {
		return fmt.Errorf("slack: read the pictures kept: %w", err)
	}
	changed := false
	defer func() {
		if changed && a.onRoomsChanged != nil {
			a.onRoomsChanged()
		}
	}()
	rooms := make([]domain.RoomID, 0, len(pictures))
	for room := range pictures {
		rooms = append(rooms, room)
	}
	slices.Sort(rooms)
	for _, room := range rooms {
		p := pictures[room]
		if check, ok := checks[room]; ok && check.ID == p.hash {
			continue
		}
		var data []byte
		if p.url != "" {
			if data, err = downloadPicture(ctx, p.url); err != nil {
				return fmt.Errorf("slack: download the picture of %s: %w", room, err)
			}
		}
		if err := a.cache.SetPicture(ctx, room, p.hash, data, time.Now()); err != nil {
			return fmt.Errorf("slack: keep the picture of %s: %w", room, err)
		}
		changed = true
	}
	return nil
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
