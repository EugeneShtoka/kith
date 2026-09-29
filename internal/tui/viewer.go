package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/media"
)

// External viewing. The viewer is always handed a list (the room's pictures), straight
// from the media cache — nothing is copied to /tmp.

// viewLimit caps how many pictures one viewing fetches.
const viewLimit = 200

// viewerCandidates are the image viewers tried, in order, when [display.media] viewer
// names none, each with the flags that make it fit a picture to its window.
var viewerCandidates = [][]string{
	{"nsxiv", "-s", "f"},
	{"sxiv", "-s", "f"},
	{"imv"},
	{"feh", "--scale-down"},
	{"eog"},
	{"gthumb"},
	{"qimgv"},
	{"loupe"},
	{"ristretto"},
}

// mediaViewedMsg reports what came of a viewing: how many pictures were opened, and
// what went wrong if nothing was.
type mediaViewedMsg struct {
	count int
	err   error
}

// viewMedia opens the attachment under the cursor outside the client: a picture opens
// the room's gallery, a video a player, anything else the desktop's handler. Voice notes
// play inside the client, so this points at the play key instead.
func (m Model) viewMedia() (Model, tea.Cmd) {
	msg, standing := m.selectedMessage()
	switch {
	case !standing || m.focus != paneTimeline || msg.Media.IsImage():
		// A picture, or no message cursor: open the room's gallery.
	case msg.Media.IsAudio():
		return m.say("that is a voice note — " + m.playKeyHint() + " plays it here"), nil
	case msg.Media.IsVideo():
		return m.playVideo(msg)
	case msg.Media != nil:
		return m.openFile(msg)
	default:
		return m.say("nothing attached to this message"), nil
	}
	// From the room list there is no message cursor, so it opens on the newest picture.
	from := m.selectedID()
	if m.focus != paneTimeline {
		from = ""
	}
	jobs, start := m.gallery(from)
	if len(jobs) == 0 {
		return m.say("no pictures here to view"), nil
	}
	viewer, err := m.viewerCommand()
	if err != nil {
		return m.say(err.Error()), nil
	}
	m = m.doing(fmt.Sprintf("opening %s…", countOf(len(jobs), "picture")))
	configured := strings.TrimSpace(m.prefs.display.Media.Viewer) != ""
	return m, m.viewMediaCmd(viewer, configured, jobs, start)
}

// gallery is the room's pictures in the order they were sent, and the index of the one
// pointed at (the newest when none is). The limit is applied around that picture.
func (m Model) gallery(from domain.EventID) (jobs []mediaJob, start int) {
	pictures := make([]mediaJob, 0, viewLimit)
	start = -1
	for i := range m.timeline.messages {
		msg := m.timeline.messages[i]
		if msg.ID == "" || !msg.Media.IsImage() {
			continue
		}
		if msg.ID == from {
			start = len(pictures)
		}
		pictures = append(pictures, mediaJob{
			roomID: msg.RoomID, eventID: msg.ID,
			name: msg.Media.Name, mime: msg.Media.Mime,
			cache: m.mediaPolicy(msg).Cache,
		})
	}
	if start < 0 {
		start = len(pictures) - 1
	}
	return trimAround(pictures, start, viewLimit)
}

// trimAround keeps at most limit jobs centered on start, and says where start ended up.
func trimAround(jobs []mediaJob, start, limit int) ([]mediaJob, int) {
	if len(jobs) <= limit || start < 0 {
		return jobs, max(start, 0)
	}
	first := min(max(start-limit/2, 0), len(jobs)-limit)
	return jobs[first : first+limit], start - first
}

// viewerFetches bounds the downloads a gallery runs at once.
const viewerFetches = 8

// viewMediaCmd puts every picture on disk (bounded concurrency; most are cached) and
// hands the list to the viewer; configured says the user named it (see viewerStart).
func (m Model) viewMediaCmd(viewer []string, configured bool, jobs []mediaJob, start int) tea.Cmd {
	cache, backend, ctx := m.pics.cache, m.backend, m.ctx
	return func() tea.Msg {
		paths := make([]string, len(jobs))
		var wg sync.WaitGroup
		sem := make(chan struct{}, viewerFetches)
		for i, job := range jobs {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				paths[i] = ensureOnDisk(ctx, cache, backend.LoadImage, job)
			})
		}
		wg.Wait()

		// Pictures that could not be fetched are dropped; at tracks the chosen one.
		files := make([]string, 0, len(paths))
		at := 0
		for i, p := range paths {
			if p == "" {
				continue
			}
			if i <= start {
				at = len(files)
			}
			files = append(files, p)
		}
		if len(files) == 0 {
			return mediaViewedMsg{err: errNoPictures}
		}
		command, files := viewerStart(viewer, configured, files, at)
		if err := launch(ctx, command, files...); err != nil {
			return mediaViewedMsg{err: err}
		}
		return mediaViewedMsg{count: len(files)}
	}
}

