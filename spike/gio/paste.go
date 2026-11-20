package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Pasting a file, as the desktop's composer takes one: Gio's clipboard is text only, so
// a paste in a composer first asks the system clipboard what it holds (xclip, or wl-paste
// under Wayland). Files copied in a file manager (text/uri-list), or an image (copied
// from a browser or a screenshot tool), are offered to send to the room, y sending them;
// anything else pastes as text, as the field would have.
// shortcut: a file dropped on the window is not taken: Gio 0.10 receives no drops from
// other apps on X11 or Wayland. Take them when Gio does, or by patching its data offers.
// shortcut: what is pasted is sent as it is asked, not held in the composer to go with
// the words as the desktop holds it.

// pasteTimeout bounds a clipboard tool, which a vanished owner can hang.
const pasteTimeout = 3 * time.Second

// clipboardTool is how this session's clipboard is read: the command listing what it
// holds, and the one printing it as a type.
func clipboardTool() (list []string, read func(mime string) []string) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return []string{"wl-paste", "--list-types"}, func(mime string) []string {
			return []string{"wl-paste", "--no-newline", "--type", mime}
		}
	}
	return []string{"xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"}, func(mime string) []string {
		return []string{"xclip", "-selection", "clipboard", "-t", mime, "-o"}
	}
}

// pasteType is the type of what the clipboard holds to send as files: copied files,
// else an image; "" for text.
func pasteType(types []string) string {
	if slices.Contains(types, "text/uri-list") {
		return "text/uri-list"
	}
	for _, preferred := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if slices.Contains(types, preferred) {
			return preferred
		}
	}
	return ""
}

// localFiles are the local files a text/uri-list names, none when it names anything else.
func localFiles(list string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(list))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "file" || u.Path == "" {
			return nil
		}
		out = append(out, u.Path)
	}
	return out
}

// pasteKey takes ed's paste key before the field does, asking the clipboard what it
// holds; text is pasted into ed once it is known to be text.
func (u *ui) pasteKey(ctx context.Context, gtx C, ed *widget.Editor) {
	if u.pasteText == ed {
		u.pasteText = nil
		gtx.Execute(clipboard.ReadCmd{Tag: ed}) // the field pastes what comes, as its own key would
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: ed, Name: "V", Required: key.ModShortcut})
		if !ok {
			return
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			go u.paste(ctx, ed, u.current)
		}
	}
}

// paste reads what the clipboard holds for room: files or an image are offered to send,
// anything else goes back to ed as text.
func (u *ui) paste(ctx context.Context, ed *widget.Editor, room domain.RoomID) {
	reading, cancel := context.WithTimeout(ctx, pasteTimeout)
	defer cancel()
	list, read := clipboardTool()
	types, err := output(reading, list)
	mime := ""
	if err == nil {
		mime = pasteType(strings.Fields(string(types)))
	}
	var data []byte
	if mime != "" {
		data, err = output(reading, read(mime))
	}
	var files []string
	if mime == "text/uri-list" {
		if files = localFiles(string(data)); len(files) == 0 {
			mime = "" // links, not files: pasted as text
		}
	}
	if err != nil {
		log.Printf("clipboard: %v", err) // read as text, as the field would
		mime = ""
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	defer u.w.Invalidate()
	switch {
	case mime == "":
		u.pasteText = ed
	case files != nil:
		what := filepath.Base(files[0])
		if len(files) > 1 {
			what = fmt.Sprintf("%d files", len(files))
		}
		u.toPane(paneTimeline) // the answer is the next key, which the field would take
		u.ask("Send "+what+" here?", func() { go u.sendPasted(ctx, room, files, nil, "") })
	default:
		u.toPane(paneTimeline)
		u.ask("Send the pasted image here?", func() { go u.sendPasted(ctx, room, nil, data, mime) })
	}
}

// sendPasted sends files to room, or an image's bytes as a file kithd can read (its
// cache: kithd sees no /tmp of ours), removed once sent.
func (u *ui) sendPasted(ctx context.Context, room domain.RoomID, files []string, image []byte, mime string) {
	if image != nil {
		path, err := keepPasted(image, mime)
		if err != nil {
			u.say("Not sent: " + err.Error())
			return
		}
		defer func() {
			if err := os.Remove(path); err != nil {
				log.Printf("pasted image: %v", err)
			}
		}()
		files = []string{path}
	}
	for _, path := range files {
		if err := u.backend.SendFile(ctx, room, path, filepath.Base(path), ""); err != nil {
			u.say("Not sent: " + err.Error())
			return
		}
	}
}

// keepPasted writes an image under the user's cache, named by the time and its type.
func keepPasted(image []byte, mime string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no cache directory: %w", err)
	}
	dir := filepath.Join(cache, "kith", "pasted")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("keeping the image: %w", err)
	}
	path := filepath.Join(dir, "pasted-"+time.Now().Format("2006-01-02-150405")+"."+strings.TrimPrefix(mime, "image/"))
	if err := os.WriteFile(path, image, 0o600); err != nil {
		return "", fmt.Errorf("keeping the image: %w", err)
	}
	return path, nil
}

// output runs argv and answers what it printed.
func output(ctx context.Context, argv []string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", argv[0], err)
	}
	return out, nil
}
