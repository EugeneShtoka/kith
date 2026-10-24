package tui

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memberships is, for every room, which tags hold it.
func memberships(m Model) map[domain.RoomID][]bool {
	view := m.unreadView()
	out := make(map[domain.RoomID][]bool, len(m.rooms.all))
	for i := range m.rooms.all {
		out[m.rooms.all[i].ID] = slices.Clone(view.tagsOf(m.rooms.all[i]).in)
	}
	return out
}

// Over random tags, same-named rooms, name entries, silent tags, unread state and
// Telegram archives (followed, mirrored or not): /tag flips whether the tag holds the
// room and changes no other room's tags, and the written config reads back the same.
func TestFilingIntoATagChangesThatRoomAlone(t *testing.T) {
	t.Parallel()
	names := []string{"A", "B", "C"}
	terms := []string{"*", "dm", "unread", "space:Made", "room:A", "room:B", "not dm", "not room:C", "tag:T0"}
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 23)) // #nosec G404 -- reproducible
		var rooms []domain.Room
		var made []domain.RoomID
		for i := range 2 + rng.IntN(5) {
			id := domain.RoomID(fmt.Sprintf(mx+"!r%d:x", i))
			archived := false
			if rng.IntN(2) == 0 { // a Telegram chat, maybe in its archive
				id, archived = domain.RoomID(fmt.Sprintf("telegram:42/-%d", i+1)), rng.IntN(2) == 0
			}
			rooms = append(rooms, domain.Room{ID: id, Name: names[rng.IntN(len(names))], IsDirect: rng.IntN(3) == 0, Archived: archived})
			if rng.IntN(2) == 0 {
				made = append(made, id)
			}
		}
		entry := func() string {
			if rng.IntN(2) == 0 {
				return "room:" + names[rng.IntN(len(names))]
			}
			return string(rooms[rng.IntN(len(rooms))].ID)
		}
		var tags []config.Tag
		for i := range 1 + rng.IntN(3) {
			tag := config.Tag{Name: fmt.Sprintf("T%d", i)}
			if rng.IntN(4) == 0 {
				tag.Exclusive = []string{"tags"}
			}
			if rng.IntN(3) == 0 {
				tag.CountsUnread = &off
			}
			for range rng.IntN(3) {
				if term := terms[rng.IntN(len(terms))]; i > 0 || term != "tag:T0" {
					tag.Rule = append(tag.Rule, term)
				}
			}
			for range rng.IntN(2) {
				tag.Picked = append(tag.Picked, entry())
			}
			for range rng.IntN(2) {
				tag.Excluded = append(tag.Excluded, entry())
			}
			tags = append(tags, tag)
		}
		cfg := config.Config{Tags: tags}
		follow := rng.IntN(2) == 0
		cfg.Telegram.Archive = config.NetworkArchive{Follow: &follow, Mirror: rng.IntN(2) == 0, Tag: tags[rng.IntN(len(tags))].Name}
		m := update(t, configured(cfg), roomsMsg{rooms: rooms})
		m = update(t, m, spacesMsg{spaces: []domain.Space{{ID: mx + "!made:x", Name: "Made", Children: made}}})
		for _, r := range rooms {
			if rng.IntN(2) == 0 {
				m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: r.ID, Messages: 1, Counted: true}})
			}
		}
		for step := range 6 {
			room := rooms[rng.IntN(len(rooms))]
			tag := rng.IntN(len(tags))
			before := memberships(m)
			_, _, archiveCmd := m.fileArchive(room, []tagFiling{{tag: tag, in: !before[room.ID][tag]}})
			m, _ = m.toggleTag(tags[tag].Name, room)
			after := memberships(m)
			where := fmt.Sprintf("seed %d step %d: /tag T%d on %s (%s)", seed, step, tag, room.ID, room.Name)
			archive := cfg.Telegram.Archive
			if domain.NetworkOf(string(room.ID)) == domain.ProtocolTelegram && archive.Mirror && strings.EqualFold(archive.Tag, tags[tag].Name) {
				if archiveCmd == nil {
					t.Fatalf("%s: the archive tag is mirrored, and Telegram was not asked", where)
				}
				// The rule alone decides whether the lists must mention the room; Telegram decides the rest.
				filed, view := m.conf.base.Tags[tag], m.unreadView()
				facts := view.factsOf(room)
				bare := make([]domain.Tag, len(m.conf.base.Tags))
				for i, c := range m.conf.base.Tags {
					bare[i] = domain.Tag{Name: c.Name, Rule: c.Rule}
				}
				ruleOnly, _, err := domain.NewTagSet(bare)
				if err != nil {
					t.Fatal(err)
				}
				plain := facts
				plain.ArchivedIn = ""
				byRule := ruleOnly.HasAt(tag, plain, view.tagState(tag, room))
				picked, excluded := slices.ContainsFunc(filed.Picked, facts.Names), slices.ContainsFunc(filed.Excluded, facts.Names)
				if follow && (picked || (excluded && !byRule)) {
					t.Fatalf("%s: Telegram decides its archive, and the tag's lists say more of the room than its rule needs: %+v", where, filed)
				}
			}
			if after[room.ID][tag] == before[room.ID][tag] {
				t.Fatalf("%s: held = %t before and after; tags %+v", where, before[room.ID][tag], m.conf.base.Tags)
			}
			for id, was := range before {
				if id != room.ID && !slices.Equal(after[id], was) {
					t.Fatalf("%s: %s changed tags %v → %v; tags %+v", where, id, was, after[id], m.conf.base.Tags)
				}
			}
			reread := update(t, configured(m.conf.base.Clone()), roomsMsg{rooms: m.rooms.joined})
			reread = update(t, reread, spacesMsg{spaces: m.rooms.spaces})
			reread.unread = m.unread
			if got := memberships(reread); !slices.Equal(got[room.ID], after[room.ID]) {
				t.Fatalf("%s: the written config reads back as %v, the running one says %v", where, got[room.ID], after[room.ID])
			}
		}
	}
}

