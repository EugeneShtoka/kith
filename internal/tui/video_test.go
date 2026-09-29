package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/media"
)

// clip is a message carrying a video.
func clip(id domain.EventID) domain.Message {
	return domain.Message{
		ID: id, RoomID: "!a:x", Sender: "@dana:x", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaVideo, Name: "holiday.mp4", Mime: "video/mp4", Size: 999},
	}
}

// watching is a model selecting a video, with a real media cache behind it.
func watching(t *testing.T, player string) Model {
	t.Helper()
	b := &videoBackend{bytes: []byte("not really an mp4, but bytes on disk are bytes")}
	disp := config.Display{Media: config.Media{VideoPlayer: player}}
	m := sized(t, withRooms(t, New(context.Background(), b, disp)))
	m.focus = paneTimeline
	cache, err := media.New(t.TempDir(), -1)
	if err != nil {
		t.Fatalf("media cache: %v", err)
	}
	m = m.WithCache(cache)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{clip("$v")}}})
	m.timeline.selected = "$v"
	return m
}

type videoBackend struct {
	apitest.Nop
	bytes []byte
}

func (b *videoBackend) LoadImage(context.Context, domain.RoomID, domain.EventID) ([]byte, error) {
	return b.bytes, nil
}

func TestPlayOpensAVideoInAPlayer(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	record := filepath.Join(dir, "played")
	script := filepath.Join(dir, "fake-player")
	body := "#!/bin/sh\nprintf '%s' \"$1\" > " + record + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	m, cmd := watching(t, script).play()
	if cmd == nil {
		t.Fatal("the play key issued no command")
	}
	if !strings.Contains(m.status(), "holiday.mp4") {
		t.Errorf("status = %q, want it to name what is being opened", m.status())
	}

	msg, ok := cmd().(videoPlayedMsg)
	if !ok {
		t.Fatalf("play returned %T, want a videoPlayedMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("playing the video failed: %v", msg.err)
	}

	// Detached, so poll; the cached file keeps its real extension for the player.
	got := waitForFile(t, record)
	if !strings.HasSuffix(string(got), ".mp4") {
		t.Errorf("player was handed %q, want a file a player can recognize", got)
	}
}

// A handoff that exits non-zero at once (xdg-open with no association) is reported.
func TestAPlayerThatFailsImmediatelyIsReported(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := filepath.Join(dir, "cannot-open")
	body := "#!/bin/sh\nexit 4\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	m := watching(t, script)
	_, cmd := m.play()
	if cmd == nil {
		t.Fatal("the play key issued no command")
	}
	msg, ok := cmd().(videoPlayedMsg)
	if !ok {
		t.Fatalf("play returned %T, want a videoPlayedMsg", cmd())
	}
	if msg.err == nil {
		t.Fatal("a player that exited 4 was reported as success")
	}

	shown, _ := m.handleVideoPlayed(msg)
	if !strings.Contains(shown.status(), "could not play") {
		t.Errorf("status = %q, want the failure said out loud", shown.status())
	}
}

// A configured player that is not installed is an error, not a fallback.
func TestAConfiguredVideoPlayerMustExist(t *testing.T) {
	t.Parallel()

	m, cmd := watching(t, "definitely-not-a-real-player").play()

	if cmd != nil {
		t.Error("nothing should be fetched for a player that cannot run")
	}
	if !strings.Contains(m.status(), "definitely-not-a-real-player") {
		t.Errorf("status = %q, want it to name the player it could not run", m.status())
	}
}

func TestTheWatchHintAppearsOnAVideo(t *testing.T) {
	t.Parallel()

	m := watching(t, "mpv")
	if !m.hasVideo() {
		t.Fatal("a video message should be watchable")
	}
	if m.hasVoiceNote() {
		t.Error("a video is not a voice note")
	}
	if !strings.Contains(m.timelineHints(), "watch") {
		t.Errorf("hints = %q, want the watch key advertised", m.timelineHints())
	}
}

// waitForFile reads path once a detached process has written it, failing after 2s.
func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing was written to %s: the player was never handed a file", path)
	return nil
}
