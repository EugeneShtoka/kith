package matrix

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// accountBridges serves one bridge's provisioning API to several people: each one's
// connected logins, every login naming one number.
type accountBridges struct {
	mu     sync.Mutex
	logins map[string][]string // by user
}

// loginNumber is the number user's login names, and the name it gives it.
func loginNumber(user, login string) (number, name string) {
	n := map[string]int{"@me:x": 100, "@work:x": 200}[user] + int(login[1]-'0')
	return fmt.Sprintf("15550101%03d", n), user + " " + login
}

func (f *accountBridges) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user := r.URL.Query().Get("user_id")
	logins, known := f.logins[user]
	if r.Header.Get("Authorization") != "Bearer token" || !known {
		http.Error(w, `{"errcode":"M_UNKNOWN_TOKEN"}`, http.StatusUnauthorized)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/v3/whoami"):
		var out []map[string]any
		for _, login := range logins {
			out = append(out, map[string]any{"id": login, "state": map[string]any{"state_event": "CONNECTED"}})
		}
		reply(w, map[string]any{"logins": out})
	case strings.HasSuffix(r.URL.Path, "/v3/contacts"):
		number, name := loginNumber(user, r.URL.Query().Get("login_id"))
		reply(w, map[string]any{"contacts": []map[string]any{contact(name, "tel:+"+number)}})
	default:
		http.NotFound(w, r)
	}
}

func (f *accountBridges) connect(user string, logins []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins[user] = logins
}

// mirrorAccount is one Matrix account over the shared cache, and what its latest
// account data and bridge read said.
type mirrorAccount struct {
	b        *InProc
	user     string
	ignored  []string
	verdicts []domain.PersonVerdict
	names    map[string]string // number to name
}

// Under any interleaving of two Matrix accounts' ignore lists, person verdicts and
// bridge contact reads (a login gone, or the bridge no longer configured, expires its
// names), the cache holds exactly both accounts' latest: one account's write never
// erases the other's. Ignoring is read as one list, and of two verdicts on one person
// and tag the newest (a release, at the same time).
func TestTwoAccountsMirrorsKeepToThemselves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	people := []string{"matrix:@p0:x", "matrix:@p1:x", "matrix:@p2:x", "matrix:@p3:x"}
	logins := []string{"l0", "l1", "l2"}
	for seed := range uint64(30) {
		rng := rand.New(rand.NewPCG(seed, 6))
		fake := &accountBridges{logins: map[string][]string{}}
		srv := httptest.NewServer(fake)
		base := srv.URL + "/_matrix/provision/wa"
		cache := testCache(t)
		var accounts []*mirrorAccount
		for _, user := range []string{"@me:x", "@work:x"} {
			b := New(cache)
			b.useAccount(user)
			client, err := mautrix.NewClient(srv.URL, id.UserID(user), "token")
			if err != nil {
				t.Fatal(err)
			}
			b.client = client
			fake.connect(user, nil)
			accounts = append(accounts, &mirrorAccount{b: b, user: user, names: map[string]string{}})
		}
		var trail []string
		for range 14 {
			a := accounts[rng.IntN(len(accounts))]
			switch rng.IntN(3) {
			case 0:
				var set []string
				for _, p := range people {
					if rng.IntN(2) == 0 {
						set = append(set, p)
					}
				}
				a.b.mirrorIgnored(ctx, set)
				a.ignored = set
				trail = append(trail, fmt.Sprintf("%s ignores %v", a.user, set))
			case 1:
				var set []domain.PersonVerdict
				for _, p := range people[:3] {
					if rng.IntN(3) == 0 {
						continue
					}
					v := domain.PersonVerdict{Person: p, Tag: "spam", At: time.UnixMilli(int64(1 + rng.IntN(3)))}
					if v.Released = rng.IntN(2) == 0; !v.Released {
						v.Rule = domain.MoveAlways
					}
					set = append(set, v)
				}
				a.b.mirrorPeople(ctx, set)
				a.verdicts = set
				trail = append(trail, fmt.Sprintf("%s marks %v", a.user, set))
			default:
				var connected []string
				for _, l := range logins {
					if rng.IntN(2) == 0 {
						connected = append(connected, l)
					}
				}
				fake.connect(a.user, connected)
				bases := []string{base}
				if rng.IntN(4) == 0 {
					bases = nil // the bridge is no longer configured
				}
				a.b.readBridgeContacts(ctx, bases)
				a.names = map[string]string{}
				for _, l := range connected {
					if bases != nil {
						number, name := loginNumber(a.user, l)
						a.names[number] = name
					}
				}
				trail = append(trail, fmt.Sprintf("%s reads bridges %v with logins %v", a.user, bases, connected))
			}
			if msg := mirrorsDiffer(ctx, t, accounts); msg != "" {
				srv.Close()
				t.Fatalf("seed %d: %s after:\n  %s", seed, msg, strings.Join(trail, "\n  "))
			}
		}
		srv.Close()
	}
}

// mirrorsDiffer says how the cache differs from both accounts' latest, or "".
func mirrorsDiffer(ctx context.Context, t *testing.T, accounts []*mirrorAccount) string {
	t.Helper()
	cache := accounts[0].b.cache
	var ignored []string
	type key struct{ person, tag string }
	newest := map[key]domain.PersonVerdict{}
	names := map[string]string{}
	for _, a := range accounts {
		ignored = append(ignored, a.ignored...)
		for _, v := range a.verdicts {
			k := key{v.Person, v.Tag}
			if have, ok := newest[k]; !ok || v.At.After(have.At) || (v.At.Equal(have.At) && v.Released) {
				newest[k] = v
			}
		}
		maps.Copy(names, a.names)
	}
	slices.Sort(ignored)
	ignored = slices.Compact(ignored)
	got, err := cache.MatrixIgnored(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, ignored) {
		return fmt.Sprintf("ignored = %v, want %v", got, ignored)
	}
	verdicts, err := cache.PersonVerdicts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	held := map[key]domain.PersonVerdict{}
	for _, v := range verdicts {
		held[key{v.Person, v.Tag}] = v
	}
	same := func(a, b domain.PersonVerdict) bool { return a.At.Equal(b.At) && a.Released == b.Released }
	if len(verdicts) != len(held) || !maps.EqualFunc(held, newest, same) {
		return fmt.Sprintf("verdicts = %v, want %v", verdicts, newest)
	}
	dir, err := cache.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		for _, l := range []string{"l0", "l1", "l2"} {
			number, _ := loginNumber(a.user, l)
			if got, _ := dir.Named("+" + number); got != names[number] {
				return fmt.Sprintf("+%s is named %q, want %q", number, got, names[number])
			}
		}
	}
	return ""
}
