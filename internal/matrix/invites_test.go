package matrix

import (
	"encoding/json"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// stripped describes one stripped state event as the server sends it.
type strippedEvent struct {
	evtType  string
	stateKey string
	sender   string
	content  map[string]any
}

func stripped(evtType event.Type, stateKey, sender string, content map[string]any) strippedEvent {
	return strippedEvent{evtType: evtType.Type, stateKey: stateKey, sender: sender, content: content}
}

// invitedRoom builds an invite section by round-tripping through JSON, so fixtures
// have the same VeryRaw/Parsed state a real invitation arrives with.
func invitedRoom(events ...strippedEvent) *mautrix.SyncInvitedRoom {
	raw := make([]map[string]any, 0, len(events))
	for _, e := range events {
		raw = append(raw, map[string]any{
			"type":      e.evtType,
			"state_key": e.stateKey,
			"sender":    e.sender,
			"content":   e.content,
		})
	}
	body, err := json.Marshal(map[string]any{"invite_state": map[string]any{"events": raw}})
	if err != nil {
		panic(err)
	}
	var room mautrix.SyncInvitedRoom
	if err := json.Unmarshal(body, &room); err != nil {
		panic(err)
	}
	return &room
}

// backend returns an InProc whose client only knows who we are.
func backend(me string) *InProc {
	b := New(nil)
	b.client = &mautrix.Client{UserID: id.UserID(me)}
	return b
}

func TestToInvite(t *testing.T) {
	t.Parallel()

	const me = "@me:x"

	t.Run("named room records the inviter", func(t *testing.T) {
		t.Parallel()
		b := backend(me)
		got := b.toInvite("!r:x", invitedRoom(
			stripped(event.StateRoomName, "", "@alice:x", map[string]any{"name": "Design Review"}),
			stripped(event.StateMember, me, "@alice:x", map[string]any{"membership": "invite"}),
		))
		if got.Name != "Design Review" {
			t.Errorf("Name = %q, want Design Review", got.Name)
		}
		if got.InvitedBy != "@alice:x" {
			t.Errorf("InvitedBy = %q, want @alice:x", got.InvitedBy)
		}
		if !got.IsInvite() {
			t.Error("the room should be marked as an invitation")
		}
		if got.ID != "!r:x" {
			t.Errorf("ID = %q", got.ID)
		}
	})

	t.Run("direct message is flagged and named after the inviter", func(t *testing.T) {
		t.Parallel()
		b := backend(me)
		got := b.toInvite("!dm:x", invitedRoom(
			stripped(event.StateMember, me, "@bob:x", map[string]any{"membership": "invite", "is_direct": true}),
			stripped(event.StateMember, "@bob:x", "@bob:x", map[string]any{"membership": "join", "displayname": "Bob Stone"}),
		))
		if !got.IsDirect {
			t.Error("is_direct on our own member event should mark a DM")
		}
		if got.Name != "Bob Stone" {
			t.Errorf("Name = %q, want the inviter's display name", got.Name)
		}
	})

	t.Run("nameless invite falls back to the inviter's MXID", func(t *testing.T) {
		t.Parallel()
		b := backend(me)
		got := b.toInvite("!x:x", invitedRoom(
			stripped(event.StateMember, me, "@carol:x", map[string]any{"membership": "invite"}),
		))
		if got.Name != "@carol:x" {
			t.Errorf("Name = %q, want the inviter's MXID", got.Name)
		}
	})

	t.Run("a canonical alias beats a bare room ID", func(t *testing.T) {
		t.Parallel()
		b := backend(me)
		got := b.toInvite("!x:x", invitedRoom(
			stripped(event.StateCanonicalAlias, "", "@dave:x", map[string]any{"alias": "#public:x"}),
			stripped(event.StateMember, me, "@dave:x", map[string]any{"membership": "invite"}),
		))
		if got.Name != "#public:x" {
			t.Errorf("Name = %q, want #public:x", got.Name)
		}
	})

	t.Run("a room name beats a canonical alias", func(t *testing.T) {
		t.Parallel()
		b := backend(me)
		got := b.toInvite("!x:x", invitedRoom(
			stripped(event.StateCanonicalAlias, "", "@dave:x", map[string]any{"alias": "#public:x"}),
			stripped(event.StateRoomName, "", "@dave:x", map[string]any{"name": "The Real Name"}),
			stripped(event.StateMember, me, "@dave:x", map[string]any{"membership": "invite"}),
		))
		if got.Name != "The Real Name" {
			t.Errorf("Name = %q, want the room name", got.Name)
		}
	})

	t.Run("someone else's invite is not read as ours", func(t *testing.T) {
		t.Parallel()
		b := backend(me)
		got := b.toInvite("!x:x", invitedRoom(
			stripped(event.StateRoomName, "", "@alice:x", map[string]any{"name": "Room"}),
			stripped(event.StateMember, "@someone:x", "@alice:x", map[string]any{"membership": "invite"}),
		))
		if got.InvitedBy != "" {
			t.Errorf("InvitedBy = %q, want empty — that invite was not ours", got.InvitedBy)
		}
	})

	t.Run("unparseable state is skipped, not fatal", func(t *testing.T) {
		t.Parallel()
		b := backend(me)
		got := b.toInvite("!x:x", invitedRoom(
			stripped(event.StateRoomName, "", "@alice:x", map[string]any{"name": []int{1, 2}}), // wrong type
			stripped(event.StateMember, me, "@alice:x", map[string]any{"membership": "invite"}),
		))
		if got.InvitedBy != "@alice:x" {
			t.Errorf("InvitedBy = %q — a bad event should not stop the good ones", got.InvitedBy)
		}
	})

	t.Run("empty invite still yields an invitation row", func(t *testing.T) {
		t.Parallel()
		b := backend(me)
		got := b.toInvite("!bare:x", invitedRoom())
		if !got.IsInvite() || got.ID != "!bare:x" {
			t.Errorf("got %+v, want a bare invitation for !bare:x", got)
		}
		if got.DisplayName() == "" {
			t.Error("an invitation must never render blank")
		}
	})
}

// inviteSync builds a sync response inviting us to these rooms.
func inviteSync(rooms ...string) *mautrix.RespSync {
	resp := &mautrix.RespSync{}
	resp.Rooms.Invite = map[id.RoomID]*mautrix.SyncInvitedRoom{}
	for _, room := range rooms {
		resp.Rooms.Invite[id.RoomID(room)] = invitedRoom(
			stripped(event.StateMember, "@me:x", "@alice:x", map[string]any{"membership": "invite"}),
		)
	}
	return resp
}

// ids lists the room IDs of a published set, in order.
func ids(rooms []domain.Room) []string {
	out := make([]string, 0, len(rooms))
	for i := range rooms {
		out = append(out, string(rooms[i].ID))
	}
	return out
}

// Regression: sync is a delta, so an invite absent from a later batch is still pending.
func TestFoldInvitesKeepsWhatASyncDoesNotMention(t *testing.T) {
	t.Parallel()

	b := backend("@me:x")
	if got, changed := b.foldInvites(inviteSync("!a:x"), false); !changed || len(got) != 1 {
		t.Fatalf("the first invitation should publish one room, got %v (changed=%t)", ids(got), changed)
	}
	if got, changed := b.foldInvites(&mautrix.RespSync{}, false); changed {
		t.Errorf("a batch that said nothing about the invitation republished %v", ids(got))
	}
	if len(b.invites.rooms) != 1 {
		t.Errorf("the invitation was dropped by a batch that did not mention it: %v", b.invites.rooms)
	}
	got, changed := b.foldInvites(inviteSync("!b:x"), false)
	if !changed {
		t.Fatal("a new invitation is a change")
	}
	if len(got) != 2 {
		t.Errorf("published %v, want both invitations", ids(got))
	}
}

// An invite is removed when the room moves to join or leave.
func TestFoldInvitesDropsAnsweredRooms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		apply func(*mautrix.RespSync)
	}{
		{"accepted", func(r *mautrix.RespSync) {
			r.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{"!a:x": {}}
		}},
		{"declined", func(r *mautrix.RespSync) {
			r.Rooms.Leave = map[id.RoomID]*mautrix.SyncLeftRoom{"!a:x": {}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := backend("@me:x")
			if _, changed := b.foldInvites(inviteSync("!a:x", "!b:x"), false); !changed {
				t.Fatal("setup")
			}
			answered := &mautrix.RespSync{}
			tc.apply(answered)
			got, changed := b.foldInvites(answered, false)
			if !changed {
				t.Fatal("an answered invitation is a change")
			}
			if len(got) != 1 || got[0].ID != "!b:x" {
				t.Errorf("published %v, want only the unanswered invitation", ids(got))
			}
		})
	}
}

