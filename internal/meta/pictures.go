package meta

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"slices"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A thread's picture is its person's in a one-to-one thread, the thread's own in a
// group (db.SetPicture). Meta's picture links expire, so a picture is downloaded as
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

// picturesLater keeps the pictures c's inbox names, off the table being applied: one
// sweep per connection at a time, the links named meanwhile swept after it.
func (a *Adapter) picturesLater(c *conn, links map[domain.RoomID]string) {
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
// first download that fails: the rest are tried again when a table next names them.
func (a *Adapter) sweepPictures(ctx context.Context, links map[domain.RoomID]string) error {
	checks, err := a.cache.PictureChecks(ctx)
	if err != nil {
		return fmt.Errorf("meta: read the pictures kept: %w", err)
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
			if data, err = download(ctx, links[room], "image/jpeg"); err != nil {
				return fmt.Errorf("meta: download the picture of %s: %w", room, err)
			}
		}
		if err := a.cache.SetPicture(ctx, room, version, data, time.Now()); err != nil {
			return fmt.Errorf("meta: keep the picture of %s: %w", room, err)
		}
		changed = true
	}
	return nil
}
