package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// Pictures are kept in the cache through the account's sweepers (internal/pictures):
// WhatsApp is asked once per room, and per person who wrote in one, again when it tells
// of a change (events.Picture), and otherwise every pictureEvery, which catches what
// changed while kith was away and what a change event never reached.

// pictureEvery is how long a kept picture, or a room's having none, is trusted.
const pictureEvery = 7 * 24 * time.Hour

// pictureBytes bounds a picture's download; WhatsApp's previews are a few KB.
const pictureBytes = 1 << 20

// pictureAnswer is what WhatsApp's answer about a room's picture means.
type pictureAnswer int

const (
	// pictureFailed keeps what is known, to be asked again later.
	pictureFailed pictureAnswer = iota
	pictureSame
	pictureNone
	pictureNew
)

// answerOf reads GetProfilePictureInfo's answer to an ask that knew known ("" none).
func answerOf(known string, info *types.ProfilePictureInfo, err error) pictureAnswer {
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet), errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return pictureNone // hidden from this account is none to it
	case err != nil:
		return pictureFailed
	case info == nil, info.ID == known:
		return pictureSame
	case info.ID == "" || info.URL == "":
		return pictureFailed
	default:
		return pictureNew
	}
}

// askable reports whether WhatsApp keeps a picture for chat by this ask: a person or a
// group, not a channel.
func askable(chat types.JID) bool {
	switch chat.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		return true
	}
	return false
}

// pictureRooms are the rooms WhatsApp keeps a picture for: people and groups.
func pictureRooms(rooms []domain.Room) []domain.RoomID {
	var out []domain.RoomID
	for i := range rooms {
		if jid, err := types.ParseJID(domain.ParseID(string(rooms[i].ID)).Native); err == nil && askable(jid) {
			out = append(out, rooms[i].ID)
		}
	}
	return out
}

// picturesLater asks for an account's due pictures off the event goroutine, through
// its sweepers (one sweep at a time each): its rooms', and those of the people who
// wrote in them. changed and changedPeople override what is asked of some: a change
// WhatsApp told of. A chat or a person that appears between listings waits for the
// next one.
func (a *Adapter) picturesLater(account Account, client *whatsmeow.Client, changed map[domain.RoomID]pictures.Want, changedPeople map[string]pictures.Want) {
	if a.cache == nil {
		return
	}
	ctx := a.lifetime()
	rooms, err := a.accountRooms(ctx, account, func(domain.RoomID) bool { return true })
	if err != nil {
		a.log.Warn("read the rooms to ask pictures of failed", "account", account.Name, "err", err)
		return
	}
	wants := map[domain.RoomID]pictures.Want{}
	ids := make([]domain.RoomID, 0, len(rooms))
	for _, room := range pictureRooms(rooms) {
		wants[room] = askPicture(client, string(room), pictureEvery)
	}
	for i := range rooms {
		ids = append(ids, rooms[i].ID)
	}
	maps.Copy(wants, changed)
	s := a.sweeper(account)
	s.rooms.Later(ctx, wants)

	senders, err := a.cache.Senders(ctx, domain.TheseRooms(ids))
	if err != nil {
		a.log.Warn("read the people to ask pictures of failed", "account", account.Name, "err", err)
		return
	}
	people := map[string]pictures.Want{}
	for _, p := range senders {
		if jid, err := types.ParseJID(domain.ParseID(p).Native); err == nil && askable(jid) {
			people[p] = askPicture(client, p, pictureEvery)
		}
	}
	maps.Copy(people, changedPeople)
	s.people.Later(ctx, people)
}

// sweepers are an account's picture sweepers: its rooms', and its people's.
type sweepers struct {
	rooms  pictures.Sweeper[domain.RoomID]
	people pictures.Sweeper[string]
}

// sweeper is account's picture sweepers, made on first use.
func (a *Adapter) sweeper(account Account) *sweepers {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sweepers[account.Digits]
	if s == nil {
		changed := func() {
			if a.onRoomsChanged != nil {
				a.onRoomsChanged()
			}
		}
		failed := func(err error) { a.log.Debug("ask for pictures stopped", "account", account.Name, "err", err) }
		s = &sweepers{
			rooms:  pictures.Sweeper[domain.RoomID]{Store: a.cache, Changed: changed, Failed: failed},
			people: pictures.Sweeper[string]{Store: a.cache.PersonPictures(), Changed: changed, Failed: failed},
		}
		a.sweepers[account.Digits] = s
	}
	return s
}

// pictureChanged hands a picture's change to onPicture, off the event goroutine.
func (a *Adapter) pictureChanged(account Account, client *whatsmeow.Client, evt any) {
	if e, ok := evt.(*events.Picture); ok {
		go a.onPicture(a.lifetime(), account, client, e)
	}
}

// onPicture has the sweepers keep the picture WhatsApp says a chat changed to, a
// person's also as theirs: asked at once, or none when it was removed.
func (a *Adapter) onPicture(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Picture) {
	chat := e.JID.ToNonAD()
	var people map[string]pictures.Want
	if chat.Server != types.GroupServer {
		chat = person(ctx, chat, types.EmptyJID, a.lookupFor(account)) // a direct chat by its person
		people = map[string]pictures.Want{}
	}
	room := roomID(account.Digits, chat)
	want := askPicture(client, string(room), 0)
	if e.Remove {
		want = pictures.Want{}
	}
	if people != nil {
		people[domain.NativePerson(domain.ProtocolWhatsApp, chat.String())] = want
	}
	a.picturesLater(account, client, map[domain.RoomID]pictures.Want{room: want}, people)
}

// askPicture is the picture of id, a room's or a person's, as asking WhatsApp tells it,
// every every (0: each sweep).
func askPicture(client *whatsmeow.Client, id string, every time.Duration) pictures.Want {
	return pictures.Want{Every: every, Ask: func(ctx context.Context, kept string) (string, []byte, error) {
		jid, err := types.ParseJID(domain.ParseID(id).Native)
		if err != nil {
			return "", nil, fmt.Errorf("whatsapp: %s names no chat: %w", id, err)
		}
		info, err := client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: true, ExistingID: kept})
		switch answerOf(kept, info, err) {
		case pictureSame:
			return kept, nil, nil
		case pictureNone:
			return "", nil, nil
		case pictureNew:
			data, derr := downloadPicture(ctx, info.URL)
			return info.ID, data, derr
		case pictureFailed:
		}
		if err == nil {
			err = errors.New("an answer without a picture")
		}
		return "", nil, fmt.Errorf("whatsapp: ask for the picture of %s: %w", id, err)
	}}
}

// downloadPicture fetches a picture WhatsApp named by its URL.
func downloadPicture(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: a picture's URL: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: download a picture: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whatsapp: download a picture: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, pictureBytes+1))
	if err != nil {
		return nil, fmt.Errorf("whatsapp: download a picture: %w", err)
	}
	if len(data) > pictureBytes {
		return nil, fmt.Errorf("whatsapp: a picture of over %d bytes", pictureBytes)
	}
	return data, nil
}
