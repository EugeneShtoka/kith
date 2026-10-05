package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A Slack file is fetched from its url_private with the workspace's session (the token
// and the d cookie), so its ID and URL are what kith keeps to load it later: in the
// media table's file_json, as {"slack": fileID, "url": url}. A message carries one
// attachment; a Slack message with several files shows the first one kith can load,
// and names the rest.

// fileSource is how a Slack file is kept for loading.
type fileSource struct {
	ID  string `json:"slack"`
	URL string `json:"url"`
}

// loadable reports whether kith can fetch a file: one Slack holds (not a link to
// another service's), not deleted, and not hidden by a free workspace's limit.
func loadable(f *slackgo.File) bool {
	return !f.IsExternal && f.Mode != "tombstone" && f.Mode != "hidden_by_limit" && fileURL(f) != ""
}

// fileURL is where a file's bytes are: its download URL, else its private one.
func fileURL(f *slackgo.File) string { return cmpOr(f.URLPrivateDownload, f.URLPrivate) }

// firstLoadable is the index of a message's first file kith can load; -1 for none.
func firstLoadable(files []slackgo.File) int {
	for i := range files {
		if loadable(&files[i]) {
			return i
		}
	}
	return -1
}

// fileMedia is a file as kith shows it, its kind by its type.
func fileMedia(f *slackgo.File) *domain.Media {
	media := domain.Media{Type: domain.MediaFile, Name: f.Name, Mime: f.Mimetype, Size: f.Size}
	switch kind, _, _ := strings.Cut(f.Mimetype, "/"); kind {
	case "image":
		media.Type, media.Width, media.Height = domain.MediaImage, f.OriginalW, f.OriginalH
	case "video":
		media.Type = domain.MediaVideo
	case "audio":
		media.Type = domain.MediaAudio
	}
	// A clip recorded in Slack: a voice message, whatever container it came in.
	if f.SubType == "slack_audio" {
		media.Type, media.Name = domain.MediaAudio, domain.VoiceMessage
	}
	return &media
}

// keepFile records how to load a cached message's attachment, the first file of m
// kith can load; a message whose files are all gone (deleted since) loses its source,
// so loading it says so rather than asking Slack for what it no longer has.
func (a *Adapter) keepFile(ctx context.Context, msg domain.Message, m *slackgo.Msg) {
	if a.cache == nil || len(m.Files) == 0 {
		return
	}
	encoded := ""
	if i := firstLoadable(m.Files); i >= 0 {
		raw, err := json.Marshal(fileSource{ID: m.Files[i].ID, URL: fileURL(&m.Files[i])})
		if err != nil {
			return
		}
		encoded = string(raw)
	}
	if err := a.cache.SaveMediaSource(ctx, msg.ID, msg.RoomID, "", encoded); err != nil {
		a.log.Warn("keep a file's source failed", "room", msg.RoomID, "err", err)
	}
}

// LoadImage downloads a message's file with its workspace's session. It caches
// nothing: the client's media cache is the cache, as for the other networks.
func (a *Adapter) LoadImage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	w, _, err := a.conversation(roomID)
	if err != nil {
		return nil, err
	}
	if a.cache == nil {
		return nil, fmt.Errorf("slack: no cache to find the file of %s in", eventID)
	}
	_, fileJSON, ok, err := a.cache.MediaSource(ctx, roomID, eventID)
	if err != nil {
		return nil, fmt.Errorf("slack: read the file's source: %w", err)
	}
	var source fileSource
	if !ok || json.Unmarshal([]byte(fileJSON), &source) != nil || source.URL == "" {
		return nil, fmt.Errorf("slack: %s has no file kith can load", eventID)
	}
	var data bytes.Buffer
	// slack-go sends the session only to https://files.slack.com, whatever the URL says.
	if err := waitingOut(ctx, func() error {
		data.Reset()
		return w.client.GetFileContext(ctx, source.URL, &data)
	}); err != nil {
		return nil, fmt.Errorf("slack: download the file of %s: %w", eventID, err)
	}
	return data.Bytes(), nil
}

// SendFile uploads a file to a conversation, the caption as its message. Slack's
// upload answers with the file, not the message it posted, so the message reaches the
// cache and the clients as Slack's live echo of it, like anyone else's file.
func (a *Adapter) SendFile(ctx context.Context, roomID domain.RoomID, path, caption string) error {
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return err
	}
	file, err := os.Open(path) // #nosec G304 -- the file the person chose to send
	if err != nil {
		return fmt.Errorf("slack: open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("slack: read %s: %w", path, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("slack: %s is empty, and Slack takes no empty files", filepath.Base(path))
	}
	if _, err := w.client.UploadFileContext(ctx, slackgo.UploadFileParameters{
		Reader: file, Filename: filepath.Base(path), FileSize: int(info.Size()),
		Channel: channel, InitialComment: caption,
	}); err != nil {
		return fmt.Errorf("slack: send %s to %s: %w", filepath.Base(path), roomID, err)
	}
	return nil
}
