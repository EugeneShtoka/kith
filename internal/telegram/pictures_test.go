package telegram

import (
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Each listing names every chat's photo: a new one is downloaded, an unchanged one is
// not, a removed one is kept as none, and one Telegram refuses holds up no other.
func TestAListingKeepsEveryChatsPicture(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	var mu sync.Mutex
	photos := map[string]int64{"dana": 77, "club": 88, "refused": 99, "hikers": 55} // 0: none
	var downloads []int64
	d := f.cluster.Dispatch(2, "dc2")
	a, cache := loggedInWithStore(t, f, openStore(t))
	d.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetDialogsRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		res := &tg.MessagesDialogs{Users: []tg.UserClass{f.user}}
		if folder, _ := req.GetFolderID(); folder != 0 {
			return sendResult(s, r, res)
		}
		mu.Lock()
		defer mu.Unlock()
		chatPhotoOf := func(id int64) tg.ChatPhotoClass {
			if id == 0 {
				return &tg.ChatPhotoEmpty{}
			}
			return &tg.ChatPhoto{PhotoID: id, DCID: 2}
		}
		person := *dana
		person.Photo = &tg.UserProfilePhotoEmpty{}
		if photos["dana"] != 0 {
			person.Photo = &tg.UserProfilePhoto{PhotoID: photos["dana"], DCID: 2}
		}
		res.Users = append(res.Users, &person)
		res.Dialogs = []tg.DialogClass{
			&tg.Dialog{Peer: &tg.PeerUser{UserID: dana.ID}},
			&tg.Dialog{Peer: &tg.PeerChat{ChatID: 11}},
			&tg.Dialog{Peer: &tg.PeerChat{ChatID: 12}},
			&tg.Dialog{Peer: &tg.PeerChat{ChatID: 13}},
		}
		res.Chats = []tg.ChatClass{
			&tg.Chat{ID: 11, Title: "Book club", Photo: chatPhotoOf(photos["club"])},
			&tg.Chat{ID: 12, Title: "Refused", Photo: chatPhotoOf(photos["refused"])},
			&tg.Chat{ID: 13, Title: "Hikers", Photo: chatPhotoOf(photos["hikers"])},
		}
		return sendResult(s, r, res)
	})
	d.HandleFunc(tg.UploadGetFileRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.UploadGetFileRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		loc, ok := req.Location.(*tg.InputPeerPhotoFileLocation)
		if !ok {
			return s.SendErr(r, tgerr.New(400, "LOCATION_INVALID"))
		}
		mu.Lock()
		downloads = append(downloads, loc.PhotoID)
		mu.Unlock()
		if loc.PhotoID == 99 {
			return s.SendErr(r, tgerr.New(400, "PHOTO_INVALID"))
		}
		if req.Offset > 0 {
			return sendResult(s, r, &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Mtime: 1})
		}
		return sendResult(s, r, &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: []byte("photo " + strconv.FormatInt(loc.PhotoID, 10)), Mtime: 1})
	})
	dm, club, refused, hikers := domain.RoomID("telegram:42/7"), domain.RoomID("telegram:42/-11"), domain.RoomID("telegram:42/-12"), domain.RoomID("telegram:42/-13")
	pictures := func() map[domain.RoomID]string {
		rooms, err := a.Rooms(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		out := map[domain.RoomID]string{}
		for i := range rooms {
			data, err := cache.Picture(t.Context(), rooms[i].ID)
			if err != nil {
				t.Fatal(err)
			}
			out[rooms[i].ID] = rooms[i].Picture + "=" + string(data)
		}
		return out
	}
	settled := func(want map[domain.RoomID]string) {
		t.Helper()
		var got map[domain.RoomID]string
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if got = pictures(); got[dm] == want[dm] && got[club] == want[club] && got[refused] == want[refused] && got[hikers] == want[hikers] {
				return
			}
		}
		t.Fatalf("pictures = %v, want %v", got, want)
	}

	if _, err := a.RefreshRooms(t.Context()); err != nil {
		t.Fatal(err)
	}
	settled(map[domain.RoomID]string{dm: "77=photo 77", club: "88=photo 88", refused: "=", hikers: "55=photo 55"})

	mu.Lock()
	photos["dana"], photos["club"], downloads = 78, 0, nil
	mu.Unlock()
	if _, err := a.RefreshRooms(t.Context()); err != nil {
		t.Fatal(err)
	}
	settled(map[domain.RoomID]string{dm: "78=photo 78", club: "=", refused: "=", hikers: "55=photo 55"})
	mu.Lock()
	defer mu.Unlock()
	// In room order: the refused one is asked again, the sweep goes on past it, and the
	// unchanged one is not downloaded again.
	if !slices.Equal(downloads, []int64{99, 78}) {
		t.Errorf("the second listing downloaded %v, want the refused photo, then the changed one", downloads)
	}
}

// The users a history page names have their photos kept as people's pictures, a
// changed one downloaded again; a min user, whose photo is reached only through its
// message, is not asked.
func TestAPagesPeopleHaveTheirPicturesKept(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.serveUpdates(&updatesOf{pts: 1})
	d := f.cluster.Dispatch(2, "dc2")
	var mu sync.Mutex
	photoID := int64(77)
	var downloads []int64
	d.HandleFunc(tg.MessagesGetHistoryRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		mu.Lock()
		person := *dana
		person.Photo = &tg.UserProfilePhoto{PhotoID: photoID, DCID: 2}
		mu.Unlock()
		stranger := &tg.User{ID: 9, Min: true, Photo: &tg.UserProfilePhoto{PhotoID: 66, DCID: 2}}
		return sendResult(s, r, &tg.MessagesMessages{
			Users:    []tg.UserClass{&person, stranger},
			Messages: []tg.MessageClass{&tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 7}, Message: "m", Date: 1001}},
		})
	})
	d.HandleFunc(tg.UploadGetFileRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.UploadGetFileRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		loc := req.Location.(*tg.InputPeerPhotoFileLocation)
		if req.Offset > 0 {
			return sendResult(s, r, &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Mtime: 1})
		}
		mu.Lock()
		downloads = append(downloads, loc.PhotoID)
		mu.Unlock()
		return sendResult(s, r, &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: []byte("photo " + strconv.FormatInt(loc.PhotoID, 10)), Mtime: 1})
	})
	st := openStore(t)
	knowDana(t, st)
	a, cache := loggedInWithStore(t, f, st)
	kept := func(want string) {
		t.Helper()
		var checks map[string]domain.PictureCheck
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			var err error
			if checks, err = cache.PersonPictures().PictureChecks(t.Context()); err != nil {
				t.Fatal(err)
			}
			if checks[personID(7)].ID == want {
				break
			}
		}
		if checks[personID(7)].ID != want || len(checks) != 1 {
			t.Fatalf("people's pictures = %v, want only Dana's at %s", checks, want)
		}
	}
	room := domain.RoomID("telegram:42/7")
	if _, err := a.Timeline(t.Context(), room, "", 4); err != nil {
		t.Fatal(err)
	}
	kept("77")
	if _, err := a.Timeline(t.Context(), room, "", 4); err != nil { // the same photo: not downloaded again
		t.Fatal(err)
	}
	mu.Lock()
	photoID = 78
	mu.Unlock()
	if _, err := a.Timeline(t.Context(), room, "", 4); err != nil {
		t.Fatal(err)
	}
	kept("78")
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(downloads, []int64{77, 78}) {
		t.Errorf("downloaded %v, want Dana's photo, then their new one", downloads)
	}
}
