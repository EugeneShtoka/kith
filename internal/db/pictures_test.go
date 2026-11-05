package db

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// pictureModel is what the cache should hold of pictures: each room's version and its
// bytes, the rooms listed, and the rooms merged into another.
type pictureModel struct {
	kept   map[domain.RoomID][2]string
	listed map[domain.RoomID]bool
	merged map[domain.RoomID]domain.RoomID
}

func (m *pictureModel) set(room domain.RoomID, id, data string) {
	if into, ok := m.merged[room]; ok {
		room = into
	}
	if !m.listed[room] {
		return
	}
	if id == "" || data == "" {
		id, data = "", ""
	}
	m.kept[room] = [2]string{id, data}
}

// pictureStep is one write the cache's pictures take, and what it does to the model.
type pictureStep struct {
	name  string
	do    func(ctx context.Context, c *Cache) error
	model func(m *pictureModel)
}

const strangerRoom = domain.RoomID("whatsapp:15550100/15550123@s.whatsapp.net")

func setPicture(room domain.RoomID, id, data string) pictureStep {
	return pictureStep{
		name: string(room) + " takes " + id + "=" + data,
		do: func(ctx context.Context, c *Cache) error {
			return c.SetPicture(ctx, room, id, []byte(data), time.UnixMilli(1))
		},
		model: func(m *pictureModel) { m.set(room, id, data) },
	}
}

var pictureSteps = []pictureStep{
	setPicture(lidRoom, "p1", "one"),
	setPicture(lidRoom, "p2", "two"),
	setPicture(lidRoom, "p3", ""), // a version without bytes
	setPicture(lidRoom, "", ""),   // found to have none
	setPicture(numberRoom, "q1", "q"),
	setPicture(strangerRoom, "s1", "s"), // a person no chat is with
	{
		name:  "the LID room is unchanged",
		do:    func(ctx context.Context, c *Cache) error { return c.PictureUnchanged(ctx, lidRoom, time.UnixMilli(2)) },
		model: func(*pictureModel) {},
	},
	{
		name: "the LID room merges into the number's",
		do: func(ctx context.Context, c *Cache) error {
			_, err := c.MergeRoom(ctx, domain.RoomMerge{From: lidRoom, Into: numberRoom, FromPerson: lidPerson, IntoPerson: numberPeson})
			return err
		},
		// The alias is kept even once the LID room is gone: a later write naming it is
		// the number's.
		model: func(m *pictureModel) {
			m.merged[lidRoom] = numberRoom
			if !m.listed[lidRoom] {
				return
			}
			if _, ok := m.kept[numberRoom]; !ok {
				if from, ok := m.kept[lidRoom]; ok {
					m.kept[numberRoom] = from
				}
			}
			delete(m.kept, lidRoom)
			delete(m.listed, lidRoom)
		},
	},
	{
		name: "the LID room leaves the listing",
		do: func(ctx context.Context, c *Cache) error {
			return c.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, "15550100"), []domain.Room{{ID: numberRoom, IsDirect: true}})
		},
		model: func(m *pictureModel) {
			delete(m.kept, lidRoom)
			delete(m.listed, lidRoom)
		},
	},
}

// Whatever order pictures are written, merged and swept in, a room's version names
// exactly the bytes it was given with, and a room without a version has no picture.
func TestAPicturesVersionNamesItsBytesOverAnyWrites(t *testing.T) {
	t.Parallel()
	const depth = 3
	cache := openTemp(t) // emptied for each path: a cache apiece takes minutes
	var walk func(path []int)
	walk = func(path []int) {
		if len(path) == depth {
			checkPictureSteps(t, cache, path)
			return
		}
		for i := range pictureSteps {
			walk(append(path, i))
		}
	}
	walk(nil)
}

func checkPictureSteps(t *testing.T, cache *Cache, path []int) {
	t.Helper()
	ctx := context.Background()
	if err := cache.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, "15550100")
	if err := cache.SaveRooms(ctx, owner, []domain.Room{{ID: lidRoom, IsDirect: true}, {ID: numberRoom, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	m := &pictureModel{
		kept:   map[domain.RoomID][2]string{},
		listed: map[domain.RoomID]bool{lidRoom: true, numberRoom: true},
		merged: map[domain.RoomID]domain.RoomID{},
	}
	var names []string
	for _, i := range path {
		step := pictureSteps[i]
		names = append(names, step.name)
		if err := step.do(ctx, cache); err != nil {
			t.Fatalf("%s: %v", strings.Join(names, ", then "), err)
		}
		step.model(m)
		rooms, err := cache.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range rooms {
			r := &rooms[i]
			want := m.kept[r.ID]
			got, err := cache.Picture(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.Picture != want[0] || !bytes.Equal(got, []byte(want[1])) || (want[1] == "") != (got == nil) {
				t.Fatalf("%s: %s is version %q with %q, want %q with %q",
					strings.Join(names, ", then "), r.ID, r.Picture, got, want[0], want[1])
			}
		}
		if got, err := cache.Picture(ctx, strangerRoom); err != nil || got != nil {
			t.Fatalf("%s: a room the cache does not hold has picture %q (%v)", strings.Join(names, ", then "), got, err)
		}
	}
}

// A room asked about is listed with its version and when it was asked, unchanged or not.
func TestPictureChecksSayWhenEachRoomWasAsked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, "15550100"),
		[]domain.Room{{ID: lidRoom, IsDirect: true}, {ID: numberRoom, IsDirect: true}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetPicture(ctx, lidRoom, "p1", []byte("one"), time.UnixMilli(10)); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetPicture(ctx, numberRoom, "", nil, time.UnixMilli(20)); err != nil {
		t.Fatal(err)
	}
	if err := cache.PictureUnchanged(ctx, lidRoom, time.UnixMilli(30)); err != nil {
		t.Fatal(err)
	}
	got, err := cache.PictureChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[domain.RoomID]domain.PictureCheck{
		lidRoom:    {ID: "p1", Checked: time.UnixMilli(30)},
		numberRoom: {ID: "", Checked: time.UnixMilli(20)},
	}
	if len(got) != len(want) || got[lidRoom] != want[lidRoom] || got[numberRoom] != want[numberRoom] {
		t.Fatalf("PictureChecks = %v, want %v", got, want)
	}
}
