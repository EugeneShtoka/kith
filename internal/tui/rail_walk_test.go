package tui

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The memoized rail must be keyed by everything BuildRail reads. This walks random
// sequences of every rail input: unread (counts, highlights, marks), drafts, the device's
// unread source, a profile switch, a config reload changing the tags, the rail's order,
// hidden and hide_when_empty and the rows' names, invitations, a network archiving a room,
// the archived toggle, a chat a link opened, spaces, canonical parents and moving the
// cursor. A frame is drawn between most steps (filling the memo), and asModel's staleRail
// check fails the first Update leaving the memo unlike a fresh build.
func TestTheRailMemoHoldsOverAnyUpdateSequence(t *testing.T) {
	t.Parallel()
	seeds, steps := 60, 40
	if testing.Short() {
		seeds = 20
	}
	for seed := range seeds {
		walkRailInputs(t, uint64(seed), steps)
	}
}

// The walk's rooms: two Matrix rooms (one direct), a Telegram chat its network can
// archive, and a room in both spaces; two invitations; a chat only a link opens.
var (
	walkAlpha  = domain.Room{ID: mx + "!a:x", Name: "Alpha"}
	walkBravo  = domain.Room{ID: mx + "!b:x", Name: "Bravo", IsDirect: true}
	walkTele   = domain.Room{ID: "telegram:1/100", Name: "Tele"}
	walkShared = domain.Room{ID: mx + "!w:x", Name: "Shared"}
	walkFresh  = domain.Room{ID: "telegram:1/200", Name: "Fresh", IsDirect: true}
	walkInvite = []domain.Room{invited(mx+"!i1:x", "Inv one", "matrix:@ann:x"), invited(mx+"!i2:x", "Inv two", "matrix:@bo:x")}
)

// walkTags is the tags in one of a few shapes: every state word, a silent tag, exclusive
// ones, hidden and empty-hiding rows.
func walkTags(shape int) []config.Tag {
	no := false
	tags := []config.Tag{
		{Name: "Invites", Rule: []string{"invite"}, First: true, CountInLabel: true, HideWhenEmpty: true},
		{Name: "Unread", Rule: []string{"unread"}, Sticky: true},
		{Name: "Mentions", Rule: []string{"mention"}, Exclusive: []string{config.ExclusiveTags}},
		{Name: "Drafts", Rule: []string{"draft"}, HideWhenEmpty: true},
		{Name: "Quiet", Rule: []string{"room:Bravo"}, CountsUnread: &no, Exclusive: []string{config.ExclusiveSpaces}},
		{Name: "Archived", Rule: []string{"space:Nowhere"}, CountsUnread: &no,
			Exclusive: []string{config.ExclusiveTags, config.ExclusiveOrigin}, HideWhenEmpty: true},
		{Name: "All", Rule: []string{"*"}},
	}
	switch shape {
	case 1:
		tags[1].Exclusive = []string{config.ExclusiveTags}
		tags[2].Hidden = true
	case 2:
		tags[4].Rule = []string{"unread", "not dm"}
		tags[3].HideWhenEmpty = false
	}
	return tags
}

// walkRails is [display.rail] in a few shapes: dividers, the wildcard, place: entries,
// hidden rows and rows hidden while empty.
var walkRails = []config.Rail{
	{Order: []string{"tag:Unread", "-", "*", "-", "tag:Archived"}, HideWhenEmpty: []string{"tag:Unread"}},
	{Order: []string{"place:Job", "tag:All", "--", "*"}, Hidden: []string{"tag:Mentions"}},
	{},
	{Order: []string{"*", "*"}, Hidden: []string{"space:Bridge"}, HideWhenEmpty: []string{"place:Drafts", "place:Mentions"}},
}

// walkConfig is the config in one shape: tags, rail, the Work space renamed or not, and two
// profiles, the second with its own archive and rail.
func walkConfig(shape int) config.Config {
	cfg := config.Config{
		Tags:    walkTags(shape % 3),
		Display: config.Display{Rail: walkRails[shape%len(walkRails)]},
		Profiles: []config.Profile{
			{Name: "home", Include: []string{"*"}},
			{
				Name: "work", Include: []string{"*"}, Archive: config.ProfileArchive{Rooms: []string{"room:Alpha"}},
				Rail: walkRails[(shape+1)%len(walkRails)],
			},
		},
	}
	if shape%2 == 1 {
		cfg.Display.Names = []config.DisplayName{{Target: config.GroupTarget("Work"), Name: "Job"}}
	}
	return cfg
}

