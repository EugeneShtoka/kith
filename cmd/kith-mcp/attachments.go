package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message's file is fetched through the daemon, as the client fetches it. An image an
// assistant can look at comes back as an image; anything else is written under the
// cache and named by its path, since the assistant runs on this machine.

// attachmentMost is the largest file handed to an assistant.
const attachmentMost = 20 << 20

// seenImages are the image types an assistant takes as an image; another picture (a
// HEIC) is a file like any other.
var seenImages = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// toolContent is a tool answer already in the protocol's content form, passed through
// as it is rather than written as JSON.
type toolContent map[string]any

func (s *server) getAttachment(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[struct {
		Room  string `json:"room"`
		Event string `json:"event"`
	}](raw)
	if err != nil {
		return nil, err
	}
	room, err := s.readableRoom(ctx, in.Room)
	if err != nil {
		return nil, err
	}
	msg, err := s.messageIn(ctx, room, in.Event)
	if err != nil {
		return nil, err
	}
	if msg.Redacted || msg.Media == nil {
		return nil, fmt.Errorf("%s carries no file", in.Event)
	}
	if msg.Media.Size > attachmentMost {
		return nil, fmt.Errorf("the file is %d MB, more than the %d MB handed over", msg.Media.Size>>20, attachmentMost>>20)
	}
	data, err := s.backend.LoadImage(ctx, room.ID, msg.ID)
	if err != nil {
		return nil, fmt.Errorf("fetching the file: %w", err)
	}
	if len(data) > attachmentMost {
		return nil, fmt.Errorf("the file is %d MB, more than the %d MB handed over", len(data)>>20, attachmentMost>>20)
	}
	view := attachmentOf(msg.Media)
	view.Size = len(data)
	if view.Mime == "" {
		view.Mime = http.DetectContentType(data)
	}
	if mime, _, _ := strings.Cut(view.Mime, ";"); seenImages[strings.TrimSpace(mime)] {
		about, merr := json.Marshal(view)
		if merr != nil {
			return nil, fmt.Errorf("encoding the answer: %w", merr)
		}
		return toolContent{"content": []any{
			map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": strings.TrimSpace(mime)},
			map[string]any{"type": "text", "text": string(about)},
		}}, nil
	}
	path, err := s.keepFile(msg, data)
	if err != nil {
		return nil, err
	}
	return map[string]any{"attachment": view, "path": path}, nil
}

// keepFile writes a message's file under the agent files directory, named by the
// message and the file's own name, and is its path.
func (s *server) keepFile(msg domain.Message, data []byte) (string, error) {
	if s.files == "" {
		return "", errors.New("there is nowhere to put the file")
	}
	if err := os.MkdirAll(s.files, 0o700); err != nil {
		return "", fmt.Errorf("making a place for the file: %w", err)
	}
	sum := sha256.Sum256([]byte(string(msg.RoomID) + "\x00" + string(msg.ID)))
	name := filepath.Base(strings.TrimSpace(msg.Media.Name))
	if name == "." || name == "/" || name == "" {
		name = "attachment"
	}
	path := filepath.Join(s.files, hex.EncodeToString(sum[:6])+"-"+name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing the file: %w", err)
	}
	return path, nil
}
