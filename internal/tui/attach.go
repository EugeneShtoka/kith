package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/filedialog"
)

// downloadDir is where attachments land: the configured directory, else
// $XDG_DOWNLOAD_DIR, else ~/Downloads.
func (m Model) downloadDir() (string, error) {
	if dir := strings.TrimSpace(m.prefs.display.Media.DownloadDir); dir != "" {
		return expandHome(dir)
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_DOWNLOAD_DIR")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home directory to save into: %w", err)
	}
	return filepath.Join(home, "Downloads"), nil
}

// openAttach asks the desktop (XDG portal) for the file to send.
func (m Model) openAttach() (Model, tea.Cmd) {
	if _, ok := m.currentRoom(); !ok {
		m = m.say("no room open")
		return m, nil
	}
	m = m.say("choosing a file…")
	return m, chooseFileCmd(m.ctx)
}

// askForPath is the fallback when no portal answered (e.g. over SSH).
func (m Model) askForPath() (Model, tea.Cmd) {
	m = m.openPrompt(promptAttach)
	return m, nil
}

// fileChosenMsg carries what the desktop chooser answered.
type fileChosenMsg struct {
	path string
	err  error
}

// chooseFileCmd runs the chooser off the event loop.
func chooseFileCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		path, err := filedialog.File(ctx, "Send which file?")
		return fileChosenMsg{path: path, err: err}
	}
}

// handleFileChosen sends what was chosen, falls back to a typed path when there is no
// chooser, and stays quiet on cancel.
func (m Model) handleFileChosen(msg fileChosenMsg) (Model, tea.Cmd) {
	switch {
	case errors.Is(msg.err, filedialog.ErrCanceled):
		m = m.clearStatus()
		return m, nil
	case errors.Is(msg.err, filedialog.ErrUnavailable):
		m = m.say("no desktop file chooser here — type a path")
		return m.askForPath()
	case msg.err != nil:
		m = m.sayErr("file chooser", msg.err)
		return m, nil
	}
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	// Read now: the caption may have been typed while the chooser was up.
	caption := strings.TrimSpace(m.compose.input)
	m.compose.input, m.compose.drafted = "", nil
	return m.attachPath(room, msg.path, caption)
}

// submitAttach sends the file with the composer as its caption; an explicit
// "path | caption" wins.
func (m Model) submitAttach(input string) (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	path, caption := splitCaption(input)
	if caption == "" {
		caption = strings.TrimSpace(m.compose.input)
	}
	if path == "" {
		m = m.say("nothing to send")
		return m, nil
	}
	// Cleared so the next enter does not send the caption again.
	m.compose.input, m.compose.drafted = "", nil
	return m.attachPath(room, path, caption)
}

// attachPath is the send itself. The path is resolved here because the daemon (under
// systemd) has no home or working directory and accepts only absolute paths.
func (m Model) attachPath(room domain.Room, path, caption string) (Model, tea.Cmd) {
	expanded, err := expandHome(path)
	if err != nil {
		m = m.sayErr("cannot attach", err)
		return m, nil
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		m = m.sayErr("cannot resolve "+path, err)
		return m, nil
	}
	// The stat runs in sendFileCmd: a dead mount would otherwise block the UI.
	m = m.say("sending " + isolate(filepath.Base(abs)) + "…")
	return m, m.sendFileCmd(room.ID, abs, caption)
}

// splitCaption reads "path | caption"; a pipe because filenames contain spaces.
func splitCaption(input string) (path, caption string) {
	path, caption, found := strings.Cut(input, "|")
	if !found {
		return strings.TrimSpace(input), ""
	}
	return strings.TrimSpace(path), strings.TrimSpace(caption)
}

// expandHome resolves a leading ~ against the home directory; nothing else is expanded.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand ~: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/")), nil
}

// attachSentMsg reports the upload, or why it did not happen.
type attachSentMsg struct {
	name string
	err  error
}

