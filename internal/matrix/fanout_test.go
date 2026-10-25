package matrix

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// perAccount is the backend's methods only a runner uses: logging in, its lifecycle and
// its settings, which the adapter drives itself.
var perAccount = []string{
	"Account", "EnableEncryption", "Login", "LoginOAuth", "LoginSSO", "OpenCryptoStore", "Reachable",
	"Resume", "VerificationUnavailable",
}

// The adapter no longer is the backend: every call the backend serves must be one the
// adapter routes to an account, with the same signature, or the router (which asks for
// capabilities by interface) silently stops offering it.
func TestTheAdapterServesEveryCallOfTheBackend(t *testing.T) {
	t.Parallel()
	backend, adapter := reflect.TypeFor[*InProc](), reflect.TypeFor[*Adapter]()
	for want := range backend.Methods() {
		if slices.Contains(perAccount, want.Name) {
			continue
		}
		got, ok := adapter.MethodByName(want.Name)
		if !ok {
			t.Errorf("the adapter has no %s: the router would not offer it", want.Name)
			continue
		}
		if a, b := signature(got.Type), signature(want.Type); a != b {
			t.Errorf("%s is %s on the adapter, %s on the backend", want.Name, a, b)
		}
	}
}

// signature is a method type without its receiver.
func signature(f reflect.Type) string {
	in := slices.Collect(f.Ins())[1:]
	return reflect.FuncOf(in, slices.Collect(f.Outs()), f.IsVariadic()).String()
}

// A call goes to the account its ID is under; one naming an account not logged in, or
// none configured, is refused, never sent as another account.
func TestACallGoesToItsAccount(t *testing.T) {
	t.Parallel()
	hs := newHomeserver(t)
	m, _, _ := freshAdapter(t)
	m.UseConfig(t.Context(), accountsConfig(hs, "@me:x", "@work:x"))
	me, work := named(t, m, "me"), named(t, m, "work")
	me.loggedIn.Store(true)
	room := domain.RoomID(string(work.ids.owner()) + "!r:x")
	if _, err := m.runnerOf(string(room)); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("a room of work, logged out = %v, want refused as off", err)
	}
	work.loggedIn.Store(true)
	if r, err := m.runnerOf(string(room)); err != nil || r != work {
		t.Errorf("a room of work went to %v (%v), want work", r, err)
	}
	if r, err := m.runnerOf(mx + "!r:x"); err != nil || r != me {
		t.Errorf("a room of me went to %v (%v), want me", r, err)
	}
	for _, id := range []string{"matrix:fedcba9876543210/!r:x", "!r:x", "whatsapp:44/1@s.whatsapp.net"} {
		if _, err := m.runnerOf(id); !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("%s = %v, want refused: no account here", id, err)
		}
	}
}

// Lists are every logged-in account's, each room once, with a space per account
// holding its rooms; the cache's other accounts' rows are not repeated by each.
func TestListsAreEveryAccountsOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hs := newHomeserver(t)
	m, _, _ := freshAdapter(t)
	m.UseConfig(ctx, accountsConfig(hs, "@me:x", "@work:x"))
	me, work := named(t, m, "me"), named(t, m, "work")
	for _, r := range []*runner{me, work} {
		r.loggedIn.Store(true)
		owner := r.ids.owner()
		if err := m.cache.SaveRooms(ctx, owner, []domain.Room{{ID: domain.RoomID(string(owner) + "!club:x"), Name: "Club"}}); err != nil {
			t.Fatal(err)
		}
		if err := m.cache.SaveInvites(ctx, owner, []domain.Room{{ID: domain.RoomID(string(owner) + "!party:x"), Name: "Party"}}); err != nil {
			t.Fatal(err)
		}
	}
	rooms, err := m.Rooms(ctx)
	if err != nil || len(rooms) != 2 {
		t.Errorf("rooms = %v (%v), want the club as each account sees it", rooms, err)
	}
	invites, err := m.CachedInvites(ctx)
	if err != nil || len(invites) != 2 {
		t.Errorf("invites = %v (%v), want the party as each account is invited", invites, err)
	}
	spaces, err := m.Spaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*runner{me, work} {
		i := slices.IndexFunc(spaces, func(sp domain.Space) bool { return sp.ID == r.ids.accountSpace() })
		if i < 0 || spaces[i].Name != "Matrix "+r.account().Name || len(spaces[i].Children) != 1 ||
			!r.ids.owner().Owns(spaces[i].Children[0]) || spaces[i].Leaving != domain.LeftBySigningOut {
			t.Errorf("%s's account space in %+v, want one holding its club, left by signing out", r.account().Name, spaces)
		}
	}
	if err := m.LeaveRoom(ctx, domain.RoomID(me.ids.accountSpace())); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("leaving an account's space = %v, want refused: it is signed out of", err)
	}
}

