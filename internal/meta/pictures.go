package meta

import (
	"context"
	"fmt"
	"net/url"
	"path"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// A thread's picture is its person's in a one-to-one thread, the thread's own in a
// group, kept by the connection's sweeper (internal/pictures). Meta's picture links expire, so a picture is downloaded as
// soon as a table names one the cache does not hold; it is kept by its file's name,
// which stays while the picture does.

// pictureVersion is the version a picture link names: its file's name; "" for none.
func pictureVersion(link string) string {
	u, err := url.Parse(link)
	if link == "" || err != nil || path.Base(u.Path) == "/" || path.Base(u.Path) == "." {
		return ""
	}
	return path.Base(u.Path)
}

func (in *inbox) seePerson(id int64, link string, c *change) {
	if id != 0 && in.pictures[id] != link {
		in.pictures[id] = link
		c.pictures = true
	}
}

func (in *inbox) seeThread(metaKey int64, link string, c *change) {
	if key := in.key(metaKey); metaKey != 0 && in.threadPictures[key] != link {
		in.threadPictures[key] = link
		c.pictures = true
	}
}

// applyPictures takes the pictures a table names, of people and of threads.
func (in *inbox) applyPictures(tbl *table.LSTable, c *change) {
	for _, p := range tbl.LSDeleteThenInsertContact {
		in.seePerson(p.Id, p.GetAvatarURL(), c)
	}
	for _, p := range tbl.LSVerifyContactRowExists {
		if p.ProfilePictureUrl != "" { // a short row: no link is not no picture
			in.seePerson(p.ContactId, p.ProfilePictureUrl, c)
		}
	}
	for _, r := range tbl.LSDeleteThenInsertThread {
		in.seeThread(r.ThreadKey, r.ThreadPictureUrl, c)
	}
	for _, r := range tbl.LSUpdateOrInsertThread {
		if r.ThreadPictureUrl != "" { // as a short contact row
			in.seeThread(r.ThreadKey, r.ThreadPictureUrl, c)
		}
	}
	for _, r := range tbl.LSSetThreadImageURL {
		in.seeThread(r.ThreadKey, r.ImageURL, c)
	}
}

// pictureLinks is each thread's picture link, "" for one with none.
func (in *inbox) pictureLinks() map[domain.RoomID]string {
	out := make(map[domain.RoomID]string, len(in.threads))
	for key, t := range in.threads {
		link := in.threadPictures[key]
		if t.direct() {
			link = in.pictures[key]
		}
		out[roomID(in.self, key)] = link
	}
	return out
}

// keepPictures sweeps the inbox's pictures when a table changed its threads or links.
func (a *Adapter) keepPictures(c *conn, ch change, roomsChanged bool) {
	if roomsChanged || ch.pictures {
		a.picturesLater(c, c.inbox.pictureLinks())
	}
}

// picturesLater has c's sweeper keep the pictures its inbox names, off the table being
// applied.
func (a *Adapter) picturesLater(c *conn, links map[domain.RoomID]string) {
	wants := make(map[domain.RoomID]pictures.Want, len(links))
	for room, link := range links {
		wants[room] = pictures.Want{Version: pictureVersion(link), Fetch: func(ctx context.Context) ([]byte, error) {
			data, err := download(ctx, link, "image/jpeg")
			if err != nil {
				return nil, fmt.Errorf("meta: download the picture of %s: %w", room, err)
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
