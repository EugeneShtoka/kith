package local

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// TestAnIdentitysPictureStandsInOnlyWhereItsPersonIs walks every way an identity of two
// IDs can stand: each with a picture or none, and its picture naming neither, the first
// or the second. Only the first is in the room. In the room the first shows the picture
// its chain picks (the chosen one, then its own, then the other's); the second, not in
// the room, shows nothing there, nor does anyone outside the identity who is not there.
func TestAnIdentitysPictureStandsInOnlyWhereItsPersonIs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := backendWithCache(t, "matrix:@me:example.org")
	room := domain.RoomID(mx + "!r:x")
	wa, tg, stranger := "whatsapp:15550101@s.whatsapp.net", "telegram:42", "telegram:99"
	for _, waHas := range []bool{false, true} {
		for _, tgHas := range []bool{false, true} {
			for _, chosen := range []string{"", wa, tg} {
				if err := b.cache.Clear(ctx); err != nil {
					t.Fatal(err)
				}
				seedRoom(t, b.cache, room)
				if err := b.cache.SaveMembers(ctx, room, []domain.Member{{UserID: wa}}); err != nil {
					t.Fatal(err)
				}
				pics := map[string]bool{wa: waHas, tg: tgHas, stranger: true}
				for p, has := range pics {
					id := ""
					if has {
						id = "v-" + p
					}
					if err := b.cache.PersonPictures().SetPicture(ctx, p, id, []byte(id), time.UnixMilli(1)); err != nil {
						t.Fatal(err)
					}
				}
				chain := []string{wa, tg} // the chosen one, then wa's own, then the rest
				if chosen == tg {
					chain = []string{tg, wa}
				}
				b.UseFaces(setup.FacesOf([]config.Identity{{Alias: "Dana", IDs: []string{wa, tg}, Picture: chosen}}))

				// The model: the first of the chain with a picture.
				source := ""
				for _, id := range chain {
					if pics[id] {
						source = id
						break
					}
				}
				name := "wa=" + map[bool]string{true: "pic", false: "none"}[waHas] + " tg=" + map[bool]string{true: "pic", false: "none"}[tgHas] + " chosen=" + chosen
				want := map[string]string{}
				switch source {
				case "":
				case wa:
					want[wa] = "v-" + wa
				default:
					want[wa] = "via " + source + " v-" + source
				}
				got, err := b.SenderPictures(ctx, room)
				if err != nil {
					t.Fatal(err)
				}
				if !maps.Equal(got, want) {
					t.Errorf("%s: SenderPictures = %v, want %v", name, got, want)
				}
				wantData := map[string]string{wa: map[bool]string{true: "v-" + source, false: ""}[source != ""], tg: "", stranger: ""}
				for p, w := range wantData {
					data, err := b.LoadPicture(ctx, room, p)
					if err != nil {
						t.Fatal(err)
					}
					if string(data) != w {
						t.Errorf("%s: LoadPicture(%s) = %q, want %q", name, p, data, w)
					}
				}
			}
		}
	}
}