// Signing out ends the device on the homeserver, forgets the session, moves the crypto
// store aside and leaves the account waiting for a login, which then starts it as a new
// device; forget drops its rooms. The other account runs on.
func TestSigningAnAccountOut(t *testing.T) {
	noKeyring()
	ctx := t.Context()
	hs := newHomeserver(t)
	m, p, _ := freshAdapter(t)
	m.UseConfig(ctx, accountsConfig(hs, "@me:x", "@work:x"))
	runCtx := started(t, m)
	for _, name := range []string{"me", "work"} {
		if _, err := named(t, m, name).passwordLogin(runCtx, "secret"); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, "both logged in", func() bool { return len(m.live()) == 2 })
	me := named(t, m, "me")
	acc := me.account()
	room := domain.RoomID(string(me.ids.owner()) + "!club:x")
	if err := m.cache.SaveRooms(ctx, me.ids.owner(), []domain.Room{{ID: room, Name: "Club"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.SignOut(ctx, me.ids.accountSpace(), true); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := session.Load(acc.Crypto.Keys, true); found {
		t.Error("the session is still saved after signing out")
	}
	if _, err := os.Stat(acc.Crypto.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the crypto store is still in place (%v): a new device could not open it", err)
	}
	if aside, _ := filepath.Glob(acc.Crypto.Path + ".signed-out-*"); len(aside) == 0 {
		t.Error("the crypto store was deleted, not moved aside")
	}
	if rooms, _ := m.cache.Rooms(ctx); slices.ContainsFunc(rooms, func(r domain.Room) bool { return r.ID == room }) {
		t.Error("forget kept the account's rooms")
	}
	waitUntil(t, "me logged out", func() bool { return p.last("me") == domain.AccountLoggedOut })
	if len(m.live()) != 1 || !named(t, m, "work").loggedIn.Load() {
		t.Errorf("after me signed out, logged in: %d, want work alone", len(m.live()))
	}
	in, err := named(t, m, "me").passwordLogin(runCtx, "secret")
	if err != nil || !in.Started {
		t.Fatalf("logging in again = (%+v, %v), want started at once", in, err)
	}
	waitUntil(t, "me logged in again", func() bool { return len(m.live()) == 2 })
}

// Ignoring someone is a person's, not an account's: every logged-in account's list
// takes them, and the list read is every account's.
func TestIgnoringReachesEveryAccount(t *testing.T) {
	noKeyring()
	hs := newHomeserver(t)
	m, _, _ := freshAdapter(t)
	m.UseConfig(t.Context(), accountsConfig(hs, "@me:x", "@work:x"))
	ctx := started(t, m)
	for _, name := range []string{"me", "work"} {
		if _, err := named(t, m, name).passwordLogin(ctx, "secret"); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, "both logged in", func() bool { return len(m.live()) == 2 })
	if err := m.Ignore(ctx, "matrix:@spam:x"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"me", "work"} {
		if got, err := named(t, m, name).Ignored(ctx); err != nil || !slices.Equal(got, []string{"matrix:@spam:x"}) {
			t.Errorf("%s ignores %v (%v), want @spam:x", name, got, err)
		}
	}
	if err := named(t, m, "work").Ignore(ctx, "matrix:@bot:x"); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Ignored(ctx); err != nil || !slices.Equal(got, []string{"matrix:@bot:x", "matrix:@spam:x"}) {
		t.Errorf("ignored = %v (%v), want both accounts' as one list", got, err)
	}
}
