package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestAnAnswerAboutAPictureIsReadByWhatItSays(t *testing.T) {
	t.Parallel()
	picture := &types.ProfilePictureInfo{ID: "p2", URL: "https://pps.example/p2"}
	for _, tc := range []struct {
		name  string
		known string
		info  *types.ProfilePictureInfo
		err   error
		want  pictureAnswer
	}{
		{"unchanged since the ID kept", "p1", nil, nil, pictureSame},
		{"the ID kept, given again", "p2", picture, nil, pictureSame},
		{"a new picture", "p1", picture, nil, pictureNew},
		{"a first picture", "", picture, nil, pictureNew},
		{"none set", "p1", nil, fmt.Errorf("iq: %w", whatsmeow.ErrProfilePictureNotSet), pictureNone},
		{"hidden from this account", "", nil, whatsmeow.ErrProfilePictureUnauthorized, pictureNone},
		{"not connected", "p1", nil, whatsmeow.ErrNotConnected, pictureFailed},
		{"asked too fast", "", nil, errors.New("rate-overlimit"), pictureFailed},
		{"an answer naming no picture", "", &types.ProfilePictureInfo{ID: "p3"}, nil, pictureFailed},
	} {
		if got := answerOf(tc.known, tc.info, tc.err); got != tc.want {
			t.Errorf("%s: answerOf = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// People and groups have pictures to ask for; channels do not.
func TestPeopleAndGroupsHavePictures(t *testing.T) {
	t.Parallel()
	at := func(s string) domain.RoomID { return domain.RoomID("whatsapp:" + ownDigits + "/" + s) }
	rooms := []domain.Room{
		{ID: at("1203@g.us")}, {ID: at("15550101@s.whatsapp.net")}, {ID: at("88001@lid")}, {ID: at("120363@newsletter")},
	}
	got := pictureRooms(rooms)
	want := []domain.RoomID{at("1203@g.us"), at("15550101@s.whatsapp.net"), at("88001@lid")}
	if !slices.Equal(got, want) {
		t.Fatalf("pictureRooms = %v, want %v", got, want)
	}
}

// A picture removed is kept as none, and the rooms are said to have changed.
func TestARemovedPictureIsKeptAsNone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, cache, _ := offline(t, Account{Name: "home", Digits: ownDigits})
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: waRoom}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetPicture(ctx, waRoom, "p1", []byte("one"), time.Now()); err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 1)
	a.OnRoomsChanged(func() { changed <- struct{}{} })
	group, _ := types.ParseJID("1203@g.us")
	a.onPicture(ctx, Account{Name: "home", Digits: ownDigits}, nil, &events.Picture{JID: group, Remove: true})
	<-changed
	rooms, err := a.Rooms(ctx)
	if err != nil || len(rooms) != 1 || rooms[0].Picture != "" {
		t.Fatalf("after the removal: rooms %+v (%v); want no picture", rooms, err)
	}
	if data, err := cache.Picture(ctx, waRoom); data != nil || err != nil {
		t.Fatalf("Picture = %q, %v; want none", data, err)
	}
}

// An ask that cannot reach WhatsApp (offline) fails, so the sweep keeps every room due.
func TestAnAskThatCannotReachWhatsAppFails(t *testing.T) {
	t.Parallel()
	_, _, store := offline(t, Account{Name: "home", Digits: ownDigits})
	client := linkedClient(t, store, ownDigits)
	if _, _, err := askPicture(client, waRoom, pictureEvery).Ask(context.Background(), "p1"); err == nil {
		t.Fatal("an ask with no connection succeeded")
	}
}
