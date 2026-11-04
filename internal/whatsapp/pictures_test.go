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

	"github.com/EugeneShtoka/kith/internal/db"
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

// People and groups are asked when never asked or not for a week; channels never.
func TestPicturesAreDueWhenNeverAskedOrAWeekOld(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_000_000_000_000)
	at := func(s string) domain.RoomID { return domain.RoomID("whatsapp:" + ownDigits + "/" + s) }
	rooms := []domain.Room{
		{ID: at("1203@g.us")}, {ID: at("15550101@s.whatsapp.net")}, {ID: at("88001@lid")},
		{ID: at("120363@newsletter")}, {ID: at("1204@g.us")}, {ID: at("1205@g.us")},
	}
	checks := map[domain.RoomID]db.PictureCheck{
		at("15550101@s.whatsapp.net"): {ID: "p1", Checked: now.Add(-pictureEvery)},
		at("1204@g.us"):               {ID: "", Checked: now.Add(-pictureEvery + time.Minute)},
		at("1205@g.us"):               {ID: "p2", Checked: now.Add(-time.Hour)},
	}
	got := picturesDue(rooms, checks, now)
	want := []domain.RoomID{at("1203@g.us"), at("15550101@s.whatsapp.net"), at("88001@lid")}
	if !slices.Equal(got, want) {
		t.Fatalf("picturesDue = %v, want %v", got, want)
	}
}

// A picture removed is kept as none at once, and the rooms are said to have changed.
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
	changed := 0
	a.OnRoomsChanged(func() { changed++ })
	group, _ := types.ParseJID("1203@g.us")
	a.onPicture(ctx, Account{Name: "home", Digits: ownDigits}, nil, &events.Picture{JID: group, Remove: true})
	rooms, err := a.Rooms(ctx)
	if err != nil || len(rooms) != 1 || rooms[0].Picture != "" || changed != 1 {
		t.Fatalf("after the removal: rooms %+v (%v), %d changes; want no picture, one change", rooms, err, changed)
	}
	if data, err := cache.Picture(ctx, waRoom); data != nil || err != nil {
		t.Fatalf("Picture = %q, %v; want none", data, err)
	}
}

// A sweep whose first ask fails (offline) stops there, keeping every room due.
func TestASweepThatCannotAskKeepsEveryRoomDue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, cache, store := offline(t, Account{Name: "home", Digits: ownDigits})
	other := domain.RoomID("whatsapp:" + ownDigits + "/15550101@s.whatsapp.net")
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: waRoom}, {ID: other, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	changed := 0
	a.OnRoomsChanged(func() { changed++ })
	client := linkedClient(t, store, ownDigits)
	if err := a.sweepPictures(ctx, Account{Name: "home", Digits: ownDigits}, client); err == nil {
		t.Fatal("a sweep with no connection succeeded")
	}
	checks, err := cache.PictureChecks(ctx)
	if err != nil || len(checks) != 0 || changed != 0 {
		t.Fatalf("after a failed sweep: checks %v (%v), %d changes; want none", checks, err, changed)
	}
}
