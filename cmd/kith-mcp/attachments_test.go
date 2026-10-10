package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// withFiles is two rooms whose open one holds a picture, a document, a picture of a
// type an assistant does not take, and a deleted file; the private one holds a picture.
func withFiles() *fake {
	f := twoRooms()
	at := time.Unix(1_700_000_100, 0)
	f.messages["!open:x"] = append(f.messages["!open:x"],
		domain.Message{ID: "$pic", Sender: "@dana:x", Body: "cat.png", Timestamp: at, Media: &domain.Media{Type: domain.MediaImage, Name: "cat.png", Mime: "image/png", Size: 4}},
		domain.Message{ID: "$doc", Sender: "@dana:x", Body: "plan.pdf", Timestamp: at, Media: &domain.Media{Type: domain.MediaFile, Name: "../plan.pdf", Mime: "application/pdf"}},
		domain.Message{ID: "$heic", Sender: "@dana:x", Body: "x.heic", Timestamp: at, Media: &domain.Media{Type: domain.MediaImage, Name: "x.heic", Mime: "image/heic"}},
		domain.Message{ID: "$gone", Sender: "@dana:x", Redacted: true, Timestamp: at, Media: &domain.Media{Type: domain.MediaFile, Name: "old.txt"}},
	)
	f.messages["!secret:x"] = []domain.Message{{ID: "$private", Sender: "@dana:x", Timestamp: at, Media: &domain.Media{Type: domain.MediaImage, Mime: "image/png"}}}
	f.files = map[domain.EventID][]byte{"$pic": []byte("PNG!"), "$doc": []byte("%PDF-1.7"), "$heic": []byte("heic"), "$private": []byte("PNG!")}
	return f
}

// A message with a file says what it is; a picture comes back as an image to look at,
// any other file is written under the files directory (its name kept to the
// directory) and named by its path; a deleted file is not fetched.
func TestAnAttachmentIsHandedOver(t *testing.T) {
	t.Parallel()
	f := withFiles()
	s := newServer(f, shareAll)
	s.files = t.TempDir()

	room := call(t, s, "read_room", map[string]any{"room": "Standup"})
	encoded, err := json.Marshal(room["messages"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"attachment":{"kind":"image","mime":"image/png","name":"cat.png","size_bytes":4}`) {
		t.Errorf("read_room does not say the picture is there: %s", encoded)
	}
	if strings.Contains(string(encoded), "old.txt") {
		t.Errorf("a deleted message still names its file: %s", encoded)
	}

	pic := raw(t, s, "get_attachment", map[string]any{"room": "Standup", "event": "$pic"})
	blocks, _ := pic["content"].([]any)
	img, _ := blocks[0].(map[string]any)
	if img["type"] != "image" || img["mimeType"] != "image/png" || img["data"] != base64.StdEncoding.EncodeToString([]byte("PNG!")) {
		t.Errorf("the picture came back as %+v", img)
	}

	for event, want := range map[string]string{"$doc": "%PDF-1.7", "$heic": "heic"} {
		got := call(t, s, "get_attachment", map[string]any{"room": "Standup", "event": event})
		path, _ := got["path"].(string)
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, []byte(want)) || !strings.HasPrefix(path, s.files+string(os.PathSeparator)) {
			t.Errorf("%s: path %q (%v), read %q", event, path, err, data)
		}
	}

	if msg := callErr(t, s, "get_attachment", map[string]any{"room": "Standup", "event": "$gone"}); !strings.Contains(msg, "no file") {
		t.Errorf("a deleted file: %q", msg)
	}
	if msg := callErr(t, s, "get_attachment", map[string]any{"room": "Standup", "event": "$1"}); !strings.Contains(msg, "no file") {
		t.Errorf("a message with no file: %q", msg)
	}
}

// A file is handed over only from a room the assistant may read, and not fetched at
// all from one it may not; one larger than the limit is refused before it is fetched.
func TestAnAttachmentKeepsToTheScope(t *testing.T) {
	t.Parallel()
	f := withFiles()
	s := newServer(f, domain.ModelScope{Only: []string{"space:Work"}})
	s.files = t.TempDir()
	callErr(t, s, "get_attachment", map[string]any{"room": "!secret:x", "event": "$private"})
	if slices.Contains(f.loaded, "$private") {
		t.Error("a file from a room outside the scope was fetched")
	}

	f.messages["!open:x"][2].Media.Size = attachmentMost + 1
	if msg := callErr(t, s, "get_attachment", map[string]any{"room": "Standup", "event": "$pic"}); !strings.Contains(msg, "MB") {
		t.Errorf("an oversized file: %q", msg)
	}
	if slices.Contains(f.loaded, "$pic") {
		t.Error("an oversized file was fetched")
	}
}

// raw runs one tool and returns its result object as the protocol carries it.
func raw(t *testing.T, s *server, name string, args map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := s.call(encoded)
	if rpcErr != nil {
		t.Fatalf("%s: rpc error %+v", name, rpcErr)
	}
	body, _ := result.(map[string]any)
	if failed, _ := body["isError"].(bool); failed {
		t.Fatalf("%s failed: %+v", name, body)
	}
	return body
}
