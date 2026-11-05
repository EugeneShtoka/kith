package linkedin

import (
	"context"
	"fmt"
	"slices"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// A one-to-one conversation's picture is its person's (a member's photo, an
// organization's logo); a group has none. The connection's sweeper (internal/pictures)
// keeps them. Every listing carries the
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

// pictureWants is what the sweeper is to keep of the conversations' pictures,
// downloaded with client.
func (a *Adapter) pictureWants(client *linkedingo.Client, pics map[domain.RoomID]picture) map[domain.RoomID]pictures.Want {
	wants := make(map[domain.RoomID]pictures.Want, len(pics))
	for room, p := range pics {
		wants[room] = pictures.Want{Version: p.root, Fetch: func(ctx context.Context) (data []byte, err error) {
			err = a.ask(ctx, "downloading a picture", func(ctx context.Context) (err error) {
				data, err = client.DownloadBytes(ctx, p.link)
				return err //nolint:wrapcheck // wrapped below
			})
			if err != nil {
				return nil, fmt.Errorf("linkedin: download the picture of %s: %w", room, err)
			}
			return data, nil
		}}
	}
	return wants
}

// picturesLater has c's sweeper keep the pictures its inbox names, off the listing;
// none before the connection has a client.
func (a *Adapter) picturesLater(c *conn) {
	pics := c.inbox.conversationPictures(c.profile)
	a.mu.Lock()
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	client := c.client
	if client == nil {
		c.mu.Unlock()
		return
	}
	if c.sweeper == nil {
		c.sweeper = &pictures.Sweeper[domain.RoomID]{
			Store:   a.cache,
			Changed: a.roomsChanged,
			Failed:  func(err error) { a.log.Debug("download pictures stopped", "account", c.account.Name, "err", err) },
		}
	}
	s := c.sweeper
	c.mu.Unlock()
	s.Later(ctx, a.pictureWants(client, pics))
}
