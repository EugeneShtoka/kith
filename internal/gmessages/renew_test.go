package gmessages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
)

// googleFunc answers a request as Google would.
type googleFunc func(*http.Request) (*http.Response, error)

func (f googleFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// answer is a reply with status and the Set-Cookie lines given.
func answer(status int, setCookies []string) *http.Response {
	header := http.Header{}
	for _, line := range setCookies {
		header.Add("Set-Cookie", line)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(`[]`))}
}

// renewPool is the cookies a renewal's histories draw from: Google's own and one kith
// holds for messages.google.com alone.
var renewPool = []string{"SID", "SIDCC", "NID", "__Secure-1PSIDTS", "__Secure-3PSIDTS", "__Secure-1PSIDCC", hostOnly}

// A renewal asks as a browser does, with every held cookie but messages.google.com's
// own, and merges in what Google sets for all of google.com: set or expired, whatever
// libgm wrote meanwhile kept, accounts.google.com's own cookies left out, and nothing
// at all taken from a refusal.
func TestARenewalMergesWhatGoogleSetsForGoogleCom(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(10, 29))
	for i := range 300 {
		held := map[string]string{renewNeeds: "psid", hostOnly: "osid"}
		for _, name := range renewPool {
			if rng.IntN(2) == 0 {
				held[name] = name + "-held"
			}
		}
		auth := libgm.NewAuthData()
		auth.SetCookies(maps.Clone(held))

		// libgm takes cookies from its own responses while the renewal is out.
		libgmWrote := maps.Clone(held)
		var libgmWrites []func(map[string]string)
		for range rng.IntN(3) {
			name := renewPool[rng.IntN(len(renewPool))]
			if rng.IntN(3) == 0 {
				libgmWrites = append(libgmWrites, func(m map[string]string) { delete(m, name) })
			} else {
				value := fmt.Sprintf("%s-libgm%d", name, rng.IntN(100))
				libgmWrites = append(libgmWrites, func(m map[string]string) { m[name] = value })
			}
		}
		for _, write := range libgmWrites {
			write(libgmWrote)
		}

		status := http.StatusOK
		if rng.IntN(6) == 0 {
			status = []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError}[rng.IntN(3)]
		}
		want := maps.Clone(libgmWrote)
		var setCookies, wantNames []string
		for _, name := range renewPool {
			value := name + "-google"
			switch rng.IntN(6) {
			case 0:
				setCookies = append(setCookies, setCookie(name, value, "; Domain=.google.com"))
			case 1:
				setCookies = append(setCookies, setCookie(name, value, "; Domain=google.com"))
			case 2:
				setCookies = append(setCookies, setCookie(name, "", "; Domain=.google.com; Max-Age=0"))
			case 3:
				setCookies = append(setCookies, setCookie(name, "", "; Domain=.google.com; Expires=Thu, 01 Jan 1970 00:00:01 GMT"))
			case 4:
				setCookies = append(setCookies, setCookie(name, value, "")) // accounts.google.com's own
				continue
			default:
				continue
			}
			wantNames = append(wantNames, name)
			if strings.HasSuffix(setCookies[len(setCookies)-1], "GMT") || strings.Contains(setCookies[len(setCookies)-1], "Max-Age=0") {
				delete(want, name)
			} else {
				want[name] = value
			}
		}
		slices.Sort(wantNames)
		if status != http.StatusOK {
			want, wantNames = libgmWrote, nil
		}

		var sent map[string]string
		web := &http.Client{Transport: googleFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			if req.Method != http.MethodPost || req.URL.String() != renewURL || string(body) != renewBody ||
				req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Origin") != "https://accounts.google.com" {
				t.Errorf("%d: asked %s %s %q (%v), not as Google's pages do", i, req.Method, req.URL, body, req.Header)
			}
			sent = map[string]string{}
			for _, cookie := range req.Cookies() {
				sent[cookie.Name] = cookie.Value
			}
			auth.CookiesLock.Lock()
			for _, write := range libgmWrites {
				write(auth.Cookies)
			}
			auth.CookiesLock.Unlock()
			return answer(status, setCookies), nil
		})}

		names, err := renew(context.Background(), web, auth)
		if (err != nil) != (status != http.StatusOK) || (err != nil && !errors.Is(err, errNotRenewed)) {
			t.Fatalf("%d: http %d renewed with err %v", i, status, err)
		}
		wantSent := maps.Clone(held)
		delete(wantSent, hostOnly)
		if !maps.Equal(sent, wantSent) {
			t.Errorf("%d: sent cookies %v, want every held one but %s: %v", i, slices.Sorted(maps.Keys(sent)), hostOnly, slices.Sorted(maps.Keys(wantSent)))
		}
		if !maps.Equal(auth.Cookies, want) {
			t.Errorf("%d: after Set-Cookie %q (http %d) the cookies are\n %v\nwant\n %v", i, setCookies, status, auth.Cookies, want)
		}
		if !slices.Equal(names, wantNames) {
			t.Errorf("%d: renewed names %v, want %v", i, names, wantNames)
		}
	}
}

// setCookie is a Set-Cookie line for name; attrs adds Domain, Max-Age or Expires.
func setCookie(name, value, attrs string) string {
	return fmt.Sprintf("%s=%s; Path=/; Secure; HttpOnly%s", name, value, attrs)
}

// A pairing without Google account cookies (by QR code) asks Google nothing; a renewal
// cut off by the connection being let go changes no cookie.
func TestARenewalWithoutTheAccountOrCutOffChangesNothing(t *testing.T) {
	t.Parallel()

	var asked atomic.Int32
	reached := make(chan struct{}, 1)
	web := &http.Client{Transport: googleFunc(func(req *http.Request) (*http.Response, error) {
		asked.Add(1)
		reached <- struct{}{}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	for _, held := range []map[string]string{nil, {"SID": "sid", hostOnly: "osid"}} {
		auth := libgm.NewAuthData()
		if held != nil {
			auth.SetCookies(maps.Clone(held))
		}
		if _, err := renew(context.Background(), web, auth); !errors.Is(err, errNothingToRenew) {
			t.Errorf("cookies %v renewed with err %v, want nothing to renew", held, err)
		}
		if !maps.Equal(auth.Cookies, held) {
			t.Errorf("cookies %v became %v", held, auth.Cookies)
		}
	}
	if asked.Load() != 0 {
		t.Errorf("Google was asked %d times without the account's cookies", asked.Load())
	}

	held := map[string]string{renewNeeds: "psid", "SIDCC": "sidcc"}
	auth := libgm.NewAuthData()
	auth.SetCookies(maps.Clone(held))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-reached
		cancel()
	}()
	if _, err := renew(ctx, web, auth); !errors.Is(err, context.Canceled) {
		t.Errorf("cut-off renewal err %v, want canceled", err)
	}
	if !maps.Equal(auth.Cookies, held) {
		t.Errorf("a cut-off renewal changed the cookies to %v", auth.Cookies)
	}
}
