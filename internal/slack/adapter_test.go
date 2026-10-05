package slack

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memSecrets is a secret store in memory; fail makes every read fail.
type memSecrets struct {
	mu     sync.Mutex
	values map[string]string
	fail   error
}

func (m *memSecrets) Secret(ref string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return "", false, m.fail
	}
	v, ok := m.values[ref]
	return v, ok, nil
}

func (m *memSecrets) StoreSecret(ref, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[ref] = value
	return nil
}

func (m *memSecrets) DeleteSecret(ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, ref)
	return nil
}

func signedIn(t *testing.T, secrets *memSecrets, account string) {
	t.Helper()
	blob, err := json.Marshal(Credentials{Team: "T0000000001", User: "U0000000002", Token: "xoxc-test", Cookie: "xoxd-test"})
	if err != nil {
		t.Fatal(err)
	}
	secrets.values[credentialsRef(account)] = string(blob)
}

// sessions records each account's reported state.
type sessions struct {
	mu   sync.Mutex
	seen map[string]domain.AccountPhase
	said map[string]string
}

func (s *sessions) hear(status domain.AccountStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[status.Account], s.said[status.Account] = status.Phase, status.Detail
}

func (s *sessions) of(name string) (domain.AccountPhase, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[name], s.said[name]
}

// Starting says of an account that is not signed in how to sign in, and of one whose
// credentials cannot be used, why. (One signed in is connected: that reaches Slack.)
func TestStartSaysWhoIsSignedOut(t *testing.T) {
	t.Parallel()
	secrets := &memSecrets{values: map[string]string{}}
	secrets.values[credentialsRef("broken")] = "{not json"
	heard := &sessions{seen: map[string]domain.AccountPhase{}, said: map[string]string{}}
	a := New(nil, secrets, []Account{{Name: "club", Workspace: "chess"}, {Name: "broken", Workspace: "x"}}, nil)
	a.OnStatus(heard.hear)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()

	waitFor(t, func() bool { s, _ := heard.of("broken"); return s != 0 })
	if s, said := heard.of("club"); s != SignedOut || !strings.Contains(said, "kith login slack club") {
		t.Errorf("club = %v %q, want SignedOut and how to sign in", s, said)
	}
	if s, said := heard.of("broken"); s != SignedOut || !strings.Contains(said, "could not be read") {
		t.Errorf("broken = %v %q, want SignedOut, saying why", s, said)
	}

	// An account added to the config later is announced too.
	a.useAccounts([]Account{{Name: "club", Workspace: "chess"}, {Name: "new", Workspace: "fresh"}})
	if s, _ := heard.of("new"); s != SignedOut {
		t.Errorf("an added account = %v, want SignedOut", s)
	}
	if err := a.Start(ctx); err == nil {
		t.Error("a second Start was accepted")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Start returned %v", err)
	}
	a.Stop()
	a.Stop() // twice is harmless
	if _, open := <-a.Messages(); open {
		t.Error("the streams stay open after Stop")
	}
}

// SignedIn is the accounts with usable credentials: one with unusable ones is not, and
// does not hide the others; a store that cannot be read is an error.
func TestSignedInIsTheAccountsWithUsableCredentials(t *testing.T) {
	t.Parallel()
	secrets := &memSecrets{values: map[string]string{}}
	signedIn(t, secrets, "work")
	secrets.values[credentialsRef("broken")] = "{not json"
	a := New(nil, secrets, []Account{{Name: "work"}, {Name: "club"}, {Name: "broken"}}, nil)
	if signed, err := a.SavedSessions(t.Context()); err != nil || !slices.Equal(signed, []string{"work"}) {
		t.Errorf("SavedSessions = %v, %v; want work alone", signed, err)
	}
	secrets.fail = errors.New("locked")
	if _, err := a.SavedSessions(t.Context()); err == nil {
		t.Error("SavedSessions hid an unreadable store")
	}
}

// Credentials missing a part, or a store that cannot be read, are not a session.
func TestCredentialsMustBeWhole(t *testing.T) {
	t.Parallel()
	secrets := &memSecrets{values: map[string]string{}}
	blob, err := json.Marshal(Credentials{Team: "T1", User: "U1", Token: "xoxc-x"}) // no cookie
	if err != nil {
		t.Fatal(err)
	}
	secrets.values[credentialsRef("work")] = string(blob)
	if _, ok, err := loadCredentials(secrets, "work"); ok || !errors.Is(err, errUnusable) {
		t.Errorf("credentials without a cookie = %v, %v; want them unusable", ok, err)
	}
	secrets.fail = errors.New("locked")
	if _, ok, err := loadCredentials(secrets, "work"); ok || err == nil || errors.Is(err, errUnusable) {
		t.Errorf("an unreadable store = %v, %v; want the store's error", ok, err)
	}
}

