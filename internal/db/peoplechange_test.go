package db

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// namedFrom is what kithd names people from, as the cache holds it: the directory, the
// joined rooms' names, and the members of every room it holds or held.
func namedFrom(t *testing.T, c *Cache, rooms ...domain.RoomID) string {
	t.Helper()
	ctx := context.Background()
	dir, err := c.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := c.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := fmt.Sprint(dir.Names(), dir.Links())
	for i := range joined {
		out += fmt.Sprintf(" room %s %q %q", joined[i].ID, joined[i].Name, joined[i].Membership)
		if !slices.Contains(rooms, joined[i].ID) {
			rooms = append(rooms, joined[i].ID)
		}
	}
	slices.Sort(rooms)
	for _, room := range rooms {
		members, err := c.Members(ctx, room, 0)
		if err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf(" members of %s %v", room, members)
	}
	return out
}

// Every pair of writes, from a start with a direct chat named after Bo, a number the
// phone book names and its bridge puppet: each write that changes what kithd names people
// from (namedFrom) tells OnPeopleChange, which kithd's names are built again on. A write
// it is not told of would leave a name stale until another write.
func TestEveryWriteThatCanRenameSomeoneSaysSo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const (
		bo     = "matrix:@bo:example.org"
		dana   = "whatsapp:15550100001@s.whatsapp.net"
		puppet = "matrix:@whatsapp_15550100001:example.org"
	)
	roomA, roomB, roomC := domain.RoomID(mx+"!a:x"), domain.RoomID(mx+"!b:x"), domain.RoomID(mx+"!c:x")
	type write struct {
		name string
		do   func(c *Cache) error
	}
	writes := []write{
		{"the phone book renaming Dana", func(c *Cache) error {
			return c.SetPeople(ctx, "book", []domain.PersonName{{Source: "book", ID: domain.PhoneID("15550100001"), Name: "Dana Cohen", Rank: domain.RankSaved}}, nil)
		}},
		{"a bridge linking the puppet", func(c *Cache) error {
			return c.SetPeople(ctx, "bridge", nil, []domain.PersonLink{{Source: "bridge", ID: puppet, Other: dana}})
		}},
		{"a members snapshot renaming Bo", func(c *Cache) error {
			return c.SaveMembers(ctx, roomA, []domain.Member{{UserID: bo, DisplayName: "Bobby"}})
		}},
		{"Bo renamed", func(c *Cache) error { return c.SaveMember(ctx, roomA, domain.Member{UserID: bo, DisplayName: "Bobby"}) }},
		{"the puppet named", func(c *Cache) error {
			return c.SaveMember(ctx, roomB, domain.Member{UserID: puppet, DisplayName: "Dana (WA)"})
		}},
		{"Bo leaving", func(c *Cache) error { return c.RemoveMember(ctx, roomA, bo) }},
		{"the rooms, Bo's renamed", func(c *Cache) error {
			return c.SaveRooms(ctx, mxRooms, []domain.Room{{ID: roomA, Name: "Bobby", IsDirect: true}, {ID: roomB, Name: "B"}})
		}},
		{"a room added", func(c *Cache) error { return c.AddRooms(ctx, mxRooms, []domain.Room{{ID: roomC, Name: "C"}}) }},
		{"a room swept", func(c *Cache) error {
			return c.SaveRooms(ctx, mxRooms, []domain.Room{{ID: roomA, Name: "Bo", IsDirect: true}})
		}},
		{"Bo's room forgotten", func(c *Cache) error { return c.ForgetRooms(ctx, []domain.RoomID{roomA}) }},
		{"the account's rooms forgotten", func(c *Cache) error { return c.ForgetOwned(ctx, mxRooms) }},
		{"a room merged into Bo's", func(c *Cache) error {
			_, err := c.MergeRoom(ctx, domain.RoomMerge{From: roomB, Into: roomA})
			return err
		}},
		{"the cache cleared", func(c *Cache) error { return c.Clear(ctx) }},
	}
	for _, first := range writes {
		for _, second := range writes {
			c := openTemp(t)
			if err := c.SaveRooms(ctx, mxRooms, []domain.Room{{ID: roomA, Name: "Bo", IsDirect: true}, {ID: roomB, Name: "B"}}); err != nil {
				t.Fatal(err)
			}
			if err := c.SaveMembers(ctx, roomA, []domain.Member{{UserID: "matrix:@me:example.org", DisplayName: "Me"}, {UserID: bo, DisplayName: "Bo"}}); err != nil {
				t.Fatal(err)
			}
			if err := c.SaveMembers(ctx, roomB, []domain.Member{{UserID: dana, DisplayName: "+15550100001"}}); err != nil {
				t.Fatal(err)
			}
			if err := c.SetPeople(ctx, "book", []domain.PersonName{{Source: "book", ID: domain.PhoneID("15550100001"), Name: "Dana Levi", Rank: domain.RankSaved}}, nil); err != nil {
				t.Fatal(err)
			}
			told := 0
			c.OnPeopleChange(func() { told++ })
			for _, w := range []write{first, second} {
				before := namedFrom(t, c, roomA, roomB, roomC)
				told = 0
				if err := w.do(c); err != nil {
					t.Fatalf("%s, then %s: %s: %v", first.name, second.name, w.name, err)
				}
				if namedFrom(t, c, roomA, roomB, roomC) != before && told == 0 {
					t.Errorf("%s, then %s: %q changed what people are named from, and told no one", first.name, second.name, w.name)
				}
			}
		}
	}
}
