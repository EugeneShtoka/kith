package matrix

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// sessionState is how an account stands when its cache is read.
type sessionState string

const (
	stateIn         sessionState = "logged in"
	stateOut        sessionState = "logged out"
	stateRejected   sessionState = "session rejected"
	stateUnreadable sessionState = "session unreadable"
	// A crypto store that will not open leaves the session up with encryption off.
	stateNoCrypto sessionState = "crypto store fails"
	stateRemoved  sessionState = "removed from the config"
)

var sessionStates = []sessionState{stateIn, stateOut, stateRejected, stateUnreadable, stateNoCrypto, stateRemoved}

// keptAccount is one account of the generator and what the cache holds of it.
type keptAccount struct {
	name, user string
	ids        ids
	room       domain.RoomID
	space      domain.SpaceID
}

func newKeptAccount(name string) keptAccount {
	user := "@" + name + ":x"
	x := accountIDs(user)
	return keptAccount{name: name, user: user, ids: x, room: domain.RoomID(x.scoped("!chat:x")), space: domain.SpaceID(x.scoped("!space:x"))}
}

// seedKept caches a room with a message, its unread state and a space holding it.
func seedKept(t *testing.T, m *Adapter, a keptAccount) {
	t.Helper()
	ctx := context.Background()
	if err := m.cache.SaveRooms(ctx, a.ids.owner(), []domain.Room{{ID: a.room, Name: a.name + "'s chat"}}); err != nil {
		t.Fatal(err)
	}
	msg := domain.Message{ID: domain.EventID(a.ids.scoped("$1")), RoomID: a.room, Sender: "matrix:@friend:x", Body: "kept", Timestamp: time.Unix(1, 0)}
	if err := m.cache.SaveMessages(ctx, a.room, []domain.Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := m.cache.SaveUnread(ctx, domain.Unread{RoomID: a.room, Notifications: 1}, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.cache.SaveSpaces(ctx, a.ids.owner(), []domain.Space{{ID: a.space, Name: a.name + "'s space", Children: []domain.RoomID{a.room}}}); err != nil {
		t.Fatal(err)
	}
}

// prepareState puts what state needs in place before the config names the account.
func prepareState(t *testing.T, hs *homeserver, dir string, a keptAccount, state sessionState) {
	t.Helper()
	place := testPlace(dir)
	keys := place.Keys(domain.AccountKey(a.user))
	switch state {
	case stateRejected:
		if err := session.Save(keys, domain.Session{Homeserver: hs.URL, UserID: a.user, DeviceID: "OLD", AccessToken: "revoked"}, true); err != nil {
			t.Fatal(err)
		}
	case stateUnreadable:
		if err := os.WriteFile(keys.File, []byte("this is not a session {{{"), 0o600); err != nil {
			t.Fatal(err)
		}
	case stateNoCrypto:
		token := "valid-" + a.name
		hs.mu.Lock()
		hs.issued[token] = a.user
		hs.mu.Unlock()
		if err := session.Save(keys, domain.Session{Homeserver: hs.URL, UserID: a.user, DeviceID: "DEV", AccessToken: token}, true); err != nil {
			t.Fatal(err)
		}
		// A directory where the crypto store's file should be: it cannot open.
		if err := os.MkdirAll(place.CryptoPath(domain.AccountKey(a.user)), 0o700); err != nil {
			t.Fatal(err)
		}
	case stateIn, stateOut, stateRemoved:
	}
}

// settleState waits until the running adapter has the account in state.
func settleState(t *testing.T, ctx context.Context, m *Adapter, p *phases, a keptAccount, state sessionState) {
	t.Helper()
	switch state {
	case stateIn:
		if _, err := named(t, m, a.name).passwordLogin(ctx, "secret"); err != nil {
			t.Fatalf("log %s in: %v", a.name, err)
		}
		waitUntil(t, a.name+" logged in", func() bool { return named(t, m, a.name).loggedIn.Load() })
	case stateOut, stateRejected, stateUnreadable:
		waitUntil(t, a.name+" logged out", func() bool { return p.last(a.name) == domain.AccountLoggedOut })
	case stateNoCrypto:
		waitUntil(t, a.name+" failed", func() bool { return p.last(a.name) == domain.AccountFailed })
	case stateRemoved:
	}
}

// Every configured account's cache is listed and read the same whatever its session
// does: logged in, logged out, a session the homeserver rejects or that cannot be read,
// a crypto store that will not open (encryption off, its encrypted rooms' keys never
// loaded). An account the config no longer names, and one it never named, show nothing;
// nothing live reaches an account without a session.
func TestEveryConfiguredAccountsCacheIsListedWhateverItsSession(t *testing.T) {
	noKeyring()
	hs := newHomeserver(t)
	me, you, gone := newKeptAccount("me"), newKeptAccount("you"), newKeptAccount("gone")
	for _, meState := range sessionStates {
		for _, youState := range sessionStates {
			t.Run(string(meState)+", "+string(youState), func(t *testing.T) {
				checkKept(t, hs, []keptAccount{me, you}, map[keptAccount]sessionState{me: meState, you: youState}, gone)
			})
		}
	}
}

func checkKept(t *testing.T, hs *homeserver, accounts []keptAccount, states map[keptAccount]sessionState, gone keptAccount) {
	t.Helper()
	m, p, dir := freshAdapter(t)
	whatsapp := domain.RoomID(domain.NativeID(domain.ProtocolWhatsApp, "44881234567", "120363@g.us"))
	if err := m.cache.SaveRooms(context.Background(), domain.AccountRooms(domain.ProtocolWhatsApp, "44881234567"), []domain.Room{{ID: whatsapp}}); err != nil {
		t.Fatal(err)
	}
	seedKept(t, m, gone)
	var users []string
	for _, a := range accounts {
		seedKept(t, m, a)
		prepareState(t, hs, dir, a, states[a])
		if states[a] != stateRemoved {
			users = append(users, a.user)
		}
	}
	m.UseConfig(t.Context(), accountsConfig(hs, users...))
	ctx := started(t, m)
	for _, a := range accounts {
		settleState(t, ctx, m, p, a, states[a])
	}

	// What a logged-in baseline lists: every configured account's room and space, and
	// an account space for each; never the removed or never-configured account's.
	var wantRooms []domain.RoomID
	var wantSpaces []domain.SpaceID
	anyIn := false
	for _, a := range accounts {
		if states[a] == stateRemoved {
			continue
		}
		wantRooms = append(wantRooms, a.room)
		wantSpaces = append(wantSpaces, a.space, a.ids.accountSpace())
		anyIn = anyIn || hasSession(states[a])
	}
	slices.Sort(wantRooms)
	slices.Sort(wantSpaces)

	rooms, err := m.Rooms(ctx)
	if got := roomIDs(rooms); err != nil || !slices.Equal(got, wantRooms) {
		t.Errorf("Rooms = (%v, %v), want %v", got, err, wantRooms)
	}
	unread, err := m.CachedUnread(ctx)
	if got := unreadIDs(unread); err != nil || !slices.Equal(got, wantRooms) {
		t.Errorf("CachedUnread = (%v, %v), want %v", got, err, wantRooms)
	}
	spaces, err := m.Spaces(ctx)
	if got := spaceIDs(spaces); err != nil || !slices.Equal(got, wantSpaces) {
		t.Errorf("Spaces = (%v, %v), want %v", got, err, wantSpaces)
	}
	if !anyIn { // a logged-in account refreshes from its homeserver, which this one is not
		refreshed, err := m.RefreshRooms(ctx)
		if got := roomIDs(refreshed); err != nil || !slices.Equal(got, wantRooms) {
			t.Errorf("RefreshRooms = (%v, %v), want the cached %v", got, err, wantRooms)
		}
		refreshedSpaces, err := m.RefreshSpaces(ctx)
		if got := spaceIDs(refreshedSpaces); err != nil || !slices.Equal(got, wantSpaces) {
			t.Errorf("RefreshSpaces = (%v, %v), want the cached %v", got, err, wantSpaces)
		}
	}

	mine := m.Me()
	for _, a := range append(accounts, gone) {
		configured := states[a] != stateRemoved && a != gone
		if slices.Contains(mine, a.ids.user(configuredUser(a.user))) != configured {
			t.Errorf("Me = %v; %s in it: want %v", mine, a.user, configured)
		}
		if slices.Contains(mine, "") {
			t.Errorf("Me = %v holds an empty ID", mine)
		}
	}

	for _, a := range accounts {
		state := states[a]
		if hasSession(state) {
			continue
		}
		encrypted, err := m.RoomEncryption(ctx, []domain.RoomID{a.room})
		_, timelineErr := m.Timeline(ctx, a.room, "", 10)
		if state == stateRemoved {
			if !errors.Is(err, api.ErrNotOnNetwork) || !errors.Is(timelineErr, api.ErrNotOnNetwork) {
				t.Errorf("%s (%s): RoomEncryption = %v, Timeline = %v; want both ErrNotOnNetwork", a.name, state, err, timelineErr)
			}
			continue
		}
		if err != nil || !encrypted[a.room] {
			t.Errorf("%s (%s): RoomEncryption = (%v, %v), want encrypted (unknown), no error", a.name, state, encrypted, err)
		}
		if !errors.Is(timelineErr, api.ErrNetworkOff) {
			t.Errorf("%s (%s): Timeline = %v, want ErrNetworkOff: nothing live reaches it", a.name, state, timelineErr)
		}
	}
}

// hasSession reports whether an account in state has a session live calls may use.
func hasSession(state sessionState) bool { return state == stateIn || state == stateNoCrypto }

func roomIDs(rooms []domain.Room) []domain.RoomID {
	out := make([]domain.RoomID, 0, len(rooms))
	for i := range rooms {
		out = append(out, rooms[i].ID)
	}
	slices.Sort(out)
	return out
}

func unreadIDs(unread []domain.Unread) []domain.RoomID {
	out := make([]domain.RoomID, 0, len(unread))
	for _, u := range unread {
		out = append(out, u.RoomID)
	}
	slices.Sort(out)
	return out
}

func spaceIDs(spaces []domain.Space) []domain.SpaceID {
	out := make([]domain.SpaceID, 0, len(spaces))
	for _, s := range spaces {
		out = append(out, s.ID)
	}
	slices.Sort(out)
	return out
}
