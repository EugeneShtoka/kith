package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A Slack file is fetched from its url_private with the workspace's session (the token
// and the d cookie), so its ID and URL are what kith keeps to load it later: in the
// media table's file_json, as {"slack": fileID, "url": url}. A message carries one
// attachment, so a Slack message with several files is its first file kith can load,
// and each other one a row of its own under it (fileRows): the same sender, time and
// thread, the file's name for its words. One kith cannot load is named in the text.

// fileSep joins a file's row ID to its message's: "<message>#<file ID>". Slack has no
// message of its own for it, so what is done to the row (a reaction, a reply, a read)
// is done to its message (cutLast reads only the message's part).
const fileSep = "#"

// fileRowID is the row of one of a message's further files.
func fileRowID(message domain.EventID, fileID string) domain.EventID {
	return message + fileSep + domain.EventID(fileID)
}

// isFileRow reports whether id is a further file's row rather than a message.
func isFileRow(id domain.EventID) bool { return strings.Contains(string(id), fileSep) }

// fileRows is a row for each of m's files after the one msg carries, each kith can
// load, ordered as the message lists them.
func fileRows(msg domain.Message, m *slackgo.Msg) []domain.Message {
	shown := firstLoadable(m.Files)
	var out []domain.Message
	for i := range m.Files {
		if i == shown || !loadable(&m.Files[i]) {
			continue
		}
		media := fileMedia(&m.Files[i])
		out = append(out, domain.Message{
			ID: fileRowID(msg.ID, m.Files[i].ID), RoomID: msg.RoomID,
			Sender: msg.Sender, SenderName: msg.SenderName, Body: media.Name, Media: media,
			Timestamp: msg.Timestamp, ThreadRoot: msg.ThreadRoot, Seq: int64(len(out) + 1),
		})
	}
	return out
}

// fileOf is the file a row of fileRows carries, among m's.
func fileOf(row domain.EventID, m *slackgo.Msg) *slackgo.File {
	_, id, ok := strings.Cut(string(row), fileSep)
	if !ok {
		return nil
	}
	for i := range m.Files {
		if m.Files[i].ID == id {
			return &m.Files[i]
		}
	}
	return nil
}

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
// kith can load (for a file's row, its file); a message whose files are all gone
// (deleted since) loses its source, so loading it says so rather than asking Slack
// for what it no longer has.
func (a *Adapter) keepFile(ctx context.Context, msg domain.Message, m *slackgo.Msg) {
	if a.cache == nil || len(m.Files) == 0 {
		return
	}
	var file *slackgo.File
	if isFileRow(msg.ID) {
		file = fileOf(msg.ID, m)
	} else if i := firstLoadable(m.Files); i >= 0 {
		file = &m.Files[i]
	}
	encoded := ""
	if file != nil {
		raw, err := json.Marshal(fileSource{ID: file.ID, URL: fileURL(file)})
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

// refetchFileMessages reads again the messages cached before a message's further
// files were rows of their own, which named them in their text, so each file becomes
// one to open and save.
func (a *Adapter) refetchFileMessages(ctx context.Context, w *workspace) {
	if a.cache == nil {
		return
	}
	stale, err := a.cache.MessagesNamingFiles(ctx, domain.AccountRooms(domain.ProtocolSlack, w.creds.Team))
	if err != nil {
		a.log.Warn("read the messages naming files failed", "account", w.account.Name, "err", err)
		return
	}
	for i := range stale {
		channel, ts, ok := cutLast(domain.ParseID(string(stale[i].ID)).Native)
		if !ok {
			continue
		}
		var page []slackgo.Message
		err := waitingOut(ctx, func() error {
			var err error
			if _, root, isReply := cutLast(domain.ParseID(string(stale[i].ThreadRoot)).Native); isReply {
				var resp *slackgo.GetConversationHistoryResponse
				if resp, err = w.client.GetConversationRepliesContext(ctx, &slackgo.GetConversationRepliesParameters{
					GetConversationHistoryParameters: slackgo.GetConversationHistoryParameters{
						ChannelID: channel, Latest: ts, Oldest: ts, Inclusive: true, Limit: 1,
					},
					Timestamp: root,
				}); err == nil {
					page = resp.Messages
				}
			} else {
				var resp *slackgo.GetConversationHistoryResponse
				if resp, err = w.client.GetConversationHistoryContext(ctx, &slackgo.GetConversationHistoryParameters{
					ChannelID: channel, Latest: ts, Oldest: ts, Inclusive: true, Limit: 1,
				}); err == nil {
					page = resp.Messages
				}
			}
			if err != nil {
				return fmt.Errorf("slack: read %s again: %w", stale[i].ID, err) // %w: waitingOut reads a rate limit through it
			}
			return nil
		})
		if err != nil {
			a.log.Warn("read a message's files again failed", "account", w.account.Name, "err", err)
			continue
		}
		page = slices.DeleteFunc(page, func(m slackgo.Message) bool { return m.Timestamp != ts })
		a.cachePage(ctx, w, channel, page, time.Now())
	}
}