// sendFileCmd stats the file first (for a useful "no such file") and hands it to the
// daemon, which encrypts, uploads and posts it.
func (m Model) sendFileCmd(roomID domain.RoomID, path, caption string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		sent := attachSentMsg{name: filepath.Base(path)}
		info, err := os.Stat(path)
		switch {
		case err != nil:
			sent.err = err
			return sent
		case info.IsDir():
			sent.err = errors.New("it is a directory")
			return sent
		}
		sent.err = backend.SendFile(ctx, roomID, path, caption)
		return sent
	}
}

// handleAttachSent reports the upload's outcome.
func (m Model) handleAttachSent(msg attachSentMsg) (Model, tea.Cmd) {
	switch {
	case errors.Is(msg.err, api.ErrNoEncryption):
		m = m.say("did not send " + isolate(msg.name) + ": this room is encrypted and encryption is not running for this session")
		return m, nil
	case msg.err != nil:
		m = m.sayErr("could not send "+isolate(msg.name), msg.err)
		return m, nil
	}
	m = m.say("sent " + isolate(msg.name))
	return m, nil
}

// download saves the selected message's attachment where the settings say it goes.
func (m Model) download() (Model, tea.Cmd) {
	msg, target, ok := m.downloadTarget()
	if !ok {
		m = m.say(noAttachmentNote(msg))
		return m, nil
	}
	m = m.say("saving " + isolate(msg.Media.Name) + "…")
	return m, m.downloadCmd(msg, target)
}

// pendingSave is the attachment waiting on the desktop's folder chooser.
type pendingSave struct {
	target domain.DownloadTarget
	msg    domain.Message
}

// taking returns the pending errand; the caller clears it.
func (p pendingSave) taking() (domain.Message, domain.DownloadTarget) {
	return p.msg, p.target
}

// saveAs saves into a folder picked with the desktop chooser; the template still names
// the file.
func (m Model) saveAs() (Model, tea.Cmd) {
	msg, target, ok := m.downloadTarget()
	if !ok {
		m = m.say(noAttachmentNote(msg))
		return m, nil
	}
	m.pendingSave = pendingSave{target: target, msg: msg}
	m = m.say("choosing a folder…")
	return m, chooseFolderCmd(m.ctx, target.Dir)
}

// folderChosenMsg carries the folder the desktop chooser answered with.
type folderChosenMsg struct {
	dir string
	err error
}

// chooseFolderCmd asks the portal for a directory, starting where the file would
// otherwise have gone.
func chooseFolderCmd(ctx context.Context, startAt string) tea.Cmd {
	return func() tea.Msg {
		dir, err := filedialog.Folder(ctx, "Save into which folder?", startAt)
		return folderChosenMsg{dir: dir, err: err}
	}
}

// handleFolderChosen writes the pending attachment into the chosen folder, keeping the
// name the template produced.
func (m Model) handleFolderChosen(msg folderChosenMsg) (Model, tea.Cmd) {
	saving, target := m.pendingSave.taking()
	m.pendingSave = pendingSave{}
	switch {
	case errors.Is(msg.err, filedialog.ErrCanceled):
		m = m.clearStatus()
		return m, nil
	case errors.Is(msg.err, filedialog.ErrUnavailable):
		m = m.say("no desktop folder chooser here — saving to " + isolate(target.Dir))
	case msg.err != nil:
		m = m.sayErr("folder chooser", msg.err)
		return m, nil
	default:
		target.Dir = msg.dir
		m = m.say("saving " + isolate(target.Name) + "…")
	}
	if saving.Media == nil {
		return m, nil
	}
	return m, m.downloadCmd(saving, target)
}

// noAttachmentNote says why nothing happened, when nothing did.
func noAttachmentNote(msg domain.Message) string {
	if msg.ID == "" {
		return ""
	}
	return "no attachment on this message"
}

