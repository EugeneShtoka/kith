package tui

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/media"
)

// photoBackend serves one real, encoded photograph.
type photoBackend struct{ apitest.Nop }

func (photoBackend) LoadImage(context.Context, domain.RoomID, domain.EventID) ([]byte, error) {
	m := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := range 240 {
		for x := range 320 {
			m.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// A real model, a real photograph, all the way to a rendered frame.
func TestTheWholeLoadPathDrawsAPicture(t *testing.T) {
	t.Parallel()

	for _, detail := range []string{"", "half", "sextant"} {
		t.Run("detail="+detail, func(t *testing.T) {
			t.Parallel()

			cache, err := media.New(t.TempDir(), -1)
			if err != nil {
				t.Fatalf("cache: %v", err)
			}
			disp := config.Display{Media: config.Media{Mode: "inline", Detail: detail}}
			m := sized(t, withRooms(t, starterNew(photoBackend{}, disp).WithCache(cache)))
			m.focus = paneTimeline

			mdl, cmd := asModel(m.Update(timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
				Messages: []domain.Message{{
					ID: "$1", RoomID: "!a:x", Sender: "@a:x", Timestamp: at(1),
					Media: &domain.Media{
						Type: domain.MediaImage, Name: "p.jpg", Mime: "image/jpeg",
						Width: 320, Height: 240,
					},
				}},
			}}))
			next := mdl
			if cmd == nil {
				t.Fatal("the timeline landing produced no command, so nothing was fetched")
			}

			loaded, found := drainFor[imageLoadedMsg](cmd)
			if !found {
				t.Fatal("no picture was loaded")
			}
			if loaded.err != nil {
				t.Fatalf("loading the picture failed: %v", loaded.err)
			}
			if len(loaded.rows) == 0 {
				t.Fatal("the picture drew no rows")
			}

			next = update(t, next, loaded)
			frame := next.View().Content
			if !strings.Contains(frame, loaded.rows[0]) {
				t.Error("the drawing is not in the rendered frame")
			}
			// And it came back out of the cache the second time, drawn the same way.
			again, hit := cache.Render("$1", next.imageTargetWidth(), next.imageMaxHeight())
			if !hit {
				t.Fatal("the drawing was not kept, so opening the room again would redo it")
			}
			if strings.Join(loaded.rows, "\n") != string(again) {
				t.Error("what was cached is not what was drawn")
			}
		})
	}
}

// drainFor runs a command, and any batch it produces, looking for one kind of message.
func drainFor[T tea.Msg](cmd tea.Cmd) (T, bool) {
	var zero T
	if cmd == nil {
		return zero, false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if got, ok := drainFor[T](c); ok {
				return got, true
			}
		}
	case T:
		return msg, true
	}
	return zero, false
}
