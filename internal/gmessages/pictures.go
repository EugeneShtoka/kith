package gmessages

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A group's picture is the one the phone names by its address, kept by that address
// (db.SetPicture). A one-to-one conversation's is its person's thumbnail, which the
// phone hands over as bytes with nothing to know it by: it is kept by their hash and
// asked for again at most every thumbEvery, of people the phone has as contacts (and
// Google's own senders), as the bridge does.

// thumbEvery is how often a person's thumbnail is asked for again.
const thumbEvery = 24 * time.Hour

// thumbsPerAsk bounds the people one thumbnail request names.
const thumbsPerAsk = 20

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

// thumbVersion is the version a thumbnail is kept by, "" for none.
func thumbVersion(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return fmt.Sprintf("hash:%x", sha256.Sum256(data))
}

// picturesLater keeps c's inbox's pictures, off the batch being stored: one sweep per
// connection at a time, the pictures named meanwhile swept after it.
func (a *Adapter) picturesLater(c *conn) {
	wants := c.inbox.pictureWants()
	c.mu.Lock()
	c.pictureWant = wants
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
			if err := a.sweepPictures(ctx, client, want, time.Now()); err != nil {
				a.log.Debug("fetch pictures stopped", "account", c.account.Name, "err", err)
			}
		}
	}()
}

// sweepPictures keeps the groups' pictures the cache holds another version of, and
// the due people's thumbnails, stopping at the first fetch that fails: the rest are
// tried again with the next batch.
func (a *Adapter) sweepPictures(ctx context.Context, client *libgm.Client, wants map[domain.RoomID]pictureWant, now time.Time) error {
	checks, err := a.cache.PictureChecks(ctx)
	if err != nil {
		return fmt.Errorf("gmessages: read the pictures kept: %w", err)
	}
	changed := false
	defer func() {
		if changed {
			a.roomsChanged()
		}
	}()
	rooms := make([]domain.RoomID, 0, len(wants))
	for room := range wants {
		rooms = append(rooms, room)
	}
	slices.Sort(rooms)
	due := map[string][]domain.RoomID{} // participant → their conversations
	var people []string
	for _, room := range rooms {
		w := wants[room]
		check, asked := checks[room]
		if w.person != "" {
			if !asked || now.Sub(check.Checked) >= thumbEvery {
				if due[w.person] == nil {
					people = append(people, w.person)
				}
				due[w.person] = append(due[w.person], room)
			}
			continue
		}
		got, err := a.keepGroupPicture(ctx, client, room, w, check, asked, now)
		changed = changed || got
		if err != nil {
			return err
		}
	}
	for chunk := range slices.Chunk(people, thumbsPerAsk) {
		got, err := a.keepThumbs(ctx, client, chunk, due, checks, now)
		changed = changed || got
		if err != nil {
			return err
		}
	}
	return nil
}

// keepGroupPicture keeps a group's picture, or a conversation's having none; changed
// is whether what the room shows changed.
func (a *Adapter) keepGroupPicture(ctx context.Context, client *libgm.Client, room domain.RoomID, w pictureWant, check db.PictureCheck, asked bool, now time.Time) (changed bool, err error) {
	switch {
	case w.group && w.url != "":
		if asked && check.ID == w.url {
			return false, nil
		}
		var data []byte
		if err := a.guard("downloading a group's picture", func() (err error) {
			data, err = client.DownloadAvatar(ctx, w.url)
			return err //nolint:wrapcheck // wrapped below
		}); err != nil {
			return false, fmt.Errorf("gmessages: download the picture of %s: %w", room, err)
		}
		return true, a.keepPicture(ctx, room, w.url, data, now)
	case !asked || check.ID != "":
		return asked, a.keepPicture(ctx, room, "", nil, now)
	}
	return false, nil
}

// keepThumbs asks the phone for people's thumbnails and keeps each in the
// conversations it shows in; changed is whether any of them changed.
func (a *Adapter) keepThumbs(ctx context.Context, client *libgm.Client, people []string, due map[string][]domain.RoomID, checks map[domain.RoomID]db.PictureCheck, now time.Time) (changed bool, err error) {
	var got *gmproto.GetThumbnailResponse
	if err := a.guard("asking for thumbnails", func() (err error) {
		got, err = client.GetParticipantThumbnail(ctx, people...)
		return err //nolint:wrapcheck // wrapped below
	}); err != nil {
		return false, fmt.Errorf("gmessages: ask for %d people's thumbnails: %w", len(people), err)
	}
	thumbs := map[string][]byte{}
	for _, t := range got.GetThumbnail() {
		thumbs[t.GetIdentifier()] = t.GetData().GetImageBuffer()
	}
	for _, person := range people {
		version := thumbVersion(thumbs[person])
		for _, room := range due[person] {
			if check, asked := checks[room]; asked && check.ID == version {
				if err := a.cache.PictureUnchanged(ctx, room, now); err != nil {
					return changed, fmt.Errorf("gmessages: mark the picture of %s checked: %w", room, err)
				}
				continue
			}
			if err := a.keepPicture(ctx, room, version, thumbs[person], now); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	return changed, nil
}

func (a *Adapter) keepPicture(ctx context.Context, room domain.RoomID, version string, data []byte, now time.Time) error {
	if err := a.cache.SetPicture(ctx, room, version, data, now); err != nil {
		return fmt.Errorf("gmessages: keep the picture of %s: %w", room, err)
	}
	return nil
}
