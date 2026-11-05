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

// Pictures are kept in the cache through the account's sweeper (internal/pictures):
// WhatsApp is asked once per room, again when it tells of a change (events.Picture), and
// otherwise every pictureEvery, which catches what changed while kith was away and what
// a change event never reached.

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
// its sweeper (one sweep at a time). changed overrides what is asked of some rooms: a
// change WhatsApp told of. A chat that appears between listings waits for the next one.
func (a *Adapter) picturesLater(account Account, client *whatsmeow.Client, changed map[domain.RoomID]pictures.Want) {
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
	for _, room := range pictureRooms(rooms) {
		wants[room] = askPicture(client, room, pictureEvery)
	}
	maps.Copy(wants, changed)
	a.sweeper(account).Later(ctx, wants)
}

// sweeper is account's picture sweeper, made on first use.
func (a *Adapter) sweeper(account Account) *pictures.Sweeper[domain.RoomID] {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sweepers[account.Digits]
	if s == nil {
		s = &pictures.Sweeper[domain.RoomID]{
			Store: a.cache,
			Changed: func() {
				if a.onRoomsChanged != nil {
					a.onRoomsChanged()
				}
			},
			Failed: func(err error) { a.log.Debug("ask for pictures stopped", "account", account.Name, "err", err) },
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

// onPicture has the sweeper keep the picture WhatsApp says a chat changed to: asked at
// once, or none when it was removed.
func (a *Adapter) onPicture(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Picture) {
	chat := e.JID.ToNonAD()
	if chat.Server != types.GroupServer {
		chat = person(ctx, chat, types.EmptyJID, a.lookupFor(account)) // a direct chat by its person
	}
	room := roomID(account.Digits, chat)
	want := askPicture(client, room, 0)
	if e.Remove {
		want = pictures.Want{}
	}
	a.picturesLater(account, client, map[domain.RoomID]pictures.Want{room: want})
}

// askPicture is room's picture as asking WhatsApp tells it, every every (0: each sweep).
func askPicture(client *whatsmeow.Client, room domain.RoomID, every time.Duration) pictures.Want {
	return pictures.Want{Every: every, Ask: func(ctx context.Context, kept string) (string, []byte, error) {
		jid, err := types.ParseJID(domain.ParseID(string(room)).Native)
		if err != nil {
			return "", nil, fmt.Errorf("whatsapp: %s names no chat: %w", room, err)
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
		return "", nil, fmt.Errorf("whatsapp: ask for the picture of %s: %w", room, err)
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
