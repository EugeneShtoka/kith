package instagram

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A thread's picture is its person's in a one-to-one thread, the thread's own in a
// group (db.SetPicture). Instagram's picture links expire, so a picture is downloaded
// as soon as a listing names one the cache does not hold; it is kept by its file's
// name, which stays while the picture does.

// pictureVersion is the version a picture link names: its file's name; "" for none.
func pictureVersion(link string) string {
	u, err := url.Parse(link)
	if link == "" || err != nil || path.Base(u.Path) == "/" || path.Base(u.Path) == "." {
		return ""
	}
	return path.Base(u.Path)
}

// pictureLinks is each thread's picture link, "" for one with none.
func (in *inbox) pictureLinks(self string) map[domain.RoomID]string {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make(map[domain.RoomID]string, len(in.threads))
	for id, th := range in.threads {
		link := th.image
		if !th.group {
			link = ""
			if len(th.users) > 0 {
				link = th.users[0].ProfilePicURL
			}
		}
		out[roomID(self, id)] = link
	}
	return out
}

// picturesLater keeps the pictures c's inbox names, off the listing: one sweep per
// connection at a time, the links named meanwhile swept after it.
func (a *Adapter) picturesLater(c *conn) {
	if a.cache == nil {
		return
	}
	links := c.inbox.pictureLinks(c.self)
	c.mu.Lock()
	c.pictureWant = links
	if c.sweeping {
		c.mu.Unlock()
		return
	}
	c.sweeping = true
	c.mu.Unlock()
	a.mu.Lock()
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		for {
			c.mu.Lock()
			want := c.pictureWant
			c.pictureWant = nil
			if want == nil {
				c.sweeping = false
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			if err := a.sweepPictures(ctx, want); err != nil {
				a.log.Debug("download pictures stopped", "account", c.account.Name, "err", err)
			}
		}
	}()
}

// sweepPictures keeps each picture the cache holds another version of, stopping at the
// first download that fails: the rest are tried again with the next listing.
func (a *Adapter) sweepPictures(ctx context.Context, links map[domain.RoomID]string) error {
	checks, err := a.cache.PictureChecks(ctx)
	if err != nil {
		return fmt.Errorf("instagram: read the pictures kept: %w", err)
	}
	changed := false
	defer func() {
		if changed {
			a.roomsChanged()
		}
	}()
	rooms := make([]domain.RoomID, 0, len(links))
	for room := range links {
		rooms = append(rooms, room)
	}
	slices.Sort(rooms)
	for _, room := range rooms {
		version := pictureVersion(links[room])
		if check, ok := checks[room]; ok && check.ID == version {
			continue
		}
		var data []byte
		if version != "" {
			if data, err = download(ctx, mediaSource{URL: links[room], Mime: "image/jpeg"}); err != nil {
				return fmt.Errorf("instagram: download the picture of %s: %w", room, err)
			}
		}
		if err := a.cache.SetPicture(ctx, room, version, data, time.Now()); err != nil {
			return fmt.Errorf("instagram: keep the picture of %s: %w", room, err)
		}
		changed = true
	}
	return nil
}
