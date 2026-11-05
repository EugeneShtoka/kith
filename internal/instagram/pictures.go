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
// group, kept by the connection's sweeper (internal/pictures); each person's is kept
// as theirs too, beside their messages. Instagram's picture links expire, so a picture is downloaded
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

// personLinks is the picture link of each person in the inbox's threads, by their
// person ID: a thread that names one wins over one that names none.
func (in *inbox) personLinks() map[string]string {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := map[string]string{}
	for _, th := range in.threads {
		for _, u := range th.users {
			id := person(fbid(u.InteropMessagingUserFBID))
			if _, seen := out[id]; !seen || u.ProfilePicURL != "" {
				out[id] = u.ProfilePicURL
			}
		}
	}
	return out
}

// picturesLater has c's sweepers keep the pictures its inbox names, off the listing:
// its threads', and their people's.
func (a *Adapter) picturesLater(c *conn) {
	if a.cache == nil {
		return
	}
	a.mu.Lock()
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.sweeper == nil {
		failed := func(err error) { a.log.Debug("download pictures stopped", "account", c.account.Name, "err", err) }
		c.sweeper = &pictures.Sweeper[domain.RoomID]{Store: a.cache, Changed: a.roomsChanged, Failed: failed}
		c.people = &pictures.Sweeper[string]{Store: a.cache.PersonPictures(), Changed: a.roomsChanged, Failed: failed}
	}
	rooms, people := c.sweeper, c.people
	c.mu.Unlock()
	rooms.Later(ctx, linkWants(c.inbox.pictureLinks(c.self)))
	people.Later(ctx, linkWants(c.inbox.personLinks()))
}

// linkWants are pictures by their links, a thread's or a person's, as a sweep keeps them.
func linkWants[K ~string](links map[K]string) map[K]pictures.Want {
	wants := make(map[K]pictures.Want, len(links))
	for key, link := range links {
		wants[key] = pictures.Want{Version: pictureVersion(link), Fetch: func(ctx context.Context) ([]byte, error) {
			data, err := download(ctx, mediaSource{URL: link, Mime: "image/jpeg"})
			if err != nil {
				return nil, fmt.Errorf("instagram: download the picture of %s: %w", key, err)
			}
			return data, nil
		}}
	}
	return wants
}