// walkSpaces is the spaces in one of a few shapes: a space a person made, one a bridge
// keeps, holding different rooms.
func walkSpaces(shape int) []domain.Space {
	work := domain.Space{ID: mx + "!sw:x", Name: "Work", Bridge: domain.ProtocolMatrix, Children: []domain.RoomID{walkAlpha.ID, walkShared.ID}}
	bridge := domain.Space{ID: mx + "!sb:x", Name: "Bridge", Keeper: "matrix:@bot:x", Children: []domain.RoomID{walkShared.ID, walkBravo.ID, walkTele.ID}}
	switch shape {
	case 1:
		work.Children = append(work.Children, walkTele.ID, walkInvite[0].ID)
	case 2:
		return []domain.Space{bridge}
	case 3:
		return nil
	}
	return []domain.Space{work, bridge}
}

func walkRailInputs(t *testing.T, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x7261696c))
	var log []string
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("seed %d: %v\nsteps:\n  %s", seed, r, strings.Join(log, "\n  "))
		}
	}()

	rooms := []domain.Room{walkAlpha, walkBravo, walkTele, walkShared}
	b := &profiledBackend{rooms: map[string][]domain.Room{"": rooms, "home": rooms, "work": rooms}}
	for shape := range 8 {
		for _, profile := range []string{"home", "work"} {
			if _, err := derive(config.Settings{Bridge: walkConfig(shape)}, profile); err != nil {
				t.Fatalf("the walk's config shape %d does not run in %s: %v", shape, profile, err)
			}
		}
	}
	cfg := walkConfig(0)
	m := sized(t, starterNew(b, config.Display{}))
	m = m.WithConfigFile("", cfg).WithProfiles(b, "")
	m = update(t, m, roomsMsg{rooms: rooms})
	m = update(t, m, spacesMsg{spaces: walkSpaces(0)})
	archived := false
	local := true
	all := append(slices.Clone(rooms), walkInvite...)
	room := func() domain.Room { return all[rng.IntN(len(all))] }

	for range steps {
		var msg tea.Msg = settleMsg{}
		switch rng.IntN(15) {
		case 0, 1:
			r := room()
			u := domain.Unread{RoomID: r.ID, Notifications: rng.IntN(3), Highlights: rng.IntN(2),
				Messages: rng.IntN(3), Mentions: rng.IntN(2), Counted: rng.IntN(4) > 0, Marked: rng.IntN(4) == 0}
			msg = unreadUpdateMsg{u: u}
			log = append(log, fmt.Sprintf("unread %+v", u))
		case 2:
			r := room()
			d := draft{}
			if rng.IntN(2) == 0 {
				d.input = "x"
			}
			m.drafts = setDraft(m.drafts, r.ID, d)
			log = append(log, fmt.Sprintf("draft in %s: %q", r.ID, d.input))
		case 3:
			local = !local
			dev := m.conf.device
			dev.Display.Unread = map[bool]string{true: config.UnreadMessages, false: config.UnreadNotifications}[local]
			m, _ = m.applyDevice(dev, "")
			log = append(log, "unread counts "+dev.Display.Unread)
		case 4:
			name := []string{"home", "work"}[rng.IntN(2)]
			msg = profileSwitchedMsg{name: name}
			log = append(log, "profile "+name)
		case 5:
			shape := rng.IntN(8)
			cfg = walkConfig(shape)
			m, _ = m.adoptConfig(cfg, "")
			log = append(log, fmt.Sprintf("config shape %d", shape))
		case 6:
			var invites []domain.Room
			for i := range walkInvite {
				if rng.IntN(2) == 0 {
					invites = append(invites, walkInvite[i])
				}
			}
			msg = inviteUpdateMsg{invites: invites}
			log = append(log, fmt.Sprintf("%d invitations", len(invites)))
		case 7:
			archived = !archived
			tele := walkTele
			tele.Archived = archived
			msg = roomsMsg{rooms: []domain.Room{walkAlpha, walkBravo, tele, walkShared}}
			log = append(log, fmt.Sprintf("telegram archived %v", archived))
		case 8:
			m, _ = m.toggleArchived()
			log = append(log, "toggle archived")
		case 9:
			msg = linkResolvedMsg{uri: "tg://resolve?domain=fresh", target: domain.LinkTarget{Room: walkFresh}}
			log = append(log, "a link opens a fresh chat")
		case 10:
			shape := rng.IntN(4)
			msg = spacesMsg{spaces: walkSpaces(shape)}
			log = append(log, fmt.Sprintf("spaces shape %d", shape))
		case 11:
			r := room()
			parent := []domain.SpaceID{mx + "!sw:x", mx + "!sb:x"}[rng.IntN(2)]
			msg = parentsMsg{parents: map[domain.RoomID]domain.SpaceID{r.ID: parent}}
			log = append(log, fmt.Sprintf("%s's home is %s", r.ID, parent))
		case 12:
			m.focus = []pane{paneRail, paneRooms}[rng.IntN(2)]
			key := []rune{'j', 'k'}[rng.IntN(2)]
			msg = tea.KeyPressMsg{Code: key, Text: string(key)}
			log = append(log, fmt.Sprintf("%c in %v", key, m.focus))
		case 13:
			msg = roomsMsg{rooms: rooms}
			log = append(log, "the rooms are listed again")
		default:
			log = append(log, "draw")
		}
		m, _ = routed(t, m, msg)
		if rng.IntN(3) > 0 {
			_ = m.View() // fills the memo, as a frame does
		}
	}
}

