package main

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// An edit of the account's own message in a room that is sent to replaces it at once,
// and is recorded as an edit, which rests the room for no cooldown.
func TestAnEditReplacesTheirOwnMessage(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "edit": "$2", "text": "morning, all"})

	if out["action"] != agent.Edited {
		t.Fatalf("action = %v, want %q", out["action"], agent.Edited)
	}
	if len(f.sent) != 1 || f.sent[0].Edits != mx+"$2" || !strings.HasSuffix(f.sent[0].Body, "morning, all") {
		t.Fatalf("sent = %+v, want an edit of $2 with the new words", f.sent)
	}
	if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Edited ||
		len(agent.Outs(ledger, mx+"!open:x")) != 0 {
		t.Fatalf("ledger = %+v, want one edit that counts toward no cooldown", ledger)
	}
}

// Only the account's own message can be edited, and only one of the room named: what
// someone else said, or a message of another room, is refused and nothing is written.
func TestAnEditOfAnotherMessageIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, room, event, want string }{
		{"someone else's", "Standup", "$1", "own message"},
		{"another room's", "Private chat", "$2", "not a message in this room"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := twoRooms()
			s := newWriter(t, f, shareAllEncrypted, []string{"dm", "group"})
			s.write.Encrypted = true
			said := callErr(t, s, "send_message", map[string]any{"room": tc.room, "edit": tc.event, "text": "x"})
			if !strings.Contains(said, tc.want) {
				t.Errorf("refusal = %q, want it to say %q", said, tc.want)
			}
			if len(f.sent) != 0 || len(f.drafts) != 0 {
				t.Fatalf("a refused edit was written: sent=%+v drafts=%+v", f.sent, f.drafts)
			}
		})
	}
}

// A room the write scope refuses takes no edit, whatever `send` names.
func TestAnEditCannotOutrankTheWriteScope(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"group"})
	s.write = domain.ModelScope{Except: []string{"room:Standup"}}
	said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "edit": "$2", "text": "x"})
	if !strings.Contains(said, "[agent.write]") || len(f.sent) != 0 || len(f.drafts) != 0 {
		t.Fatalf("refusal %q, sent=%+v drafts=%+v: want a refusal naming [agent.write] and nothing written", said, f.sent, f.drafts)
	}
	if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Refused {
		t.Fatalf("ledger = %+v, want the refusal recorded", ledger)
	}
}

// In a room drafted into, the edit waits in an empty composer as an edit of that
// message; a composer holding their words is left alone.
func TestAnEditInADraftedRoomWaitsInTheComposer(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	s := newWriter(t, f, shareAll, nil)
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "edit": "$2", "text": "morning, all"})
	held := f.drafts[mx+"!open:x"]
	if out["action"] != agent.Drafted || held.Editing != mx+"$2" || held.Body != "morning, all" || len(f.sent) != 0 {
		t.Fatalf("action %v, draft %+v, sent %+v: want an edit of $2 waiting in the composer", out["action"], held, f.sent)
	}

	f = twoRooms()
	f.store(domain.StoredDraft{RoomID: mx + "!open:x", Body: "half a thought", Updated: time.Now()})
	s = newWriter(t, f, shareAll, nil)
	said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "edit": "$2", "text": "morning, all"})
	if held := f.drafts[mx+"!open:x"]; held.Body != "half a thought" || held.Editing != "" {
		t.Fatalf("their draft became %+v (%s), want it untouched", held, said)
	}
}

// An edit or a file takes only the arguments it can honor.
func TestEditAndFileRefuseWhatTheyCannotDo(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"edit": "$2", "text": "x", "reply_to": "$1"},
		{"edit": "$2", "text": "x", "at": "2099-01-01T00:00:00Z"},
		{"edit": "$2", "text": "x", "file": file},
		{"edit": "$2", "text": " "},
		{"file": file, "thread": "$1"},
		{"file": "notes.txt"},
		{"file": filepath.Dir(file)},
	} {
		f := twoRooms()
		s := newWriter(t, f, shareAll, []string{"group"})
		args["room"] = "Standup"
		callErr(t, s, "send_message", args)
		if len(f.sent) != 0 || len(f.filesSent) != 0 || len(f.drafts) != 0 {
			t.Fatalf("%v wrote something: sent=%+v files=%+v drafts=%+v", args, f.sent, f.filesSent, f.drafts)
		}
	}
}

