package gmessages

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A group wants its picture's address; a one-to-one conversation the thumbnail of its
// person, when the phone may keep one (a contact, Google's own senders), else none.
func TestEachConversationWantsItsPicture(t *testing.T) {
	t.Parallel()
	contact := participant("2", danaNum, "Dana Reyes", false)
	contact.ContactID = "c2"
	google := participant("4", "verify.goog", "Google", false)
	in := newInbox(self)
	board := active(group, "Board", true, 1000, false, pMe, contact, pEli)
	board.GroupAvatarURL = "https://lh3.example/board"
	for _, c := range []*gmproto.Conversation{
		board,
		active(danaDM, "", false, 1000, false, pMe, contact),
		active("13", "", false, 1000, false, pMe, pEli),
		active("14", "", false, 1000, false, pMe, google),
		active("15", "Untitled", true, 1000, false, pMe, pEli, contact),
	} {
		in.take(c)
	}
	want := map[domain.RoomID]pictureWant{
		roomID(self, group):  {group: true, url: "https://lh3.example/board"},
		roomID(self, danaDM): {person: "2"},
		roomID(self, "13"):   {},
		roomID(self, "14"):   {person: "4"},
		roomID(self, "15"):   {group: true},
	}
	got := in.pictureWants()
	if len(got) != len(want) {
		t.Fatalf("pictureWants = %+v, want %+v", got, want)
	}
	for room, w := range want {
		if got[room] != w {
			t.Errorf("%s wants %+v, want %+v", room, got[room], w)
		}
	}
}

// What needs no phone is done without one: a conversation with no picture is kept as
// none once, a group's unchanged picture and a thumbnail asked for within a day are
// not fetched again.
func TestASweepFetchesOnlyWhatIsDue(t *testing.T) {
	t.Parallel()
	a, _, cache, _ := cachedAdapter(t)
	ctx := context.Background()
	now := time.UnixMilli(1_000_000_000_000)
	rooms := []domain.RoomID{roomID(self, group), roomID(self, danaDM), roomID(self, "13")}
	var listed []domain.Room
	for _, r := range rooms {
		listed = append(listed, domain.Room{ID: r, Membership: domain.MembershipJoin})
	}
	if err := cache.SaveRooms(ctx, owner(self), listed); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetPicture(ctx, rooms[0], "https://lh3.example/board", []byte("board"), now); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetPicture(ctx, rooms[1], thumbVersion([]byte("dana")), []byte("dana"), now.Add(-thumbEvery+time.Minute)); err != nil {
		t.Fatal(err)
	}
	wants := map[domain.RoomID]pictureWant{
		rooms[0]: {group: true, url: "https://lh3.example/board"},
		rooms[1]: {person: "2"},
		rooms[2]: {},
	}
	// No client: anything fetched would fail the sweep.
	if err := a.sweepPictures(ctx, nil, wants, now); err != nil {
		t.Fatalf("a sweep with nothing due fetched: %v", err)
	}
	checks, err := cache.PictureChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := checks[rooms[2]]; !ok || c.ID != "" || !c.Checked.Equal(now) {
		t.Errorf("a conversation with no picture is %+v, %v; want kept as none", c, ok)
	}
	// A day on, the thumbnail is due: with no client to ask, the sweep fails.
	if err := a.sweepPictures(ctx, nil, wants, now.Add(time.Minute)); err == nil {
		t.Error("a sweep with a thumbnail due asked nobody")
	}
}
