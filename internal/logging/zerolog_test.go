package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// mautrix's zerolog output passes through slog: its errors become warnings, and a
// request body (which can be a message's text) and any token in a URL are dropped.
func TestZerologBridgeScrubsBodiesAndTokens(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	bridge := zerologBridge{log: New(&buf, Options{Level: slog.LevelDebug})}
	secret := strings.Repeat("q", 16)
	line := `{"level":"error","time":"2026-09-25T10:00:00Z","req_id":7,"method":"PUT",` +
		`"url":"https://hs.example/_matrix/client/v3/rooms/!r:x/send/m.room.message/1?access_token=syt_` + secret + `",` +
		`"req_body":{"msgtype":"m.text","body":"secret message body"},"status_code":400,"message":"Request failed"}` + "\n"
	if n, err := bridge.Write([]byte(line)); err != nil || n != len(line) {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	out := buf.String()
	for _, leaked := range []string{"secret message body", secret} {
		if strings.Contains(out, leaked) {
			t.Errorf("%q leaked:\n%s", leaked, out)
		}
	}
	// Per-request outcomes are debug: the caller logs the failure it returns.
	for _, want := range []string{"level=DEBUG", `msg="Request failed"`, "status_code=400", "req_id=7"} {
		if !strings.Contains(out, want) {
			t.Errorf("bridge output lacks %q:\n%s", want, out)
		}
	}

	buf.Reset()
	if _, err := bridge.Write([]byte(`{"level":"error","message":"Failed to decrypt event","event_id":"$e"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "event_id=$e") {
		t.Errorf("a mautrix error did not become a warning: %q", out)
	}

	buf.Reset()
	if _, err := bridge.Write([]byte("not json\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "not json") {
		t.Errorf("a non-JSON line was lost: %q", buf.String())
	}
}
