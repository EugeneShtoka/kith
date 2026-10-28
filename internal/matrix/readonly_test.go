package matrix

import (
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// A room is read-only when our level is below what a message needs: by our own entry,
// the users default, a message-specific level or the events default.
func TestARoomIsReadOnlyBelowTheMessageLevel(t *testing.T) {
	t.Parallel()
	const me = id.UserID("@me:x")
	for name, tc := range map[string]struct {
		levels *event.PowerLevelsEventContent
		want   bool
	}{
		"an ordinary room": {levels: &event.PowerLevelsEventContent{}},
		"announcements": {levels: &event.PowerLevelsEventContent{
			EventsDefault: 50, Users: map[id.UserID]int{"@admin:x": 100},
		}, want: true},
		"announcements, and we moderate": {levels: &event.PowerLevelsEventContent{
			EventsDefault: 50, Users: map[id.UserID]int{me: 50},
		}},
		"messages only, raised": {levels: &event.PowerLevelsEventContent{
			Events: map[string]int{event.EventMessage.Type: 10},
		}, want: true},
		"messages lowered below a raised default": {levels: &event.PowerLevelsEventContent{
			EventsDefault: 50, Events: map[string]int{event.EventMessage.Type: 0},
		}},
		"everyone muted but the default": {levels: &event.PowerLevelsEventContent{
			UsersDefault: -1,
		}, want: true},
		"muted ourselves": {levels: &event.PowerLevelsEventContent{
			Users: map[id.UserID]int{me: -10},
		}, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := readOnly(tc.levels, me); got != tc.want {
				t.Errorf("readOnly = %v, want %v", got, tc.want)
			}
		})
	}
}
