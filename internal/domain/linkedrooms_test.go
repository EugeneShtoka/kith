package domain_test

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A link names the listed chat it is for, as every account in it sees it; one naming a
// person, an alias or an invite names none, which only its network can look up.
func TestALinkNamesItsListedChat(t *testing.T) {
	t.Parallel()
	rooms := []domain.Room{
		{ID: mx + "!abc:x"},
		{ID: "matrix:fedcba9876543210/!abc:x"},
		{ID: "telegram:1/-1001", Handle: "novosalsa"},
		{ID: "whatsapp:2/15550100@s.whatsapp.net"},
	}
	for uri, want := range map[string][]domain.RoomID{
		"matrix:roomid/abc:x":           {mx + "!abc:x", "matrix:fedcba9876543210/!abc:x"},
		"https://matrix.to/#/!abc:x":    {mx + "!abc:x", "matrix:fedcba9876543210/!abc:x"},
		"tg://resolve?domain=novosalsa": {"telegram:1/-1001"},
		"https://wa.me/15550100":        {"whatsapp:2/15550100@s.whatsapp.net"},
		"matrix:r/somewhere:x":          nil,
		"matrix:u/dana:x":               nil,
		"https://t.me/+invitehash":      nil,
		"https://example.org":           nil,
	} {
		var got []domain.RoomID
		for _, r := range domain.LinkedRooms(uri, rooms) {
			got = append(got, r.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s names %v, want %v", uri, got, want)
		}
	}
}

// A link to a message names it in each chat, as that chat's account sees it.
func TestALinkNamesItsMessageInEachChat(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		uri  string
		room domain.RoomID
		want domain.EventID
	}{
		{"matrix:roomid/abc:x/e/evt", mx + "!abc:x", mx + "$evt"},
		{"matrix:roomid/abc:x/e/evt", "matrix:" + mxWork + "/!abc:x", domain.EventID("matrix:" + mxWork + "/$evt")},
		{"https://matrix.to/#/!abc:x/$evt", mx + "!abc:x", mx + "$evt"},
		{"https://t.me/novosalsa/42", "telegram:1/-1001", "telegram:1/-1001/42"},
		{"matrix:roomid/abc:x", mx + "!abc:x", ""},
		{"tg://resolve?domain=novosalsa", "telegram:1/-1001", ""},
		{"https://wa.me/15550100", "whatsapp:2/15550100@s.whatsapp.net", ""},
		{"https://example.org", mx + "!abc:x", ""},
	} {
		if got := domain.LinkedMessage(c.uri, c.room); got != c.want {
			t.Errorf("%s in %s names %q, want %q", c.uri, c.room, got, c.want)
		}
	}
}
