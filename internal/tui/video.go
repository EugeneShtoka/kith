package tui

import (
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

func (m Model) hasVideo() bool {
	msg, ok := m.selectedMessage()
	return ok && msg.Media.IsVideo()
}

func (m Model) playVideo(msg domain.Message) (Model, tea.Cmd) {
	player, err := m.videoPlayerCommand()
	return m.handOff(msg, player, err, "the video", "playing", "play")
}

// videoPlayerCommand is the player program and its arguments.
func (m Model) videoPlayerCommand() ([]string, error) {
	return resolveCommand(m.prefs.display.Media.VideoPlayer, videoPlayerCandidates, "video player")
}
