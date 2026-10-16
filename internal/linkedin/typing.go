package linkedin

import (
	"slices"
	"time"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// LinkedIn repeats a typing notice while someone types and sends nothing on stop, so a
// typist is forgotten typingFor after their last notice, as its web client does.
const typingFor = 6 * time.Second

// onTyping records a typist in c's conversation and streams the room's set when it
// changes; your own notices, from another device, are not shown.
func (a *Adapter) onTyping(c *conn, t *linkedingo.RealtimeTypingIndicator) {
	who := profileOf(t.TypingParticipant.EntityURN)
	thread, ok := threadIn(t.Conversation.EntityURN)
	if who == "" || who == c.profile || !ok {
		return
	}
	room, person := roomID(c.profile, thread), personID(who)
	a.mu.Lock()
	if a.typing[room] == nil {
		a.typing[room] = map[string]*time.Timer{}
	}
	timer, already := a.typing[room][person]
	if already {
		timer.Reset(typingFor)
	} else {
		a.typing[room][person] = time.AfterFunc(typingFor, func() { a.stoppedTyping(room, person) })
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