// Until a workspace is connected, what needs one says the network is off, and the
// rest is empty: members are the cached ones, and a typing notice is a courtesy.
func TestNothingIsReachableBeforeAWorkspaceConnects(t *testing.T) {
	t.Parallel()
	a := New(nil, &memSecrets{values: map[string]string{}}, nil, nil)
	ctx := t.Context()
	room := domain.RoomID("slack:T1/C1")
	for name, err := range map[string]error{
		"send":     a.Send(ctx, room, domain.Draft{Body: "hi"}),
		"mark":     a.MarkRead(ctx, room, "", false),
		"react":    a.SendReaction(ctx, room, "slack:T1/C1/1.2", "👍"),
		"redact":   a.Redact(ctx, room, "slack:T1/C1/1.2", ""),
		"file":     a.SendFile(ctx, room, "/tmp/x", ""),
		"timeline": func() error { _, err := a.Timeline(ctx, room, "", 10); return err }(),
		"fetch":    func() error { _, err := a.FetchEvent(ctx, room, "slack:T1/C1/1.2"); return err }(),
		"image":    func() error { _, err := a.LoadImage(ctx, room, "slack:T1/C1/1.2"); return err }(),
	} {
		if !errors.Is(err, api.ErrNetworkOff) {
			t.Errorf("%s = %v, want ErrNetworkOff", name, err)
		}
	}
	// What the cache answers needs no workspace: nothing to mark here, no versions kept.
	if res, err := a.MarkRoomsRead(ctx, []domain.RoomID{room}, false); err != nil || res.Skipped != 1 {
		t.Errorf("MarkRoomsRead offline = %+v, %v; want it skipped", res, err)
	}
	if revs, _, err := a.MessageHistory(ctx, room, "slack:T1/C1/1.2"); err != nil || len(revs) != 0 {
		t.Errorf("MessageHistory offline = %v, %v", revs, err)
	}
	if err := a.MarkRoomUnread(ctx, room, true); !errors.Is(err, errNoMarkUnread) {
		t.Errorf("MarkRoomUnread = %v, want it said to be unsupported", err)
	}
	if err := a.SendTyping(ctx, room, true, time.Second); err != nil {
		t.Errorf("typing = %v, want nothing said", err)
	}
	if members, err := a.RefreshMembers(ctx, room); err != nil || len(members) != 0 {
		t.Errorf("RefreshMembers = %v, %v; want the cached ones, none", members, err)
	}
	if enc, err := a.RoomEncryption(ctx, []domain.RoomID{room}); err != nil || enc[room] {
		t.Errorf("RoomEncryption = %v, %v; Slack is not end-to-end encrypted", enc, err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// UseConfig reads [[slack.account]], each workspace as Slack writes it, and
// [display.deleted] keep.
func TestUseConfigReadsTheSlackSection(t *testing.T) {
	t.Parallel()
	a := New(nil, &memSecrets{values: map[string]string{}}, nil, nil)
	cfg := config.Config{Slack: config.Slack{Accounts: []config.SlackAccount{{Name: "work", Workspace: "https://Acme.slack.com/"}}}}
	cfg.Display.Deleted.KeepDeleted = true
	a.UseConfig(t.Context(), cfg)
	if got := a.accountsNow(); len(got) != 1 || got[0] != (Account{Name: "work", Workspace: "acme"}) {
		t.Errorf("accounts = %+v, want work at acme", got)
	}
	if !a.keepsDeleted() {
		t.Error("[display.deleted] keep not taken")
	}
}

// A new workspace is asked for until it is one (a web-client link is taken as its
// ID), named ("work" for an ID, which says nothing), and written into the config;
// then the session is asked for, a half pasted in the wrong field asked again.
func TestANewWorkspaceIsSetUpThenItsSessionAsked(t *testing.T) {
	t.Parallel()
	a := New(nil, &memSecrets{values: map[string]string{}}, []Account{{Name: "club", Workspace: "chess"}}, nil)
	talk := &apitest.Talk{Answers: map[string][]string{
		"workspace": {"not a workspace!", "chess", "https://app.slack.com/client/T0123456789/C1"},
		"name":      {""},
		"token":     {"xoxd-pasted-here"},
		"cookie":    {"xoxd-cookie"},
	}}
	talk.OnConfigure = func(r api.LoginRecord) error {
		a.useAccounts([]Account{{Name: "club", Workspace: "chess"}, {Name: r.Values["name"], Workspace: r.Values["workspace"]}})
		return nil
	}
	if _, err := a.Login(t.Context(), "", talk); !errors.Is(err, apitest.ErrNoAnswer) {
		t.Fatalf("login = %v, want it to reach the session's second ask", err)
	}
	if len(talk.Written) != 1 || !maps.Equal(talk.Written[0].Values, map[string]string{"name": "work", "workspace": "T0123456789"}) {
		t.Errorf("written %+v, want work in T0123456789", talk.Written)
	}
	notes := talk.Notes
	if len(notes) != 3 || !strings.Contains(notes[1], "club") || !strings.Contains(notes[2], "xoxc-") {
		t.Errorf("notes %q, want a bad workspace, club's, and the token's prefix", notes)
	}
}
