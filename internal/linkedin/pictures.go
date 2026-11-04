package linkedin

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A one-to-one conversation's picture is its person's (a member's photo, an
// organization's logo); a group has none (db.SetPicture). Every listing carries the
// participants, so each one that changes the rooms sweeps them: a picture the cache
// holds another version of is downloaded with the account's session, as LinkedIn's
// links expire. It is kept by its root address, which stays while the picture does.

// pictureWidth is the artifact asked for: the smallest at least this wide.
const pictureWidth = 100

// picture is a participant's picture: its root address (the version) and an
// artifact's link; zero for none.
type picture struct{ root, link string }

func pictureOf(p linkedingo.MessagingParticipant) picture {
	var img *linkedingo.VectorImage
	switch t := p.ParticipantType; {
	case t.Member != nil:
		img = t.Member.ProfilePicture
	case t.Organization != nil:
		img = t.Organization.Logo
	}
	if img == nil || img.RootURL == "" || len(img.Artifacts) == 0 {
		return picture{}
	}
	best := slices.MaxFunc(img.Artifacts, func(a, b linkedingo.VectorArtifact) int { return a.Width - b.Width })
	for _, art := range img.Artifacts {
		if art.Width >= pictureWidth && art.Width < best.Width {
			best = art
		}
	}
	return picture{root: img.RootURL, link: img.RootURL + best.FileIdentifyingURLPathSegment}
}

// seePicture keeps a participant's picture. Caller is in take.
func (in *inbox) seePicture(p linkedingo.MessagingParticipant) {
	if id := profileOf(p.EntityURN); id != "" {
		in.pictures[id] = pictureOf(p)
	}
}

// conversationPictures is each shown one-to-one conversation's picture.
func (in *inbox) conversationPictures(profile string) map[domain.RoomID]picture {
	out := map[domain.RoomID]picture{}
	for thread, cv := range in.convs {
		if p, ok := in.pictures[firstOf(cv.others)]; ok && !cv.group {
			out[roomID(profile, thread)] = p
		}
	}
	return out
}

func firstOf(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// picturesLater keeps the pictures c's inbox names, off the listing: one sweep per
// connection at a time, the pictures named meanwhile swept after it.
func (a *Adapter) picturesLater(c *conn) {
	pictures := c.inbox.conversationPictures(c.profile)
	c.mu.Lock()
	c.pictureWant = pictures
	client := c.client
	if c.sweeping || client == nil {
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
			if err := a.sweepPictures(ctx, client, want); err != nil {
				a.log.Debug("download pictures stopped", "account", c.account.Name, "err", err)
			}
		}
	}()
}

// sweepPictures keeps each picture the cache holds another version of, stopping at the
// first download that fails: the rest are tried again with the next listing.
func (a *Adapter) sweepPictures(ctx context.Context, client *linkedingo.Client, pictures map[domain.RoomID]picture) error {
	checks, err := a.cache.PictureChecks(ctx)
	if err != nil {
		return fmt.Errorf("linkedin: read the pictures kept: %w", err)
	}
	changed := false
	defer func() {
		if changed {
			a.roomsChanged()
		}
	}()
	rooms := make([]domain.RoomID, 0, len(pictures))
	for room := range pictures {
		rooms = append(rooms, room)
	}
	slices.Sort(rooms)
	for _, room := range rooms {
		p := pictures[room]
		if check, ok := checks[room]; ok && check.ID == p.root {
			continue
		}
		var data []byte
		if p.root != "" {
			err := a.ask(ctx, "downloading a picture", func(ctx context.Context) (err error) {
				data, err = client.DownloadBytes(ctx, p.link)
				return err //nolint:wrapcheck // wrapped below
			})
			if err != nil {
				return fmt.Errorf("linkedin: download the picture of %s: %w", room, err)
			}
		}
		if err := a.cache.SetPicture(ctx, room, p.root, data, time.Now()); err != nil {
			return fmt.Errorf("linkedin: keep the picture of %s: %w", room, err)
		}
		changed = true
	}
	return nil
}
