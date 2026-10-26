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
