package slack

import (
	"context"
	"slices"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Slack repeats a typing notice every few seconds and sends nothing on stop, so a typist
// is forgotten typingFor after their last notice, as the web client does.
const typingFor = 6 * time.Second

// onTyping records a typist and streams the room's set when it changes.
func (a *Adapter) onTyping(w *workspace, e *slackgo.UserTypingEvent) {
	if e.User == "" || e.User == w.creds.User || e.Channel == "" {
		return
	}
	room, who := roomID(w.creds.Team, e.Channel), personID(w.creds.Team, e.User)
	a.mu.Lock()
	if a.typing[room] == nil {
		a.typing[room] = map[string]*time.Timer{}
	}
	timer, already := a.typing[room][who]
	if already {
		timer.Reset(typingFor)
	} else {
		a.typing[room][who] = time.AfterFunc(typingFor, func() { a.stoppedTyping(room, who) })
	}
	now := a.typistsLocked(room)
	a.mu.Unlock()
	if !already {
		emit(a, a.activity, domain.Activity{RoomID: room, Typing: now})
	}
}

// stoppedTyping forgets a typist whose notices stopped.
func (a *Adapter) stoppedTyping(room domain.RoomID, who string) {
	a.mu.Lock()
	delete(a.typing[room], who)
	now := a.typistsLocked(room)
	a.mu.Unlock()
	emit(a, a.activity, domain.Activity{RoomID: room, Typing: now})
}

// typistsLocked is who is typing in room, sorted. Caller holds mu.
func (a *Adapter) typistsLocked(room domain.RoomID) []string {
	out := make([]string, 0, len(a.typing[room]))
	for who := range a.typing[room] {
		out = append(out, who)
	}
	slices.Sort(out)
	return out
}

// SendTyping sends a typing notice over the websocket. Slack has no "stopped". It is a
// courtesy, so a disconnected workspace is no error.
func (a *Adapter) SendTyping(_ context.Context, roomID domain.RoomID, typing bool, _ time.Duration) error {
	if !typing {
		return nil
	}
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return nil //nolint:nilerr // a courtesy
	}
	w.mu.Lock()
	rtm := w.rtm
	w.mu.Unlock()
	if rtm != nil {
		rtm.SendMessage(rtm.NewTypingMessage(channel))
	}
	return nil
}
