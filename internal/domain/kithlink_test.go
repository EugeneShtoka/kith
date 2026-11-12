package domain_test

import (
	"net/url"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// kithLink writes a kith link as the docs say to: the room path-escaped, the event
// query-escaped.
func kithLink(room, event string) string {
	link := "kith:" + url.PathEscape(room)
	if event != "" {
		link += "?event=" + url.QueryEscape(event)
	}
	return link
}

const (
	kithWAGroup = "whatsapp:359884650326/120363429032076834@g.us"
	kithWAMsg   = "whatsapp:359884650326/3BF495B87692C4F46C02"
	kithSlackCh = "slack:T01K2D5TCAC/C08HVEWCKQE"
	kithSlackTS = "slack:T01K2D5TCAC/C08HVEWCKQE/1794382539.534909"
)

// A kith link names a room by kith's ID on any network, and a message in it by the
// event ID the same account sees.
func TestAKithLinkNamesARoomAndItsMessage(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		uri  string
		want domain.Place
	}{
		{kithLink(kithWAGroup, ""), domain.Place{Kind: domain.PlaceRoom, Room: kithWAGroup}},
		{kithLink(kithWAGroup, kithWAMsg), domain.Place{Kind: domain.PlaceEvent, Room: kithWAGroup, Event: kithWAMsg}},
		{kithLink(kithSlackCh, kithSlackTS), domain.Place{Kind: domain.PlaceEvent, Room: kithSlackCh, Event: kithSlackTS}},
		{kithLink(mx+"!abc:x", mx+"$evt"), domain.Place{Kind: domain.PlaceEvent, Room: mx + "!abc:x", Event: mx + "$evt"}},
		{kithLink("telegram:1/-1001", "telegram:1/-1001/42"), domain.Place{Kind: domain.PlaceEvent, Room: "telegram:1/-1001", Event: "telegram:1/-1001/42"}},
		// Typed by hand: the scheme in any case, the room's slash left as it is.
		{"  KITH:" + kithWAGroup + "?event=" + url.QueryEscape(kithWAMsg) + " ", domain.Place{Kind: domain.PlaceEvent, Room: kithWAGroup, Event: kithWAMsg}},
	} {
		got, ok := domain.ParseKithLink(c.uri)
		if !ok || got.Kind != c.want.Kind || got.Room != c.want.Room || got.Event != c.want.Event {
			t.Errorf("%s reads as %+v (%v), want %+v", c.uri, got, ok, c.want)
		}
	}
}

// What is not one account's room, or a message of another account's, is no kith link:
// following it could open nothing or the wrong chat.
func TestWhatNamesNoRoomIsNoKithLink(t *testing.T) {
	t.Parallel()
	for _, uri := range []string{
		"kith:",
		"kith:" + url.PathEscape("whatsapp:359884650326@s.whatsapp.net"),          // a person
		"kith:" + url.PathEscape("matrix:@dana:x"),                                // a person
		"kith:" + url.PathEscape("nowhere:1/2"),                                   // no network
		"kith:" + url.PathEscape(kithWAMsg),                                       // a message, no room
		"kith:%zz",                                                                // a broken escape
		kithLink(kithWAGroup, "") + "?event=",                                     // an empty message
		kithLink(kithWAGroup, "") + "?event=%zz",                                  // a broken escape
		kithLink(kithWAGroup, kithWAMsg) + "&event=" + url.QueryEscape(kithWAMsg), // two messages
		kithLink(kithWAGroup, "whatsapp:111/3BF495B87692C4F46C02"),                // another account's
		kithLink(kithWAGroup, "3BF495B87692C4F46C02"),                             // no account's
		kithLink(kithWAGroup, "whatsapp:359884650326/15550100@s.whatsapp.net"),    // a room
		kithLink(kithSlackCh, "slack:T0OTHER/C08HVEWCKQE/1794382539.534909"),      // another team's
		"matrix:roomid/abc:x",
		"https://example.org/kith:" + kithWAGroup,
	} {
		if got, ok := domain.ParseKithLink(uri); ok {
			t.Errorf("%s reads as %+v", uri, got)
		}
	}
}

// A kith link names its exact room, never another account's view of the same chat, and
// its message only there; it is followed when its room's network is one kith is on.
func TestAKithLinkNamesOnlyItsOwnRoom(t *testing.T) {
	t.Parallel()
	rooms := []domain.Room{
		{ID: mx + "!abc:x"},
		{ID: "matrix:fedcba9876543210/!abc:x"},
		{ID: kithWAGroup},
		{ID: "whatsapp:111/120363429032076834@g.us"},
	}
	for uri, want := range map[string][]domain.RoomID{
		kithLink(mx+"!abc:x", mx+"$evt"):   {mx + "!abc:x"},
		kithLink(kithWAGroup, kithWAMsg):   {kithWAGroup},
		kithLink(kithSlackCh, kithSlackTS): nil,
	} {
		var got []domain.RoomID
		for _, r := range domain.LinkedRooms(uri, rooms) {
			got = append(got, r.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s names %v, want %v", uri, got, want)
		}
	}
	for _, c := range []struct {
		room domain.RoomID
		want domain.EventID
	}{
		{kithWAGroup, kithWAMsg},
		{"whatsapp:111/120363429032076834@g.us", ""},
	} {
		if got := domain.LinkedMessage(kithLink(kithWAGroup, kithWAMsg), c.room); got != c.want {
			t.Errorf("in %s the link names %q, want %q", c.room, got, c.want)
		}
	}
	for uri, want := range map[string]bool{
		kithLink(kithWAGroup, kithWAMsg):   true,
		kithLink(kithSlackCh, kithSlackTS): false, // no Slack account
	} {
		if got := domain.Followable(uri, rooms); got != want {
			t.Errorf("%s followable = %v, want %v", uri, got, want)
		}
	}
}
