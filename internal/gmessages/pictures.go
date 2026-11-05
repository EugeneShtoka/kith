package gmessages

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// The connection's sweeper (internal/pictures) keeps the pictures. A group's is the one
// the phone names by its address, kept by that address. A one-to-one conversation's is its person's thumbnail, which the
// phone hands over as bytes with nothing to know it by: it is kept by their hash and
// asked for again at most every thumbEvery, of people the phone has as contacts (and
// Google's own senders), as the bridge does. Each such person, in any conversation,
// has theirs kept as theirs too, beside their messages.

// thumbEvery is how often a person's thumbnail is asked for again.
const thumbEvery = 24 * time.Hour

// pictureWant is a conversation's picture as the inbox knows it: a group's address, or
// the participant whose thumbnail a one-to-one conversation shows ("" for none).
type pictureWant struct {
	group       bool
	url, person string
}

// mayHaveThumb reports whether the phone keeps a thumbnail for p: a contact, or one of
// Google's own senders (a .goog number).
func mayHaveThumb(p *gmproto.Participant) bool {
	return p.GetContactID() != "" || strings.HasSuffix(p.GetID().GetNumber(), ".goog")
}

// pictureWants is each conversation's picture.
func (in *inbox) pictureWants() map[domain.RoomID]pictureWant {
	out := make(map[domain.RoomID]pictureWant, len(in.convs))
	for id, cv := range in.convs {
		w := pictureWant{group: cv.group, url: cv.avatar}
		if !cv.group {
			for _, pid := range cv.order {
				if !in.me[cv.members[pid]] {
					if in.thumbs[pid] {
						w.person = pid
					}
					break
				}
			}
		}
		out[roomID(in.self, id)] = w
	}
	return out
}

// personThumbs is the participant whose thumbnail each person with one has, by their
// person ID.
func (in *inbox) personThumbs() map[string]string {
	out := map[string]string{}
	for _, cv := range in.convs {
		for pid, person := range cv.members {
			if in.thumbs[pid] && person != "" {
				out[person] = pid
			}
		}
	}
	return out
}

// thumbVersion is the version a thumbnail is kept by, "" for none.
func thumbVersion(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return fmt.Sprintf("hash:%x", sha256.Sum256(data))
}

// pictureWants is what the sweeper is to keep of the conversations' pictures, fetched
// with client.
func (a *Adapter) pictureWants(client *libgm.Client, wants map[domain.RoomID]pictureWant) map[domain.RoomID]pictures.Want {
	out := make(map[domain.RoomID]pictures.Want, len(wants))
	for room, w := range wants {
		switch {
		case w.person != "":
			out[room] = a.thumbWant(client, string(room), w.person)
		case w.group && w.url != "":
			out[room] = pictures.Want{Version: w.url, Fetch: func(ctx context.Context) (data []byte, err error) {
				if err := a.guard("downloading a group's picture", func() (err error) {
					data, err = client.DownloadAvatar(ctx, w.url)
					return err //nolint:wrapcheck // wrapped below
				}); err != nil {
					return nil, fmt.Errorf("gmessages: download the picture of %s: %w", room, err)
				}
				return data, nil
			}}
		default:
			out[room] = pictures.Want{}
		}
	}
	return out
}

// thumbWant is participant's thumbnail as asking the phone tells it, for whom, at most
// every thumbEvery.
func (a *Adapter) thumbWant(client *libgm.Client, whom, participant string) pictures.Want {
	return pictures.Want{Every: thumbEvery, Ask: func(ctx context.Context, _ string) (string, []byte, error) {
		var got *gmproto.GetThumbnailResponse
		if err := a.guard("asking for a thumbnail", func() (err error) {
			got, err = client.GetParticipantThumbnail(ctx, participant)
			return err //nolint:wrapcheck // wrapped below
		}); err != nil {
			return "", nil, fmt.Errorf("gmessages: ask for the thumbnail of %s: %w", whom, err)
		}
		for _, t := range got.GetThumbnail() {
			if data := t.GetData().GetImageBuffer(); t.GetIdentifier() == participant {
				return thumbVersion(data), data, nil
			}
		}
		return "", nil, nil
	}}
}

// picturesLater has c's sweepers keep its inbox's pictures, off the batch being stored:
// its conversations' and its people's; none before the connection has a client.
func (a *Adapter) picturesLater(c *conn) {
	wants, thumbs := c.inbox.pictureWants(), c.inbox.personThumbs()
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
		failed := func(err error) { a.log.Debug("fetch pictures stopped", "account", c.account.Name, "err", err) }
		c.sweeper = &pictures.Sweeper[domain.RoomID]{Store: a.cache, Changed: a.roomsChanged, Failed: failed}
		c.people = &pictures.Sweeper[string]{Store: a.cache.PersonPictures(), Changed: a.roomsChanged, Failed: failed}
	}
	rooms, people := c.sweeper, c.people
	c.mu.Unlock()
	rooms.Later(ctx, a.pictureWants(client, wants))
	people.Later(ctx, a.peopleWants(client, thumbs))
}

// peopleWants is what the people's sweeper is to keep of thumbs, by person.
func (a *Adapter) peopleWants(client *libgm.Client, thumbs map[string]string) map[string]pictures.Want {
	out := make(map[string]pictures.Want, len(thumbs))
	for person, pid := range thumbs {
		out[person] = a.thumbWant(client, person, pid)
	}
	return out
}
