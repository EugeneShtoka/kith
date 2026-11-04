package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Pictures are kept in the cache (db.SetPicture): WhatsApp is asked once per room, again
// when it tells of a change (events.Picture), and otherwise every pictureEvery, which
// catches what changed while kith was away and what a change event never reached.

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

// picturesDue are the rooms whose picture is due to be asked for at now: never asked,
// or not within pictureEvery.
func picturesDue(rooms []domain.Room, checks map[domain.RoomID]db.PictureCheck, now time.Time) []domain.RoomID {
	var due []domain.RoomID
	for i := range rooms {
		id := rooms[i].ID
		jid, err := types.ParseJID(domain.ParseID(string(id)).Native)
		if err != nil || !askable(jid) {
			continue
		}
		if check, ok := checks[id]; !ok || now.Sub(check.Checked) >= pictureEvery {
			due = append(due, id)
		}
	}
	return due
}

// picturesLater asks for an account's due pictures off the event goroutine, one sweep
// per account at a time. A chat that appears between listings waits for the next one.
func (a *Adapter) picturesLater(account Account, client *whatsmeow.Client) {
	if a.cache == nil {
		return
	}
	a.mu.Lock()
	if a.sweeping[account.Digits] {
		a.mu.Unlock()
		return
	}
	a.sweeping[account.Digits] = true
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.sweeping, account.Digits)
			a.mu.Unlock()
		}()
		if err := a.sweepPictures(a.lifetime(), account, client); err != nil {
			a.log.Debug("ask for pictures stopped", "account", account.Name, "err", err)
		}
	}()
}

// sweepPictures asks for each of an account's due pictures in turn. It stops at the
// first ask that fails (offline, refused for asking too fast): the rest stay due.
func (a *Adapter) sweepPictures(ctx context.Context, account Account, client *whatsmeow.Client) error {
	rooms, err := a.accountRooms(ctx, account, func(domain.RoomID) bool { return true })
	if err != nil {
		return err
	}
	checks, err := a.cache.PictureChecks(ctx)
	if err != nil {
		return fmt.Errorf("whatsapp: read the pictures asked for: %w", err)
	}
	changed := false
	defer func() {
		if changed && a.onRoomsChanged != nil {
			a.onRoomsChanged()
		}
	}()
	for _, room := range picturesDue(rooms, checks, time.Now()) {
		got, err := a.askPicture(ctx, client, room, checks[room].ID)
		changed = changed || got
		if err != nil {
			return err
		}
	}
	return nil
}

// pictureChanged hands a picture's change to onPicture, off the event goroutine.
func (a *Adapter) pictureChanged(account Account, client *whatsmeow.Client, evt any) {
	if e, ok := evt.(*events.Picture); ok {
		go a.onPicture(a.lifetime(), account, client, e)
	}
}

// onPicture keeps the picture WhatsApp says a chat changed to.
func (a *Adapter) onPicture(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Picture) {
	if a.cache == nil {
		return
	}
	chat := e.JID.ToNonAD()
	if chat.Server != types.GroupServer {
		chat = person(ctx, chat, types.EmptyJID, a.lookupFor(account)) // a direct chat by its person
	}
	room := roomID(account.Digits, chat)
	var changed bool
	var err error
	if e.Remove {
		changed, err = true, a.cache.SetPicture(ctx, room, "", nil, time.Now())
	} else {
		changed, err = a.askPicture(ctx, client, room, "")
	}
	if err != nil {
		a.log.Warn("keep a changed picture failed", "room", room, "err", err)
	}
	if changed && a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}

// askPicture asks WhatsApp for room's picture, known the ID kept ("" none), and keeps
// the answer; changed is whether the kept picture changed. Asks are one at a time, each
// kept before the next, so an older answer never overwrites a newer one.
func (a *Adapter) askPicture(ctx context.Context, client *whatsmeow.Client, room domain.RoomID, known string) (changed bool, err error) {
	jid, err := types.ParseJID(domain.ParseID(string(room)).Native)
	if err != nil {
		return false, fmt.Errorf("whatsapp: %s names no chat: %w", room, err)
	}
	a.asking.Lock()
	defer a.asking.Unlock()
	info, err := client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: true, ExistingID: known})
	now := time.Now()
	var kerr error
	switch answerOf(known, info, err) {
	case pictureSame:
		kerr = a.cache.PictureUnchanged(ctx, room, now)
	case pictureNone:
		changed, kerr = known != "", a.cache.SetPicture(ctx, room, "", nil, now)
	case pictureNew:
		data, derr := downloadPicture(ctx, info.URL)
		if derr != nil {
			return false, derr
		}
		changed, kerr = true, a.cache.SetPicture(ctx, room, info.ID, data, now)
	default:
		if err == nil {
			err = errors.New("an answer without a picture")
		}
		return false, fmt.Errorf("whatsapp: ask for the picture of %s: %w", room, err)
	}
	if kerr != nil {
		return false, fmt.Errorf("whatsapp: keep the picture of %s: %w", room, kerr)
	}
	return changed, nil
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
