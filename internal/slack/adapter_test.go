package slack

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
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
	seen map[string]Session
	said map[string]string
}

func (s *sessions) hear(account Account, state Session, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[account.Name], s.said[account.Name] = state, detail
}

func (s *sessions) of(name string) (Session, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[name], s.said[name]
}

// Starting says of each account whether it is signed in: one with credentials is
// connecting, one without is told how to sign in, and one whose credentials cannot be
// read says so.
func TestStartSaysWhoIsSignedIn(t *testing.T) {
	t.Parallel()
	secrets := &memSecrets{values: map[string]string{}}
	signedIn(t, secrets, "work")
	secrets.values[credentialsRef("broken")] = "{not json"
	heard := &sessions{seen: map[string]Session{}, said: map[string]string{}}
	a := New(nil, secrets, []Account{{Name: "work", Workspace: "acme"}, {Name: "club", Workspace: "chess"}, {Name: "broken", Workspace: "x"}}, nil)
	a.OnSession(heard.hear)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()

	waitFor(t, func() bool { s, _ := heard.of("broken"); return s != 0 })
	if s, _ := heard.of("work"); s != Connecting {
		t.Errorf("work = %v, want Connecting", s)
	}
	if s, said := heard.of("club"); s != SignedOut || !strings.Contains(said, "kith login slack club") {
		t.Errorf("club = %v %q, want SignedOut and how to sign in", s, said)
	}
	if s, said := heard.of("broken"); s != SignedOut || !strings.Contains(said, "could not be read") {
		t.Errorf("broken = %v %q, want SignedOut, saying why", s, said)
	}

	// An account added to the config later is announced too.
	a.UseAccounts([]Account{{Name: "work", Workspace: "acme"}, {Name: "new", Workspace: "fresh"}})
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
// rest is empty.
func TestNothingIsReachableBeforeAWorkspaceConnects(t *testing.T) {
	t.Parallel()
	a := New(nil, &memSecrets{values: map[string]string{}}, nil, nil)
	ctx := t.Context()
	room := domain.RoomID("slack:T1/C1")
	for name, err := range map[string]error{
		"send":     a.Send(ctx, room, domain.Draft{Body: "hi"}),
		"mark":     a.MarkRead(ctx, room, "", false),
		"react":    a.SendReaction(ctx, room, "slack:T1/C1/1.2", "👍"),
		"typing":   a.SendTyping(ctx, room, true, time.Second),
		"redact":   a.Redact(ctx, room, "slack:T1/C1/1.2", ""),
		"file":     a.SendFile(ctx, room, "/tmp/x", ""),
		"unread":   a.MarkRoomUnread(ctx, room, true),
		"star":     a.StarMessage(ctx, room, "slack:T1/C1/1.2", true),
		"spam":     a.MarkSpam(ctx, domain.SpamVerdict{Room: room}),
		"timeline": func() error { _, err := a.Timeline(ctx, room, "", 10); return err }(),
		"fetch":    func() error { _, err := a.FetchEvent(ctx, room, "slack:T1/C1/1.2"); return err }(),
		"image":    func() error { _, err := a.LoadImage(ctx, room, "slack:T1/C1/1.2"); return err }(),
		"history":  func() error { _, _, err := a.MessageHistory(ctx, room, "slack:T1/C1/1.2"); return err }(),
		"members":  func() error { _, err := a.RefreshMembers(ctx, room); return err }(),
		"read all": func() error { _, err := a.MarkRoomsRead(ctx, []domain.RoomID{room}, false); return err }(),
	} {
		if !errors.Is(err, api.ErrNetworkOff) {
			t.Errorf("%s = %v, want ErrNetworkOff", name, err)
		}
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