// viewerStart makes the viewer open at start. A built-in candidate is told how it
// understands: by index (nsxiv, sxiv, imv) or by file name (feh). A configured command
// is run as written — no flag it may not know is added — and is told only through the
// placeholders it was given ({index}, 1-based; {file}, the path). Anything not told
// gets the list rotated so start comes first.
func viewerStart(command []string, configured bool, files []string, start int) ([]string, []string) {
	if configured {
		if filled, ok := fillViewerPlaceholders(command, files, start); ok {
			return filled, files
		}
	}
	if start <= 0 || start >= len(files) {
		return command, files
	}
	if !configured {
		switch command[0] {
		case "nsxiv", "sxiv", "imv":
			return append(append([]string{}, command...), "-n", strconv.Itoa(start+1)), files
		case "feh":
			return append(append([]string{}, command...), "--start-at", files[start]), files
		}
	}
	return command, append(append([]string{}, files[start:]...), files[:start]...)
}

// fillViewerPlaceholders replaces {index} (1-based) and {file} in a configured
// viewer's arguments with files[start], and reports whether there was any.
func fillViewerPlaceholders(command, files []string, start int) ([]string, bool) {
	if start < 0 || start >= len(files) {
		start = 0
	}
	file := ""
	if len(files) > 0 {
		file = files[start]
	}
	fill := strings.NewReplacer("{index}", strconv.Itoa(start+1), "{file}", file)
	out := append([]string{}, command...)
	found := false
	for i := 1; i < len(out); i++ {
		if strings.Contains(out[i], "{index}") || strings.Contains(out[i], "{file}") {
			found = true
			out[i] = fill.Replace(out[i])
		}
	}
	return out, found
}

// playKeyHint names the key that plays a voice note, as actually bound.
func (m Model) playKeyHint() string {
	if key := m.keys.keyHint(scopeTimeline, actPlay); key != "" {
		return key
	}
	return "the play key"
}

// errNoPictures is what a viewing that fetched nothing has to report.
var errNoPictures = fmt.Errorf("none of the pictures could be fetched")

// launchGrace is how long an external program is given to fail before it is let go.
const launchGrace = 500 * time.Millisecond

// launch starts an external program on some files, waits launchGrace to see whether it
// fails, and then lets go of it. The wait is what catches xdg-open exiting non-zero for a
// missing association, which would otherwise look like success.
func launch(ctx context.Context, command []string, files ...string) error {
	args := append(append([]string{}, command[1:]...), files...)
	// #nosec G204,G702 -- command comes from config as a program and flags, never a shell
	cmd := exec.CommandContext(ctx, command[0], args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not run %s: %w", command[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", command[0], err)
		}
		return nil
	case <-time.After(launchGrace):
		return nil
	}
}

// resolveCommand is the configured program taken as written (an error if missing),
// else the first candidate found on $PATH. Synchronous on purpose: whether the key does
// anything must be known before it reports success.
func resolveCommand(configured string, candidates [][]string, missing string) ([]string, error) {
	if fields := strings.Fields(configured); len(fields) > 0 {
		if _, err := exec.LookPath(fields[0]); err != nil {
			return nil, fmt.Errorf("cannot run the configured %s %q: %w", missing, fields[0], err)
		}
		return fields, nil
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err == nil {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("no %s found; set one in [display.media]", missing)
}

// ensureOnDisk returns the file a picture lives in, fetching it if needed; "" when it
// could not be had. A no-cache room's file is still written, just not touched, so Trim
// reclaims it.
func ensureOnDisk(
	ctx context.Context,
	cache *media.Cache,
	fetch func(context.Context, domain.RoomID, domain.EventID) ([]byte, error),
	job mediaJob,
) string {
	path := cache.Path(job.eventID, job.name, job.mime)
	if path == "" {
		return ""
	}
	if _, ok := cache.Read(job.eventID, job.name, job.mime); ok {
		if job.cache {
			cache.Touch(path)
		}
		return path
	}
	data, err := fetch(ctx, job.roomID, job.eventID)
	if err != nil || len(data) == 0 {
		return ""
	}
	written, err := cache.Write(job.eventID, job.name, job.mime, data)
	if err != nil {
		return ""
	}
	return written
}

// viewerCommand is the program that shows pictures, as a program and its arguments.
func (m Model) viewerCommand() ([]string, error) {
	return resolveCommand(m.prefs.display.Media.Viewer, viewerCandidates, "image viewer")
}

// handleMediaViewed reports how a viewing went.
func (m Model) handleMediaViewed(msg mediaViewedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not view", msg.err), nil
	}
	return m.say(fmt.Sprintf("viewing %s", countOf(msg.count, "picture"))), nil
}

// countOf is "1 picture" or "12 pictures", so a status line does not say "1 pictures".
func countOf(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}
