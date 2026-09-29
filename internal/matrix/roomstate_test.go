package matrix

import (
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const meUser = id.UserID("@me:example.org")

// stateEvent builds a minimal state event of a type, keyed on stateKey.
func stateEvent(t event.Type, stateKey string) *event.Event {
	key := stateKey
	return &event.Event{Type: t, StateKey: &key}
}

// joined wraps events as one joined room's timeline.
func joined(events ...*event.Event) *mautrix.RespSync {
	resp := &mautrix.RespSync{}
	resp.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{
		"!a:example.org": {Timeline: mautrix.SyncTimeline{
			SyncEventsList: mautrix.SyncEventsList{Events: events},
		}},
	}
	return resp
}

// The sync loop must notice every event that changes what Rooms()/Spaces() answer,
// and ignore ordinary traffic (a refresh is a network call).
func TestRoomsChanged(t *testing.T) {
	t.Parallel()

	left := &mautrix.RespSync{}
	left.Rooms.Leave = map[id.RoomID]*mautrix.SyncLeftRoom{"!a:example.org": {}}

	directs := &mautrix.RespSync{}
	directs.AccountData.Events = []*event.Event{{Type: event.AccountDataDirectChats}}

	tests := map[string]struct {
		resp *mautrix.RespSync
		want bool
	}{
		"a room renamed":        {joined(stateEvent(event.StateRoomName, "")), true},
		"a canonical alias set": {joined(stateEvent(event.StateCanonicalAlias, "")), true},
		"a space child added":   {joined(stateEvent(event.StateSpaceChild, "!child:example.org")), true},
		"a space parent added":  {joined(stateEvent(event.StateSpaceParent, "!parent:example.org")), true},
		"a room created":        {joined(stateEvent(event.StateCreate, "")), true},
		// The topic is shown in the title.
		"a topic change":        {joined(stateEvent(event.StateTopic, "")), true},
		"we joined or left":     {joined(stateEvent(event.StateMember, string(meUser))), true},
		"we left the room":      {left, true},
		"the direct list moved": {directs, true},

		"someone else's membership": {joined(stateEvent(event.StateMember, "@alice:example.org")), false},
		"an ordinary message":       {joined(&event.Event{Type: event.EventMessage}), false},
		"an empty response":         {&mautrix.RespSync{}, false},
		"no response at all":        {nil, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := roomsChanged(tc.resp, meUser); got != tc.want {
				t.Errorf("roomsChanged = %v, want %v", got, tc.want)
			}
		})
	}
}

// State in the state section counts the same as state in the timeline.
func TestRoomsChangedReadsBothSections(t *testing.T) {
	t.Parallel()

	resp := &mautrix.RespSync{}
	resp.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{
		"!a:example.org": {State: mautrix.SyncEventsList{Events: []*event.Event{
			stateEvent(event.StateRoomName, ""),
		}}},
	}
	if !roomsChanged(resp, meUser) {
		t.Error("a rename in the state section should count")
	}
}
