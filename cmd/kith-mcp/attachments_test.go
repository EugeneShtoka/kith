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

// withFiles is two rooms: the open one holds a picture, a document, an unsupported
// image type and a deleted file; the private one a picture.
func withFiles() *fake {
	f := twoRooms()
	at := time.Unix(1_700_000_100, 0)
	f.messages[mx+"!open:x"] = append(f.messages[mx+"!open:x"],
		domain.Message{ID: mx + "$pic", Sender: "matrix:@dana:x", Body: "cat.png", Timestamp: at, Media: &domain.Media{Type: domain.MediaImage, Name: "cat.png", Mime: "image/png", Size: 4}},
		domain.Message{ID: mx + "$doc", Sender: "matrix:@dana:x", Body: "plan.pdf", Timestamp: at, Media: &domain.Media{Type: domain.MediaFile, Name: "../plan.pdf", Mime: "application/pdf"}},
		domain.Message{ID: mx + "$heic", Sender: "matrix:@dana:x", Body: "x.heic", Timestamp: at, Media: &domain.Media{Type: domain.MediaImage, Name: "x.heic", Mime: "image/heic"}},
		domain.Message{ID: mx + "$gone", Sender: "matrix:@dana:x", Redacted: true, Timestamp: at, Media: &domain.Media{Type: domain.MediaFile, Name: "old.txt"}},
	)
	f.messages[mx+"!secret:x"] = []domain.Message{{ID: mx + "$private", Sender: "matrix:@dana:x", Timestamp: at, Media: &domain.Media{Type: domain.MediaImage, Mime: "image/png"}}}
	f.files = map[domain.EventID][]byte{mx + "$pic": []byte("PNG!"), mx + "$doc": []byte("%PDF-1.7"), mx + "$heic": []byte("heic"), mx + "$private": []byte("PNG!")}
	return f
}

// A picture comes back as an image; another file is written under the files directory
// and named by path; a deleted file is not fetched.
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

	pic := raw(t, s, "get_attachment", map[string]any{"room": "Standup", "event": mx + "$pic"})
	blocks, _ := pic["content"].([]any)
	img, _ := blocks[0].(map[string]any)
	if img["type"] != "image" || img["mimeType"] != "image/png" || img["data"] != base64.StdEncoding.EncodeToString([]byte("PNG!")) {
		t.Errorf("the picture came back as %+v", img)
	}

	for event, want := range map[string]string{mx + "$doc": "%PDF-1.7", mx + "$heic": "heic"} {
		got := call(t, s, "get_attachment", map[string]any{"room": "Standup", "event": event})
		path, _ := got["path"].(string)
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, []byte(want)) || !strings.HasPrefix(path, s.files+string(os.PathSeparator)) {
			t.Errorf("%s: path %q (%v), read %q", event, path, err, data)
		}
	}

	callErr(t, s, "get_attachment", map[string]any{"room": "Standup", "event": mx + "$gone"})
	callErr(t, s, "get_attachment", map[string]any{"room": "Standup", "event": mx + "$1"})
}

// Files come only from readable rooms, never fetched from others; one over the limit is
// refused before fetching.
func TestAnAttachmentKeepsToTheScope(t *testing.T) {
	t.Parallel()
	f := withFiles()
	s := newServer(f, domain.ModelScope{Only: []string{"space:Work"}})
	s.files = t.TempDir()
	callErr(t, s, "get_attachment", map[string]any{"room": mx + "!secret:x", "event": mx + "$private"})
	if slices.Contains(f.loaded, mx+"$private") {
		t.Error("a file from a room outside the scope was fetched")
	}

	f.messages[mx+"!open:x"][2].Media.Size = attachmentMost + 1
	callErr(t, s, "get_attachment", map[string]any{"room": "Standup", "event": mx + "$pic"})
	if slices.Contains(f.loaded, mx+"$pic") {
		t.Error("an oversized file was fetched")
	}
}

// raw runs one tool and returns its protocol result object.
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
