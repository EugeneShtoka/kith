package telegram

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/apitest"
)

// scanAfter has a phone logged in as user scan once n QR codes were shown; shown is
// every code, in turn.
func scanAfter(t *testing.T, f *fakeTelegram, talk *apitest.Talk, n int, user *tg.User) *[]string {
	t.Helper()
	var mu sync.Mutex
	var shown []string
	talk.OnShow = func(code string) {
		mu.Lock()
		defer mu.Unlock()
		shown = append(shown, code)
		if len(shown) == n {
			go f.scan(t, user) // after the client went back to waiting for it
		}
	}
	return &shown
}

// A login shows a QR code, a new one as each expires, each offering the code Telegram
// sends instead; once a phone of the account scans one, it is logged in without a
// code being sent.
func TestALoginShowsQRCodesUntilOneIsScanned(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.set(func(f *fakeTelegram) { f.tokenLife = 2 * time.Second })
	secrets := &memSecrets{values: map[string]string{}}
	talk := &apitest.Talk{}
	shown := scanAfter(t, f, talk, 2, f.user)
	a, heard, _, end, err := logInWith(t, f, secrets, talk)
	if err != nil || !strings.HasPrefix(end.Done, "logged in to Telegram as Dana Lee") {
		t.Fatalf("login = (%+v, %v)", end, err)
	}
	codes := *shown
	if len(codes) < 2 || codes[0] == codes[1] || !strings.HasPrefix(codes[0], "tg://login?token=") {
		t.Errorf("shown %q, want rotating tg://login codes", codes)
	}
	if len(talk.Offered) != len(codes) || talk.Offered[0] != insteadOfQR {
		t.Errorf("offered %q with %d codes, want the code Telegram sends with each", talk.Offered, len(codes))
	}
	f.mu.Lock()
	sent := f.sentCodes
	f.mu.Unlock()
	if sent != 0 || len(talk.Asked) != 0 {
		t.Errorf("sent %d codes and asked %v; a scan needs neither", sent, talk.Asked)
	}
	if creds, ok, err := loadCredentials(secrets, home.Digits); !ok || err != nil || creds.User != 42 {
		t.Fatalf("kept = (%+v, %v, %v), want user 42", creds, ok, err)
	}
	connected(t, heard, "home")
	if me := a.Me(); !slices.Equal(me, []string{"telegram:42"}) {
		t.Errorf("Me = %v", me)
	}
}

// An account with two-step verification is asked its password after the scan, again
// while wrong.
func TestAScannedLoginAsksTheTwoStepPassword(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.set(func(f *fakeTelegram) { f.tokenLife, f.password, f.rejectPasswords = 3*time.Second, true, 1 })
	secrets := &memSecrets{values: map[string]string{}}
	talk := &apitest.Talk{Answers: map[string][]string{"password": {"wrong", "right"}}}
	scanAfter(t, f, talk, 1, f.user)
	_, _, _, end, err := logInWith(t, f, secrets, talk)
	if err != nil || end.Done == "" {
		t.Fatalf("login = (%+v, %v)", end, err)
	}
	if want := []string{"password", "password"}; !slices.Equal(talk.Asked, want) {
		t.Errorf("asked %v, want %v", talk.Asked, want)
	}
	if want := []string{"the account has two-step verification", "that is not the password; try again"}; !slices.Equal(talk.Notes, want) {
		t.Errorf("notes %q, want %q", talk.Notes, want)
	}
	if _, ok, _ := loadCredentials(secrets, home.Digits); !ok {
		t.Error("nothing kept after the password")
	}
}

// Taking the other way leaves the QR code for the code Telegram sends to the number.
func TestTheCodeTelegramSendsCanBeTakenInstead(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	secrets := &memSecrets{values: map[string]string{}}
	_, _, talk, end, err := logIn(t, f, secrets, map[string][]string{"code": {"12345"}})
	if err != nil || end.Done == "" {
		t.Fatalf("login = (%+v, %v)", end, err)
	}
	f.mu.Lock()
	sent := f.sentCodes
	f.mu.Unlock()
	if sent != 1 || !slices.Equal(talk.Asked, []string{"code"}) || len(talk.Shown) != 1 {
		t.Errorf("sent %d codes, asked %v, shown %q; want one QR code, then one code sent and asked", sent, talk.Asked, talk.Shown)
	}
}

// A QR code scanned by a phone of another Telegram account ends that session and
// keeps nothing.
func TestAQRCodeScannedByAnotherAccountKeepsNothing(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.set(func(f *fakeTelegram) { f.tokenLife = 3 * time.Second })
	secrets := &memSecrets{values: map[string]string{}}
	talk := &apitest.Talk{}
	other := &tg.User{ID: 77, AccessHash: 2, FirstName: "Eli", Phone: "12025550100", Self: true}
	scanAfter(t, f, talk, 1, other)
	_, _, _, _, err := logInWith(t, f, secrets, talk)
	if !errors.Is(err, errOtherAccount) || !strings.Contains(err.Error(), "+12025550100") {
		t.Fatalf("login = %v, want the other account named", err)
	}
	f.mu.Lock()
	out := f.loggedOut
	f.mu.Unlock()
	if out != 1 {
		t.Errorf("logged out %d times, want the other account's new session ended", out)
	}
	if _, ok, _ := loadCredentials(secrets, home.Digits); ok {
		t.Error("another account's session was kept for home")
	}
}

// A login ended while its QR code shows keeps nothing.
func TestALoginEndedAtTheQRCodeKeepsNothing(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	secrets := &memSecrets{values: map[string]string{}}
	a, _ := started(t, secrets, home)
	ctx, cancel := context.WithCancel(t.Context())
	talk := &apitest.Talk{OnShow: func(string) { cancel() }}
	if _, err := a.login(ctx, home, testApp, talk, f.dial); err == nil {
		t.Fatal("a canceled login succeeded")
	}
	if _, ok, _ := loadCredentials(secrets, home.Digits); ok {
		t.Error("a canceled login kept a session")
	}
}
