package telegram

import (
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// archivedRooms is which cached rooms are archived.
func archivedRooms(t *testing.T, cache *db.Cache) []domain.RoomID {
	t.Helper()
	rooms, err := cache.Rooms(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.RoomID
	for i := range rooms {
		if rooms[i].Archived {
			out = append(out, rooms[i].ID)
		}
	}
	slices.Sort(out)
	return out
}

// A chat in Telegram's Archived folder is listed as archived; one in the main list,
// or one whose folder the listing did not say, is not.
func TestAListingSaysWhichChatsAreArchived(t *testing.T) {
	t.Parallel()
	ent := peer.NewEntities(nil, map[int64]*tg.Chat{11: {ID: 11, Title: "Old"}, 12: {ID: 12, Title: "Live"}}, nil)
	archived := &tg.Dialog{Peer: &tg.PeerChat{ChatID: 11}}
	archived.SetFolderID(archiveFolder)
	l := listed(42, []dialog{
		{peer: &tg.InputPeerChat{ChatID: 11}, entities: ent, info: archived},
		{peer: &tg.InputPeerChat{ChatID: 12}, entities: ent, info: &tg.Dialog{Peer: &tg.PeerChat{ChatID: 12}}},
	})
	if !l.archived["telegram:42/-11"] || l.archived["telegram:42/-12"] || len(l.archived) != 2 {
		t.Errorf("archived = %v", l.archived)
	}
}

// A listing is fetched, then written: whatever order chats are moved between folders
// live (here or on another device) and listings are fetched and written in, a listing
// never writes a chat's older folder over a move heard since it was fetched.
func TestAListingNeverUndoesAFolderMoveHeardSince(t *testing.T) {
	t.Parallel()
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 9))
		a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
		ctx, room := t.Context(), domain.RoomID("telegram:42/-11")
		rooms := []domain.Room{{ID: room, Name: "c", Membership: domain.MembershipJoin}}
		// Listed once first, as a connected account's chats are: a chat moved before any
		// listing is a placeholder, not yet a room to show.
		if err := a.save(ctx, 42, listing{rooms: rooms, archived: map[domain.RoomID]bool{room: false}}, time.Now()); err != nil {
			t.Fatal(err)
		}
		server, shown := false, false // in the Archived folder; what the adapter last heard
		var heardAt time.Time
		type fetched struct {
			archived bool
			at       time.Time
		}
		var pending []fetched
		for range 30 {
			switch rng.IntN(3) {
			case 0: // moved live
				server = rng.IntN(2) == 0
				a.movedLive(ctx, room, server)
				shown, heardAt = server, time.Now()
			case 1: // a listing fetched
				pending = append(pending, fetched{server, time.Now()})
			case 2: // the oldest listing written (listings never interleave)
				if len(pending) == 0 {
					continue
				}
				p := pending[0]
				pending = pending[1:]
				if err := a.save(ctx, 42, listing{rooms: rooms, archived: map[domain.RoomID]bool{room: p.archived}}, p.at); err != nil {
					t.Fatal(err)
				}
				if heardAt.Before(p.at) {
					shown = p.archived
				}
			}
			if got := len(archivedRooms(t, cache)) == 1; got != shown {
				t.Fatalf("seed %d: the cache says archived=%v, the newest heard is %v", seed, got, shown)
			}
			time.Sleep(time.Microsecond)
		}
	}
}

// Archiving here moves the chat into Telegram's Archived folder (and back to the main
// list); a move on another device arrives and is kept.
func TestAChatIsArchivedBothWays(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	u := &updatesOf{pts: 1}
	f.serveUpdates(u)
	var mu sync.Mutex
	var folders []int
	f.cluster.Dispatch(2, "dc2").HandleFunc(tg.FoldersEditPeerFoldersRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.FoldersEditPeerFoldersRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		for _, p := range req.FolderPeers {
			folders = append(folders, p.FolderID)
		}
		mu.Unlock()
		return sendResult(s, r, &tg.Updates{Date: int(time.Now().Unix())})
	})
	st := openStore(t)
	knowDana(t, st)
	a, cache := loggedInWithStore(t, f, st)
	ctx, room := t.Context(), domain.RoomID("telegram:42/7")
	if err := cache.AddRooms(ctx, domain.AccountRooms(domain.ProtocolTelegram, "42"), []domain.Room{{ID: room, Name: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetArchived(ctx, room, true); err != nil {
		t.Fatal(err)
	}
	if got := archivedRooms(t, cache); !slices.Equal(got, []domain.RoomID{room}) {
		t.Errorf("after archiving here: %v", got)
	}
	if err := a.SetArchived(ctx, room, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if !slices.Equal(folders, []int{archiveFolder, 0}) {
		t.Errorf("moved to folders %v", folders)
	}
	mu.Unlock()
	waitFor(t, "no session asked for its updates", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.session != nil })
	u.send(t, f.server(), 1, &tg.UpdateFolderPeers{FolderPeers: []tg.FolderPeer{{Peer: &tg.PeerUser{UserID: 7}, FolderID: archiveFolder}}, Pts: 2, PtsCount: 1})
	waitFor(t, "a move on another device was not kept", func() bool {
		return slices.Equal(archivedRooms(t, cache), []domain.RoomID{room})
	})
}