// The filing picker's last row makes a tag holding the room; escaping the name prompt
// changes nothing.
func TestTheFilingPickerMakesANewTagWithTheRoomInIt(t *testing.T) {
	t.Parallel()
	open := func(t *testing.T) Model {
		t.Helper()
		m := counting(t, config.Display{})
		m.focus = paneRooms
		m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
		m, _ = m.selectRoom(roomByName(t, m, mx+"!a:x"))
		m, _ = m.openSpacePicker()
		last := len(m.picker.items) - 1
		if last < 0 || m.picker.items[last].value != tagNew {
			t.Fatalf("filing rows = %v, want New tag last", m.picker.items)
		}
		m.picker.cursor = last
		m, _ = m.acceptPick()
		if m.picker.active() || m.prompt.kind != promptTagName {
			t.Fatalf("enter on New tag: picker open %v, prompt %v; want the tag name asked", m.picker.active(), m.prompt.kind)
		}
		return m
	}

	m := open(t)
	tags := len(m.conf.base.Tags)
	m.prompt.input = "Later"
	m, _ = m.submitPrompt()
	if got := m.conf.base.Tags[tagIndex(t, m, "Later")].Picked; len(got) != 1 || got[0] != mx+"!a:x" {
		t.Errorf("the new tag picks %v, want the room's ID", got)
	}
	if len(m.conf.base.Tags) != tags+1 {
		t.Errorf("%d tags, want one more than %d", len(m.conf.base.Tags), tags)
	}
	if !strings.Contains(m.status(), "Alpha is in Later") {
		t.Errorf("status = %q, want it to say the room went in", m.status())
	}

	m = open(t)
	m, _ = m.cancelPrompt()
	if len(m.conf.base.Tags) != tags || m.picker.active() {
		t.Errorf("after escaping the name: %d tags (want %d), a picker open %v", len(m.conf.base.Tags), tags, m.picker.active())
	}
}

// A refused archive is reported and the room list reloads; a successful one needs
// nothing. A network rewriting its rooms is reloaded too.
func TestAnArchiveRefusedPutsTheRoomBack(t *testing.T) {
	t.Parallel()
	m := configured(config.Config{Tags: []config.Tag{{Name: "Archived"}}})
	next, cmd := m.handleArchived(archivedMsg{label: "Dana", archived: true, err: errors.New("FLOOD_WAIT")})
	if !strings.Contains(next.status(), "could not archive Dana") || cmd == nil {
		t.Errorf("status %q, reread %v", next.status(), cmd != nil)
	}
	if _, cmd := m.handleArchived(archivedMsg{label: "Dana", archived: true}); cmd != nil {
		t.Error("an archive that went through read the list again")
	}
	if _, cmd := m.Update(roomsChangedMsg{}); cmd == nil {
		t.Error("a network's rooms changing was not read again")
	}
}

// The filing picker offers only hand-filed tags, not rule-filled ones (Unread, DMs,
// Drafts, Invites, All).
func TestTheFilingPickerLeavesOutTagsTheirRuleFills(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{
		{Name: "All", Rule: []string{"*"}},
		{Name: "DMs", Rule: []string{"dm"}},
		{Name: "Unread", Rule: []string{"unread"}},
		{Name: "Drafts", Rule: []string{"draft"}},
		{Name: "Invites", Rule: []string{"invite"}},
		{Name: "Spam"},
		{Name: "Quiet", Rule: []string{"not unread"}},
		{Name: "Wanted", Rule: []string{"not tag:Spam"}},
		{Name: "Pinned"},
		{Name: "Archived"},
		{Name: "Work", Rule: []string{"space:Work"}},
		{Name: "Close", Rule: []string{"dm", "tag:Pinned"}},
	}}
	m := update(t, configured(cfg), roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "A"}}})
	m.focus = paneRooms
	m, _ = m.openSpacePicker()
	var offered []string
	for _, item := range m.picker.all {
		if isTagGroup(item.value) {
			name, _ := domain.TagOf(item.value)
			offered = append(offered, name)
		}
	}
	slices.Sort(offered)
	if want := []string{"Archived", "Close", "Pinned", "Spam", "Wanted", "Work"}; !slices.Equal(offered, want) {
		t.Errorf("offered %v, want %v", offered, want)
	}
}
