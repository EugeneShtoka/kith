package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Videos cannot be drawn in a terminal, so they are handed to a real player (same key
// as a voice note: play what is on this message).

// videoPlayerCandidates are tried in order when the config names none: xdg-open first,
// so the desktop's choice wins; the players are the fallback without xdg-utils.
var videoPlayerCandidates = [][]string{
	{"xdg-open"},
	{"mpv"},
	{"vlc"},
	{"celluloid"},
	{"mplayer"},
	{"ffplay", "-autoexit"},
}

// videoPlayedMsg reports what came of handing a video to a player.
type videoPlayedMsg struct {
	name string
	err  error
}

// hasVideo reports whether the selected message is something to watch.
func (m Model) hasVideo() bool {
	msg, ok := m.selectedMessage()
	return ok && msg.Media.IsVideo()
}

// playVideo fetches the video under the cursor and opens it in a real player.
func (m Model) playVideo(msg domain.Message) (Model, tea.Cmd) {
	player, err := m.videoPlayerCommand()
	if err != nil {
		return m.say(err.Error()), nil
	}
	name := msg.Media.Name
	if name == "" {
		name = "the video"
	}
	job := mediaJob{
		roomID: msg.RoomID, eventID: msg.ID,
		name: msg.Media.Name, mime: msg.Media.Mime,
		cache: m.mediaPolicy(msg).Cache,
	}
	return m.doing("opening " + isolate(name) + "…"), m.playVideoCmd(player, job, name)
}

// playVideoCmd puts the video on disk (see ensureOnDisk) and starts the player.
func (m Model) playVideoCmd(player []string, job mediaJob, name string) tea.Cmd {
	cache, backend, ctx := m.pics.cache, m.backend, m.ctx
	return func() tea.Msg {
		path := ensureOnDisk(ctx, cache, backend.LoadImage, job)
		if path == "" {
			return videoPlayedMsg{name: name, err: errNoVideo}
		}
		if err := launch(ctx, player, path); err != nil {
			return videoPlayedMsg{name: name, err: err}
		}
		return videoPlayedMsg{name: name}
	}
}

// errNoVideo is what a video that could not be fetched has to report.
var errNoVideo = fmt.Errorf("it could not be fetched")

// videoPlayerCommand is the program that plays a video, as a program and its arguments.
func (m Model) videoPlayerCommand() ([]string, error) {
	return resolveCommand(m.prefs.display.Media.VideoPlayer, videoPlayerCandidates, "video player")
}

// handleVideoPlayed reports how the handover went.
func (m Model) handleVideoPlayed(msg videoPlayedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not play "+msg.name, msg.err), nil
	}
	return m.say("playing " + isolate(msg.name)), nil
}