// Every input the rail is built from is in the memo's key: changing any one alone, which
// the walk's Updates never do (a refresh of the facts goes with most), rebuilds the rail.
// Each change is checked to change the rail, so none passes by changing nothing.
func TestTheRailMemoKeyHoldsEachInputAlone(t *testing.T) {
	t.Parallel()
	tagsOf := func(shape int) domain.TagSet {
		set, _, err := setup.Tags(config.Config{Tags: walkTags(shape)})
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	spaces := walkSpaces(0)
	spaces[0].Children = append(slices.Clone(spaces[0].Children), walkBravo.ID)
	rooms := []domain.Room{walkAlpha, walkBravo, walkTele, walkShared}
	set := tagsOf(0)
	places := domain.Places{Tags: set, Archives: map[domain.Protocol]string{domain.ProtocolTelegram: "Archived"}}
	factsOf := func(rooms []domain.Room) map[domain.RoomID]domain.RoomFacts {
		out := map[domain.RoomID]domain.RoomFacts{}
		for i := range rooms {
			out[rooms[i].ID] = places.Facts(rooms[i], domain.HoldersOf(rooms[i].ID, spaces))
		}
		return out
	}
	base := unreadView{
		tags: set, tagsRev: 1, local: true,
		counts:    map[domain.RoomID]domain.Unread{walkAlpha.ID: {RoomID: walkAlpha.ID, Messages: 1, Counted: true}},
		roomFacts: factsOf(rooms),
		rail:      railInputs{spaces: spaces, rooms: rooms, cfg: walkRails[0]},
	}
	archived := slices.Clone(rooms)
	archived[2].Archived = true
	for _, c := range []struct {
		input  string
		change func(v unreadView) unreadView
	}{
		{"the tags", func(v unreadView) unreadView { v.tags, v.tagsRev = tagsOf(1), v.tagsRev+1; return v }},
		{"the unread source", func(v unreadView) unreadView { v.local = false; return v }},
		{"unread", func(v unreadView) unreadView {
			v.counts = withEntry(v.counts, walkTele.ID, domain.Unread{RoomID: walkTele.ID, Highlights: 1, Notifications: 1})
			return v
		}},
		{"drafts", func(v unreadView) unreadView {
			v.drafts = setDraft(v.drafts, walkShared.ID, draft{input: "x"})
			return v
		}},
		{"facts", func(v unreadView) unreadView { v.roomFacts = factsOf(archived); return v }},
		{"parents", func(v unreadView) unreadView {
			v.parents = map[domain.RoomID]domain.SpaceID{walkBravo.ID: spaces[0].ID}
			return v
		}},
		{"rooms", func(v unreadView) unreadView { v.rail.rooms = slices.Clone(rooms[:3]); return v }},
		{"spaces", func(v unreadView) unreadView { v.rail.spaces = walkSpaces(2); return v }},
		{"names", func(v unreadView) unreadView {
			v.rail.names = []config.DisplayName{{Target: config.GroupTarget("Work"), Name: "Job"}}
			return v
		}},
		{"order", func(v unreadView) unreadView { v.rail.cfg.Order = []string{"*", "-", "tag:All"}; return v }},
		{"hidden", func(v unreadView) unreadView { v.rail.cfg.Hidden = []string{"tag:All"}; return v }},
		{"hide_when_empty", func(v unreadView) unreadView { v.rail.cfg.HideWhenEmpty = []string{"tag:Mentions"}; return v }},
	} {
		v := base
		v.railMemo = &railMemo{}
		was := v.builtRail() // fills the memo
		changed := c.change(v)
		fresh := changed.buildRail()
		if sameBuilt(was, fresh) == nil {
			t.Errorf("changing %s changes nothing the rail is built from", c.input)
			continue
		}
		if err := sameBuilt(changed.builtRail(), fresh); err != nil {
			t.Errorf("after changing %s alone, the memo: %v", c.input, err)
		}
	}
}
