package slack

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// filesHost plays https://files.slack.com, where slack-go alone sends the session:
// it answers downloads with the bytes kept under their path, takes uploads, and keeps
// what each request carried. Every other request goes to the fake API server.
type filesHost struct {
	mu       sync.Mutex
	files    map[string][]byte // by path
	requests []*http.Request
	uploaded []byte
}

func (h *filesHost) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "files.slack.com" {
		return http.DefaultTransport.RoundTrip(r)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, r)
	answer := func(code int, body []byte) *http.Response {
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}, Request: r}
	}
	if r.Method == http.MethodPost { // an upload
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			return answer(http.StatusBadRequest, nil), nil
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			return answer(http.StatusBadRequest, nil), nil
		}
		h.uploaded, _ = io.ReadAll(file)
		return answer(http.StatusOK, []byte("OK")), nil
	}
	data, ok := h.files[r.URL.Path]
	if !ok {
		return answer(http.StatusNotFound, nil), nil
	}
	return answer(http.StatusOK, data), nil
}

func (h *filesHost) asked() []*http.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests
}

// slackWithFiles is the fake API server and files host, its client signed in with a
// d cookie as a browser shows it.
func slackWithFiles(t *testing.T) (*fakeSlack, *filesHost, *slackgo.Client) {
	t.Helper()
	host := &filesHost{files: map[string][]byte{"/files-pri/T1-F1/a.png": []byte("png bytes")}}
	f, client := newFakeSlack(t, slackgo.OptionHTTPClient(&http.Client{Transport: host}),
		slackgo.OptionCookie("d", cookieValue("xoxd-a%2Bb")))
	return f, host, client
}

func imageMsg(ts, mode string) slackgo.Message {
	return slackgo.Message{Msg: slackgo.Msg{
		Type: "message", SubType: "file_share", User: "U2", Timestamp: ts,
		Files: []slackgo.File{{
			ID: "F1", Name: "a.png", Mimetype: "image/png", Mode: mode,
			URLPrivate: "https://files.slack.com/files-pri/T1-F1/a.png",
		}},
	}}
}

// A message's file is kept with it and loads with the workspace's session — the
// token and the d cookie, encoded once. A file deleted since (a page that shows it
// gone) loses its source, and loading it asks Slack nothing.
func TestAFileLoadsWithTheSessionUntilItIsDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, host, client := slackWithFiles(t)
	a, w := connectedTo(t, client)
	room := roomID("T1", "C1")
	id := domain.EventID("slack:T1/C1/1.0")

	msgs, _ := a.cachePage(ctx, w, "C1", []slackgo.Message{imageMsg("1.0", "hosted")}, time.Now())
	if len(msgs) != 1 || msgs[0].Media == nil || msgs[0].Media.Type != domain.MediaImage {
		t.Fatalf("page = %+v, want the image attached", msgs)
	}
	if cached, found, err := a.cache.MessageByID(ctx, room, id); err != nil || !found || cached.Media == nil || cached.Media.Name != "a.png" {
		t.Fatalf("cached = %+v (found %v, %v), want it with its image", cached, found, err)
	}
	data, err := a.LoadImage(ctx, room, id)
	if err != nil || string(data) != "png bytes" {
		t.Fatalf("LoadImage = (%q, %v), want the file", data, err)
	}
	got := host.asked()
	if len(got) != 1 || got[0].Header.Get("Authorization") != "Bearer xoxc-test" {
		t.Fatalf("files host asked %d times, authorization %q; want once, with the token", len(got), got[0].Header.Get("Authorization"))
	}
	if cookie := got[0].Header.Get("Cookie"); !strings.Contains(cookie, "d=xoxd-a%2Bb") {
		t.Errorf("cookie = %q, want d as the browser holds it", cookie)
	}

	a.cachePage(ctx, w, "C1", []slackgo.Message{imageMsg("1.0", "tombstone")}, time.Now())
	if _, err := a.LoadImage(ctx, room, id); err == nil || !strings.Contains(err.Error(), "no file kith can load") {
		t.Errorf("LoadImage of a deleted file = %v, want it said there is none", err)
	}
	if n := len(host.asked()); n != 1 {
		t.Errorf("files host asked %d times, want no more after the file went", n)
	}
}

// A file goes up by Slack's upload — named, sized, its bytes to the upload URL — and
// is posted to the conversation with the caption as its message.
func TestAFileGoesUpWithItsCaption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, host, client := slackWithFiles(t)
	f.on("files.getUploadURLExternal", func(map[string]string) any {
		return map[string]any{"ok": true, "upload_url": "https://files.slack.com/upload/v1/abc", "file_id": "F9"}
	})
	f.on("files.completeUploadExternal", func(map[string]string) any {
		return map[string]any{"ok": true, "files": []any{map[string]string{"id": "F9", "title": "notes.txt"}}}
	})
	a, _ := connectedTo(t, client)
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := a.SendFile(ctx, roomID("T1", "C1"), path, "here"); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if asked := f.calls("files.getUploadURLExternal"); len(asked) != 1 || asked[0]["filename"] != "notes.txt" || asked[0]["length"] != "5" {
		t.Errorf("upload URL asked %v, want notes.txt of 5 bytes", asked)
	}
	if string(host.uploaded) != "hello" {
		t.Errorf("uploaded %q, want the file's bytes", host.uploaded)
	}
	done := f.calls("files.completeUploadExternal")
	if len(done) != 1 || done[0]["channel_id"] != "C1" || done[0]["initial_comment"] != "here" || !strings.Contains(done[0]["files"], "F9") {
		t.Errorf("upload completed with %v, want F9 in C1 with the caption", done)
	}

	empty := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.SendFile(ctx, roomID("T1", "C1"), empty, ""); err == nil {
		t.Error("an empty file was sent; Slack takes none")
	}
}

// A file heard live loads as one from history does; an edit that shows it deleted
// takes its source.
func TestAFileHeardLiveLoadsUntilAnEditShowsItGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, host, client := slackWithFiles(t)
	a, w := connectedTo(t, client)
	room, id := roomID("T1", "C1"), domain.EventID("slack:T1/C1/1.0")

	live := imageMsg("1.0", "hosted").Msg
	a.arrived(ctx, w, "C1", &live)
	if data, err := a.LoadImage(ctx, room, id); err != nil || string(data) != "png bytes" {
		t.Fatalf("LoadImage of a live file = (%q, %v), want the file", data, err)
	}

	gone := imageMsg("1.0", "tombstone").Msg
	gone.Text, gone.Edited = "the file is gone", &slackgo.Edited{User: "U2", Timestamp: "2.0"}
	a.onEdited(ctx, w, &slackgo.MessageEvent{Msg: slackgo.Msg{Channel: "C1", SubType: "message_changed", Timestamp: "2.0"}, SubMessage: &gone})
	if _, err := a.LoadImage(ctx, room, id); err == nil {
		t.Error("a file an edit showed deleted still loads")
	}
	if n := len(host.asked()); n != 1 {
		t.Errorf("files host asked %d times, want once", n)
	}
}
