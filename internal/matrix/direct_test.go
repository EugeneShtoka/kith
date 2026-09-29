package matrix

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Creating a DM must record it in our own m.direct (is_direct only hints the invitee).
func TestCreatingADirectMessageRecordsItInAccountData(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		created map[string]any
		written map[string][]string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/createRoom"):
			_ = json.Unmarshal(body, &created)
			_, _ = w.Write([]byte(`{"room_id":"!made:x"}`))
		case strings.Contains(r.URL.Path, "/account_data/m.direct"):
			if r.Method == http.MethodPut {
				_ = json.Unmarshal(body, &written)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			// The existing DM must survive the read-modify-write.
			_, _ = w.Write([]byte(`{"@old:x":["!existing:x"]}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b := backendOn(t, srv)
	roomID, err := b.CreateRoom(context.Background(), domain.NewRoom{
		Encrypted: true, Direct: true, Invite: []string{"@dana:x"},
	})
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if roomID != "!made:x" {
		t.Fatalf("room ID = %q, want !made:x", roomID)
	}

	mu.Lock()
	defer mu.Unlock()
	if created["is_direct"] != true {
		t.Errorf("is_direct = %v, want true — the invited client files the room by it", created["is_direct"])
	}
	if got := created["preset"]; got != "trusted_private_chat" {
		t.Errorf("preset = %v, want trusted_private_chat so the other person is an equal", got)
	}
	invited, _ := created["invite"].([]any)
	if len(invited) != 1 || invited[0] != "@dana:x" {
		t.Errorf("invite = %v, want [@dana:x] — a DM with nobody in it is not a DM", created["invite"])
	}
	if got := written["@dana:x"]; len(got) != 1 || got[0] != "!made:x" {
		t.Errorf("m.direct[@dana:x] = %v, want [!made:x]", got)
	}
	if got := written["@old:x"]; len(got) != 1 || got[0] != "!existing:x" {
		t.Errorf("m.direct[@old:x] = %v — the existing DM was dropped by the write", got)
	}
}

// An ordinary room gets no is_direct and no account-data write.
func TestCreatingAnOrdinaryRoomIsUnchanged(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		created map[string]any
		wrote   bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/createRoom"):
			_ = json.Unmarshal(body, &created)
			_, _ = w.Write([]byte(`{"room_id":"!made:x"}`))
		case strings.Contains(r.URL.Path, "/account_data/m.direct"):
			wrote = wrote || r.Method == http.MethodPut
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	if _, err := backendOn(t, srv).CreateRoom(context.Background(), domain.NewRoom{Name: "Beer"}); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if created["is_direct"] == true {
		t.Error("an ordinary room was created as a direct message")
	}
	if got := created["preset"]; got != "private_chat" {
		t.Errorf("preset = %v, want private_chat", got)
	}
	if wrote {
		t.Error("an ordinary room was recorded in m.direct")
	}
}

// A failed read of m.direct must stop the write, or every other DM is un-marked.
func TestAFailedReadOfDirectChatsDoesNotOverwriteThem(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		wrote bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/createRoom"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"room_id":"!made:x"}`))
		case strings.Contains(r.URL.Path, "/account_data/m.direct"):
			if r.Method == http.MethodPut {
				wrote = true
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
				return
			}
			http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	roomID, err := backendOn(t, srv).CreateRoom(context.Background(), domain.NewRoom{
		Direct: true, Invite: []string{"@dana:x"},
	})
	// The room exists; the caller gets its ID and an error.
	if roomID != "!made:x" {
		t.Errorf("room ID = %q, want the room that was made", roomID)
	}
	if err == nil {
		t.Error("a failed m.direct write was reported as success")
	}
	mu.Lock()
	defer mu.Unlock()
	if wrote {
		t.Error("m.direct was overwritten from a read that failed")
	}
}

// Candidates exclude people we already have a DM with (per m.direct).
func TestDirectCandidatesLeaveOutTheDMsYouAlreadyHave(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/account_data/m.direct") {
			_, _ = w.Write([]byte(`{"@dana:x":["!dm:x"]}`))
			return
		}
		http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()

	b, ctx := backendOn(t, srv), context.Background()
	seedRoom(t, b, "!r:x")
	for _, who := range []struct{ id, name string }{
		{"@dana:x", "Dana"}, {"@sam:x", "Sam"}, {"@me:x", "Me"},
	} {
		if err := b.cache.SaveMessages(ctx, "!r:x", []domain.Message{{
			ID: domain.EventID("$" + who.id), RoomID: "!r:x",
			Sender: who.id, SenderName: who.name, Body: "hi", Timestamp: at(1),
		}}); err != nil {
			t.Fatalf("SaveMessages: %v", err)
		}
	}

	people, err := b.DirectCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("DirectCandidates: %v", err)
	}
	got := make([]string, 0, len(people))
	for _, person := range people {
		got = append(got, person.UserID)
	}
	if len(got) != 1 || got[0] != "@sam:x" {
		t.Errorf("candidates = %v, want only @sam:x — Dana has a DM and @me:x is us", got)
	}
}