// jpegWithComment is a JPEG whose comment segment holds secret: metadata a sent copy
// must not carry.
func jpegWithComment(secret string) []byte {
	out := []byte{0xFF, 0xD8, 0xFF, 0xFE, 0, byte(2 + len(secret))}
	out = append(out, secret...)
	return append(out, 0xFF, 0xDA, 0, 2, 1, 2, 3, 0xFF, 0xD9)
}

// A file goes out now to a room that is sent to, under a neutral name, a picture
// without its metadata, with text as its caption; the clean copy is removed after.
func TestAFileIsSentAnonymized(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "IMG_2041 Haifa.JPG")
	if err := os.WriteFile(path, jpegWithComment("at home"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "file": path, "text": "the view"})

	if out["action"] != agent.Sent || len(f.filesSent) != 1 {
		t.Fatalf("action %v, files %+v: want the file sent", out["action"], f.filesSent)
	}
	got := f.filesSent[0]
	if got.room != mx+"!open:x" || got.name != "photo.jpg" || got.caption != "the view" {
		t.Errorf("sent %q to %s captioned %q, want photo.jpg to Standup captioned \"the view\"", got.name, got.room, got.caption)
	}
	if strings.Contains(string(got.data), "at home") {
		t.Error("the picture went out with its metadata")
	}
	if _, err := os.Stat(got.path); got.path == path || !os.IsNotExist(err) {
		t.Errorf("the clean copy %s is still there (or the original was sent)", got.path)
	}
	if ledger := entries(t, s); len(ledger) != 1 || !strings.Contains(ledger[0].Text, path) {
		t.Fatalf("ledger = %+v, want the send recorded with the file's path", ledger)
	}
}

// A file that cannot go now waits in the daemon's queue as a message would: asked
// for later, inside the room's cooldown, or after a failed send. Only a room drafted
// into refuses it, recorded, since a composer cannot hold a file.
func TestAFileThatCannotGoNowIsQueued(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "IMG_2041 Haifa.JPG")
	if err := os.WriteFile(path, jpegWithComment("at home"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name string
		send []string
		args map[string]any
		// first sends a message before, failSend fails the send.
		first, failSend bool
		want            string
	}{
		{"drafted room", nil, nil, false, false, agent.Refused},
		{"asked for later", []string{"group"}, map[string]any{"at": at.Format(time.RFC3339)}, false, false, agent.Scheduled},
		{"resting room", []string{"group"}, nil, true, false, agent.Scheduled},
		{"failed send", []string{"group"}, nil, false, true, agent.Queued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := twoRooms()
			s := newWriter(t, f, shareAll, tc.send)
			if tc.first {
				call(t, s, "send_message", map[string]any{"room": "Standup", "text": "first"})
			}
			if tc.failSend {
				f.sendErr = errors.New("homeserver down")
			}
			args := map[string]any{"room": "Standup", "file": path, "text": "the view"}
			maps.Copy(args, tc.args)
			if tc.want == agent.Refused {
				callErr(t, s, "send_message", args)
			} else if out := call(t, s, "send_message", args); out["action"] != tc.want {
				t.Fatalf("action = %v, want %q", out["action"], tc.want)
			}
			ledger := entries(t, s)
			if last := ledger[len(ledger)-1]; last.Outcome != tc.want || !strings.Contains(last.Text, path) {
				t.Fatalf("ledger = %+v, want %s recorded with the file's path", ledger, tc.want)
			}
			if len(f.filesSent) != 0 || len(f.drafts) != 0 {
				t.Fatalf("files %+v drafts %+v: want nothing sent or drafted", f.filesSent, f.drafts)
			}
			if tc.want == agent.Refused {
				if len(f.queued) != 0 {
					t.Fatalf("queued %+v in a room drafted into", f.queued)
				}
				return
			}
			got := f.queued[len(f.queued)-1]
			if got.FileName != "photo.jpg" || got.Body != "the view" || got.KeptFile == "" ||
				strings.Contains(got.KeptFile, "at home") {
				t.Fatalf("queued %+v: want the picture without its metadata, as photo.jpg captioned \"the view\"", got)
			}
			if tc.args != nil && !got.At.Equal(at) {
				t.Errorf("queued for %v, want %v", got.At, at)
			}
		})
	}
}
