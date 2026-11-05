package local

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
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

// A saved picture is kept by its hash and named by it; an identity naming it shows it
// for each of its IDs in the room, and an emptied cache has it back once the identities
// are set again (the next start). A file that is no image is refused.
func TestASavedPictureIsAnIdentitysOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := backendWithCache(t, "matrix:@me:example.org")
	b.UseDataDir(t.TempDir())
	room := domain.RoomID(mx + "!r:x")
	wa := "whatsapp:15550101@s.whatsapp.net"
	png := append([]byte("\x89PNG\r\n\x1a\n"), "rest of it"...)
	src := filepath.Join(t.TempDir(), "dana.png")
	if err := os.WriteFile(src, png, 0o600); err != nil {
		t.Fatal(err)
	}
	picture, err := b.SavePicture(ctx, src)
	if err != nil || !strings.HasPrefix(picture, "file:") {
		t.Fatalf("SavePicture = %q, %v", picture, err)
	}
	name := strings.TrimPrefix(picture, "file:")
	if kept, err := os.ReadFile(filepath.Join(b.picturesDir(), name)); err != nil || !bytes.Equal(kept, png) {
		t.Fatalf("kept %q (%v), want the picture", kept, err)
	}
	notes := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notes, []byte("not a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SavePicture(ctx, notes); err == nil {
		t.Error("a text file was saved as a picture")
	}

	shows := func(when string) {
		t.Helper()
		seedRoom(t, b.cache, room)
		if err := b.cache.SaveMembers(ctx, room, []domain.Member{{UserID: wa}}); err != nil {
			t.Fatal(err)
		}
		got, err := b.SenderPictures(ctx, room)
		if err != nil || got[wa] != "via "+picture+" "+name {
			t.Errorf("%s: SenderPictures = %v (%v), want %s's saved picture", when, got, err, wa)
		}
		if data, err := b.LoadPicture(ctx, room, wa); err != nil || !bytes.Equal(data, png) {
			t.Errorf("%s: LoadPicture = %q (%v), want the saved picture", when, data, err)
		}
	}
	faces := setup.FacesOf([]config.Identity{{Alias: "Dana", IDs: []string{wa}, Picture: picture}})
	b.UseFaces(faces)
	shows("saved")
	if err := b.cache.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	b.UseFaces(faces) // the next start
	shows("after the cache was emptied")
}