// downloadTarget resolves where the selected message's attachment belongs.
func (m Model) downloadTarget() (domain.Message, domain.DownloadTarget, bool) {
	msg, ok := m.selectedMessage()
	if !ok || msg.Media == nil {
		return msg, domain.DownloadTarget{}, false
	}
	dir, err := m.downloadDir()
	if err != nil {
		return msg, domain.DownloadTarget{}, false
	}
	return msg, domain.ResolveDownload(dir, m.prefs.display.Media.DownloadTemplate, m.downloadRules(), m.placeOf(msg)), true
}

// downloadRules converts the configured overrides into the pure layer's own shape.
func (m Model) downloadRules() []domain.DownloadRule {
	configured := m.prefs.display.Media.Rules
	rules := make([]domain.DownloadRule, 0, len(configured))
	for _, rule := range configured {
		dir := rule.Dir
		if dir != "" {
			if expanded, err := expandHome(dir); err == nil {
				dir = expanded
			}
		}
		rules = append(rules, domain.DownloadRule{
			Match:    rule.Match,
			Sender:   rule.Sender,
			Dir:      dir,
			Template: rule.Template,
		})
	}
	return rules
}

// placeOf is everything a download template can name about one message, using
// displayed names.
func (m Model) placeOf(msg domain.Message) domain.DownloadPlace {
	place := domain.DownloadPlace{
		RoomID: string(msg.RoomID),
		Person: m.processedName(msg),
		Sender: msg.Sender,
		Name:   msg.Media.Name,
		Sent:   msg.Timestamp,
	}
	if room, ok := m.roomByID(msg.RoomID); ok {
		place.Room = m.roomLabel(room)
		if spaces := m.spacesOf(room.ID); len(spaces) > 0 {
			place.Space = spaces[0]
		}
	}
	return place
}

// downloadedMsg reports where the attachment landed.
type downloadedMsg struct {
	path string
	err  error
}

// downloadCmd fetches the bytes through the backend and writes them to target.
func (m Model) downloadCmd(msg domain.Message, target domain.DownloadTarget) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	roomID, eventID := msg.RoomID, msg.ID
	return func() tea.Msg {
		data, err := backend.LoadImage(ctx, roomID, eventID)
		if err != nil {
			return downloadedMsg{err: err}
		}
		path, err := writeDownload(target.Dir, target.Name, data)
		return downloadedMsg{path: path, err: err}
	}
}

// writeDownload saves under a name that does not already exist; it never overwrites.
func writeDownload(dir, name string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	// The name is remote input: keep only its last element so it cannot traverse out.
	base := filepath.Base(filepath.Clean("/" + name))
	if base == "/" || base == "." {
		base = "attachment"
	}
	path := filepath.Join(dir, base)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; ; i++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the save path the user chose; O_EXCL never overwrites
		switch {
		case errors.Is(err, os.ErrExist):
			path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
			continue
		case err != nil:
			return "", fmt.Errorf("cannot write into %s: %w", dir, err)
		}
		_, werr := file.Write(data)
		cerr := file.Close()
		if werr != nil {
			return "", fmt.Errorf("cannot write %s: %w", path, werr)
		}
		if cerr != nil {
			return "", fmt.Errorf("cannot finish writing %s: %w", path, cerr)
		}
		return path, nil
	}
}

// handleDownloaded reports the full saved path, copying it with copy_downloads.
func (m Model) handleDownloaded(msg downloadedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m = m.sayErr("could not save", msg.err)
		return m, nil
	}
	if m.prefs.external.copyDownloads {
		return m.copy(msg.path, "saved to "+isolate(msg.path)+" (path copied)")
	}
	m = m.say("saved to " + isolate(msg.path))
	return m, nil
}

// hasAttachment reports whether the selected message carries one, for the legend.
func (m Model) hasAttachment() bool {
	msg, ok := m.selectedMessage()
	return ok && msg.Media != nil
}