// An initial sync is a census: it replaces the set.
func TestFoldInvitesReplacesTheSetOnAnInitialSync(t *testing.T) {
	t.Parallel()

	b := backend("@me:x")
	if _, changed := b.foldInvites(inviteSync("!a:x", "!b:x"), false); !changed {
		t.Fatal("setup")
	}
	got, changed := b.foldInvites(inviteSync("!b:x"), true)
	if !changed {
		t.Fatal("an invitation gone from a census is a change")
	}
	if len(got) != 1 || got[0].ID != "!b:x" {
		t.Errorf("published %v, want only what the census listed", ids(got))
	}
	if _, changed := b.foldInvites(inviteSync("!b:x"), true); changed {
		t.Error("the same census republished")
	}
}

// Same size, different rooms — the count alone must not be the test.
func TestFoldInvitesNoticesASwapOfTheSameSize(t *testing.T) {
	t.Parallel()

	b := backend("@me:x")
	if _, changed := b.foldInvites(inviteSync("!a:x"), true); !changed {
		t.Fatal("setup")
	}
	if _, changed := b.foldInvites(inviteSync("!z:x"), true); !changed {
		t.Error("a swapped invitation of the same count is a change")
	}
}

// syncInvites publishes the whole set on change and nothing on a repeat.
func TestSyncInvitesPublishesOnlyOnChange(t *testing.T) {
	t.Parallel()

	b := backend("@me:x")
	resp := &mautrix.RespSync{}
	resp.Rooms.Invite = map[id.RoomID]*mautrix.SyncInvitedRoom{
		"!r:x": invitedRoom(
			stripped(event.StateRoomName, "", "@alice:x", map[string]any{"name": "Room"}),
			stripped(event.StateMember, "@me:x", "@alice:x", map[string]any{"membership": "invite"}),
		),
	}

	b.syncInvites(t.Context(), resp, "s1")
	select {
	case got := <-b.out.invites:
		if len(got) != 1 || got[0].Name != "Room" || got[0].InvitedBy != "@alice:x" {
			t.Errorf("published %+v", got)
		}
	default:
		t.Fatal("the first invite set should be published")
	}

	b.syncInvites(t.Context(), resp, "s2") // the same section again
	select {
	case got := <-b.out.invites:
		t.Errorf("an unchanged set was republished: %+v", got)
	default:
	}

	answered := &mautrix.RespSync{}
	answered.Rooms.Leave = map[id.RoomID]*mautrix.SyncLeftRoom{"!r:x": {}}
	b.syncInvites(t.Context(), answered, "s3")
	select {
	case got := <-b.out.invites:
		if len(got) != 0 {
			t.Errorf("published %+v, want an empty set", got)
		}
	default:
		t.Fatal("an emptied set should be published")
	}
}

// A nil invite section is ignored rather than panicking.
func TestSyncInvitesSkipsNilRooms(t *testing.T) {
	t.Parallel()

	b := backend("@me:x")
	resp := &mautrix.RespSync{}
	resp.Rooms.Invite = map[id.RoomID]*mautrix.SyncInvitedRoom{"!r:x": nil}
	b.syncInvites(t.Context(), resp, "s1")
	select {
	case got := <-b.out.invites:
		if len(got) != 0 {
			t.Errorf("published %+v for a nil room", got)
		}
	default:
	}
}
