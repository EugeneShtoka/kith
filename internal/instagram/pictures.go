package instagram

import (
	"context"
	"fmt"
	"net/url"
	"path"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// A thread's picture is its person's in a one-to-one thread, the thread's own in a
// group, kept by the connection's sweeper (internal/pictures). Instagram's picture links expire, so a picture is downloaded
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

// picturesLater has c's sweeper keep the pictures its inbox names, off the listing.
func (a *Adapter) picturesLater(c *conn) {
	if a.cache == nil {
		return
	}
	links := c.inbox.pictureLinks(c.self)
	wants := make(map[domain.RoomID]pictures.Want, len(links))
	for room, link := range links {
		wants[room] = pictures.Want{Version: pictureVersion(link), Fetch: func(ctx context.Context) ([]byte, error) {
			data, err := download(ctx, mediaSource{URL: link, Mime: "image/jpeg"})
			if err != nil {
				return nil, fmt.Errorf("instagram: download the picture of %s: %w", room, err)
			}
			return data, nil
		}}
	}
	a.mu.Lock()
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.sweeper == nil {
		c.sweeper = &pictures.Sweeper{
			Store:   a.cache,
			Changed: a.roomsChanged,
			Failed:  func(err error) { a.log.Debug("download pictures stopped", "account", c.account.Name, "err", err) },
		}
	}
	s := c.sweeper
	c.mu.Unlock()
	s.Later(ctx, wants)
}
