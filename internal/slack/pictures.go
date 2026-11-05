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
// picture changed. Channels and group DMs have none. Each person a message names has
// theirs kept the same way, as theirs.

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

// peoplePictures is the picture of each user w has seen, by their person ID; w.mu is held.
func (w *workspace) peoplePictures() map[string]picture {
	out := map[string]picture{}
	for u := range w.seen {
		if p, ok := w.pictures[u]; ok {
			out[personID(w.creds.Team, u)] = p
		}
	}
	return out
}

// pictureWants is what a sweeper is to keep of pictures, a DM's or a person's.
func pictureWants[K ~string](pics map[K]picture) map[K]pictures.Want {
	wants := make(map[K]pictures.Want, len(pics))
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

// picturesLater has w's sweepers keep the pictures a listing named, off the listing:
// its DMs', and those of the people seen.
func (a *Adapter) picturesLater(w *workspace, pics map[domain.RoomID]picture) {
	if a.cache == nil {
		return
	}
	w.mu.Lock()
	a.sweepers(w)
	rooms, faces, seen := w.sweeper, w.faces, w.peoplePictures()
	w.mu.Unlock()
	rooms.Later(a.runCtx(), pictureWants(pics))
	faces.Later(a.runCtx(), pictureWants(seen))
}

// seePeople has w's faces sweeper keep the pictures of users, named by messages,
// once any of them is new to w this run.
func (a *Adapter) seePeople(w *workspace, users []string) {
	if a.cache == nil {
		return
	}
	w.mu.Lock()
	news := false
	for _, u := range users {
		if !w.seen[u] {
			if w.seen == nil {
				w.seen = map[string]bool{}
			}
			w.seen[u], news = true, true
		}
	}
	if !news {
		w.mu.Unlock()
		return
	}
	a.sweepers(w)
	faces, seen := w.faces, w.peoplePictures()
	w.mu.Unlock()
	faces.Later(a.runCtx(), pictureWants(seen))
}

// sweepers makes w's picture sweepers on first use; w.mu is held.
func (a *Adapter) sweepers(w *workspace) {
	if w.sweeper != nil {
		return
	}
	changed := func() {
		if a.onRoomsChanged != nil {
			a.onRoomsChanged()
		}
	}
	failed := func(err error) { a.log.Debug("download pictures stopped", "account", w.account.Name, "err", err) }
	w.sweeper = &pictures.Sweeper[domain.RoomID]{Store: a.cache, Changed: changed, Failed: failed}
	w.faces = &pictures.Sweeper[string]{Store: a.cache.PersonPictures(), Changed: changed, Failed: failed}
}

// runCtx is the context the adapter's background work runs under.
func (a *Adapter) runCtx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run == nil {
		return context.Background()
	}
	return a.run
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
