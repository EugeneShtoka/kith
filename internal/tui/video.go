package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Videos can't be drawn in a terminal, so they are handed to a real player (the same
// key as a voice note).

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

type videoPlayedMsg struct {
	name string
	err  error
}

func (m Model) hasVideo() bool {
	msg, ok := m.selectedMessage()
	return ok && msg.Media.IsVideo()
}

func (m Model) playVideo(msg domain.Message) (Model, tea.Cmd) {
	player, err := m.videoPlayerCommand()
	if err != nil {
		return m.say(err.Error()), nil
	}
	name := msg.Media.Name
	if name == "" {
		name = "the video"
	}
	job := m.jobFor(msg)
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

var errNoVideo = fmt.Errorf("it could not be fetched")

// videoPlayerCommand is the player program and its arguments.
func (m Model) videoPlayerCommand() ([]string, error) {
	return resolveCommand(m.prefs.display.Media.VideoPlayer, videoPlayerCandidates, "video player")
}

func (m Model) handleVideoPlayed(msg videoPlayedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not play "+msg.name, msg.err), nil
	}
	return m.say("playing " + isolate(msg.name)), nil
}
