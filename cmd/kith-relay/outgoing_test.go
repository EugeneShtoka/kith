package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
)

// A file the window sends is readied as the TUI readies it: where [display.media]
// anonymize holds (unset: it does), a picture goes as a copy without its metadata and
// under a neutral name; where it does not, the file goes as it is.
func TestOutgoingFilesAreAnonymizedAsTheTUISendsThem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, _ := relayConfig(t, dir)
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	const secret = "GPS 32.0853N 34.7818E"
	var enc bytes.Buffer
	if err := jpeg.Encode(&enc, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	photo := filepath.Join(dir, "IMG_2041 at home.jpg")
	comment := append([]byte{0xFF, 0xFE, 0, byte(2 + len(secret))}, secret...)
	if err := os.WriteFile(photo, append(append(enc.Bytes()[:2:2], comment...), enc.Bytes()[2:]...), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "media")
	ready := func() *devicev1.OutgoingResponse {
		t.Helper()
		resp, err := device.Outgoing(t.Context(), connect.NewRequest(&devicev1.OutgoingRequest{Path: photo}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}

	for _, file := range []string{
		"[display.media]\ncache_dir = \"" + cache + "\"\n",
		"[display.media]\ncache_dir = \"" + cache + "\"\nanonymize = false\n\n[[display.media.rule]]\nmatch = \"space:Work\"\nanonymize = true\n",
	} {
		handWrite(t, path, file)
		got := ready()
		data, err := os.ReadFile(got.GetPath())
		if err != nil {
			t.Fatal(err)
		}
		if !got.GetCopied() || got.GetName() != "photo.jpg" || bytes.Contains(data, []byte(secret)) ||
			!strings.HasPrefix(got.GetPath(), filepath.Join(cache, "outgoing")+string(filepath.Separator)) {
			t.Errorf("readied %+v (secret kept: %v), want a clean copy in the media cache, sent as photo.jpg",
				got, bytes.Contains(data, []byte(secret)))
		}
	}

	handWrite(t, path, "[display.media]\nanonymize = false\n")
	if got := ready(); got.GetPath() != photo || got.GetName() != "" || got.GetCopied() {
		t.Errorf("readied %+v with anonymize off, want the file as it is, under its own name", got)
	}
}
