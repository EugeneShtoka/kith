package tui

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const markedMe = "@me:example.org"

// markedPool is the senders the generator draws from: Matrix users, two WhatsApp
// numbers each seen natively and as a bridge puppet, and you.
var markedPool = []string{
	"@ann:example.org", "@bob:example.org", "@cat:example.org", "telegram:1001",
	"whatsapp:15550000001@s.whatsapp.net", "@whatsapp_15550000001:example.org",
	"whatsapp:15550000002@s.whatsapp.net", "@whatsapp_15550000002:example.org",
	markedMe,
}

// markedCase is one generated world: who is linked to whom, which tags mark whom, and
// a room's messages.
type markedCase struct {
	identities []config.Identity
	names      []domain.PersonName
	links      []domain.PersonLink
	tags       []config.Tag
	verdicts   []domain.PersonVerdict
	msgs       []domain.Message
	shownHere  bool
	keys       config.TagKeys
}

func (c markedCase) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "identities %v\nnames %v\nlinks %v\n", c.identities, c.names, c.links)
	for i := range c.tags {
		fmt.Fprintf(&b, "tag %s muted=%v people=%v\n", c.tags[i].Name, !c.tags[i].Notifying(), c.tags[i].People)
	}
	for i := range c.msgs {
		fmt.Fprintf(&b, "msg %s from %s reply-to %q\n", c.msgs[i].ID, c.msgs[i].Sender, c.msgs[i].ReplyTo)
	}
	fmt.Fprintf(&b, "verdicts %v\nshown here %v", c.verdicts, c.shownHere)
	return b.String()
}

func genMarkedCase(rng *rand.Rand) markedCase {
	pick := func() string { return markedPool[rng.IntN(len(markedPool))] }
	var c markedCase
	grouped := map[string]bool{}
	for g := range rng.IntN(3) {
		alias := fmt.Sprintf("Person %d", g)
		if g == 0 && rng.IntN(4) == 0 {
			alias = "Me" // a group that is you: its other IDs are you too
		}
		// An ID belongs to one identity, as the in-app editor keeps it.
		ident := config.Identity{Alias: alias}
		for range 1 + rng.IntN(3) {
			if id := pick(); !grouped[id] {
				grouped[id] = true
				ident.IDs = append(ident.IDs, id)
			}
		}
		if len(ident.IDs) == 0 || (alias == "Me" && grouped[markedMe]) {
			continue
		}
		if alias == "Me" {
			grouped[markedMe] = true
			ident.IDs = append(ident.IDs, markedMe)
		}
		c.identities = append(c.identities, ident)
	}
	for range rng.IntN(3) {
		c.names = append(c.names, domain.PersonName{Source: "phonebook", ID: pick(), Name: "Someone", Rank: domain.RankSaved})
	}
	for range rng.IntN(3) {
		c.links = append(c.links, domain.PersonLink{Source: "phonebook", ID: pick(), Other: pick()})
	}
	for i := range 1 + rng.IntN(3) {
		tag := config.Tag{Name: fmt.Sprintf("Tag%d", i)}
		if rng.IntN(2) == 0 {
			off := false
			tag.Notifies = &off
		}
		for range rng.IntN(3) {
			tag.People = append(tag.People, pick())
		}
		c.tags = append(c.tags, tag)
	}
	for range rng.IntN(3) {
		c.verdicts = append(c.verdicts, genVerdict(rng, c.tags))
	}
	start := time.Unix(1700000000, 0)
	for i := range 4 + rng.IntN(8) {
		msg := domain.Message{
			ID: domain.EventID(fmt.Sprintf("$%d", i)), RoomID: "!a:x", Sender: pick(),
			Body: "hello", Timestamp: start.Add(time.Duration(i) * time.Minute),
		}
		switch r := rng.IntN(4); {
		case r == 0 && i > 0:
			msg.ReplyTo = domain.EventID(fmt.Sprintf("$%d", rng.IntN(i)))
		case r == 1:
			msg.ReplyTo = "$unloaded"
		}
		c.msgs = append(c.msgs, msg)
	}
	c.shownHere = rng.IntN(4) == 0
	return c
}

// genVerdict is a filter's mark of someone into one of tags, or a release.
func genVerdict(rng *rand.Rand, tags []config.Tag) domain.PersonVerdict {
	v := domain.PersonVerdict{Person: markedPool[rng.IntN(len(markedPool))], Tag: strings.ToLower(tags[rng.IntN(len(tags))].Name)}
	if rng.IntN(3) == 0 {
		v.Released = true
	} else {
		v.Rule, v.Filter = domain.MoveAlways, "crypto"
	}
	return v
}

// markedOracle answers who is hidden from the generated links alone: a union-find
// over every ID, with a number joining its directory person as domain.Directory says
// (an ID the directory knows joins its number; any other ID joins a number the
// directory knows).
type markedOracle struct {
	parent map[string]string
	marked map[string]bool
	me     map[string]bool
}

func (o markedOracle) find(id string) string {
	if _, ok := o.parent[id]; !ok {
		o.parent[id] = id
	}
	for o.parent[id] != id {
		id = o.parent[id]
	}
	return id
}

func (o markedOracle) union(a, b string) { o.parent[o.find(b)] = o.find(a) }

func newMarkedOracle(c markedCase) markedOracle {
	o := markedOracle{parent: map[string]string{}, marked: map[string]bool{}, me: map[string]bool{markedMe: true}}
	known := map[string]bool{}
	for _, n := range c.names {
		known[n.ID] = true
	}
	for _, l := range c.links {
		known[l.ID], known[l.Other] = true, true
		o.union(l.ID, l.Other)
	}
	numbers := map[string]bool{}
	for id := range known {
		if digits := domain.PhoneOf(id); digits != "" {
			numbers["tel:"+digits] = true
		}
	}
	for _, id := range markedPool {
		if digits := domain.PhoneOf(id); digits != "" && (known[id] || numbers["tel:"+digits]) {
			o.union(id, "tel:"+digits)
		}
	}
	for _, ident := range c.identities {
		for _, id := range ident.IDs[1:] {
			o.union(ident.IDs[0], id)
		}
		if slices.Contains(ident.IDs, markedMe) { // a group you are in is you
			for _, id := range ident.IDs {
				o.me[id] = true
			}
		}
	}
	for i := range c.tags {
		if !c.tags[i].Notifying() {
			for _, id := range c.tags[i].People {
				o.marked[o.find(id)] = true
			}
			for _, v := range c.verdicts {
				if v.In() && strings.EqualFold(v.Tag, c.tags[i].Name) {
					o.marked[o.find(v.Person)] = true
				}
			}
		}
	}
	return o
}

func (o markedOracle) hides(sender string) bool {
	return sender != "" && !o.me[sender] && o.marked[o.find(sender)]
}

// shown is what the timeline must draw.
func (o markedOracle) shown(c markedCase) []domain.EventID {
	senders := map[domain.EventID]string{}
	for i := range c.msgs {
		senders[c.msgs[i].ID] = c.msgs[i].Sender
	}
	var out []domain.EventID
	for i := range c.msgs {
		if m := &c.msgs[i]; c.shownHere || (!o.hides(m.Sender) && !o.hides(senders[m.ReplyTo])) {
			out = append(out, m.ID)
		}
	}
	return out
}

func markedModel(t *testing.T, c markedCase) Model {
	t.Helper()
	cfg := config.Config{Tags: c.tags}
	cfg.Keys.Tag = c.keys
	cfg.Display.Identities = c.identities
	m := sized(t, taggedModel(t, cfg))
	m.selves = []string{markedMe}
	m.dir = directoryState{dir: domain.NewDirectory(c.names, c.links), rev: 1}
	next, _ := m.selectRoom(m.rooms.all[0])
	m = next.setMessages(c.msgs)
	m = update(t, m, personVerdictsLoadedMsg{verdicts: c.verdicts})
	if c.shownHere {
		m.markedShownIn = m.openRoom
	}
	return m
}

// Whatever the links, tags and replies, the timeline hides exactly the marked people
// of muted tags and the direct replies to them, never you, unless the room shows them;
// and the cached answer follows each later change (the show key, a link learned, a
// leave and return).
func TestMarkedPeopleAreHiddenExactly(t *testing.T) {
	t.Parallel()

	for seed := range uint64(400) {
		rng := rand.New(rand.NewPCG(seed, 0x6d6b)) // #nosec G404 -- reproducible
		c := genMarkedCase(rng)
		m := markedModel(t, c)
		check := func(step string, c markedCase) {
			t.Helper()
			if got, want := shownIDs(m), newMarkedOracle(c).shown(c); !slices.Equal(got, want) {
				t.Fatalf("seed %d, %s: drew %v, want %v\n%v", seed, step, got, want, c)
			}
		}
		check("first", c)

		m, _ = m.toggleMarkedShown()
		c.shownHere = !c.shownHere
		check("after the show key", c)

		link := domain.PersonLink{Source: "later", ID: markedPool[rng.IntN(len(markedPool))], Other: markedPool[rng.IntN(len(markedPool))]}
		c.links = append(c.links, link)
		m = update(t, m, directoryMsg{dir: domain.NewDirectory(c.names, c.links)})
		check("after a link was learned", c)

		c.verdicts = append(slices.Clone(c.verdicts), genVerdict(rng, c.tags))
		m = update(t, m, personVerdictsLoadedMsg{verdicts: c.verdicts})
		check("after a verdict arrived", c)

		next, _ := m.selectRoom(m.rooms.all[1])
		next, _ = next.selectRoom(next.rooms.all[0])
		m = next.setMessages(c.msgs)
		c.shownHere = false
		check("after leaving and coming back", c)
	}
}

// A tag key in the timeline puts the selected message's sender in its tag, then takes
// every ID of theirs out of it; the cursor moves to a message still drawn.
func TestATagKeyFilesTheSenderAndTakesThemOut(t *testing.T) {
	t.Parallel()

	off := false
	c := markedCase{
		identities: []config.Identity{{Alias: "Bob", IDs: []string{"@bob:example.org", "telegram:1001"}}},
		tags: []config.Tag{
			{Name: "Loud", People: []string{"@cat:example.org"}},
			{Name: "Spam", Notifies: &off, People: []string{"telegram:1001"}},
		},
		msgs: []domain.Message{
			{ID: "$0", RoomID: "!a:x", Sender: "@ann:example.org", Body: "hi", Timestamp: time.Unix(1700000000, 0)},
			{ID: "$1", RoomID: "!a:x", Sender: "@cat:example.org", Body: "buy", Timestamp: time.Unix(1700000060, 0)},
			{ID: "$2", RoomID: "!a:x", Sender: "@ann:example.org", Body: "no", ReplyTo: "$1", Timestamp: time.Unix(1700000120, 0)},
			{ID: "$3", RoomID: "!a:x", Sender: "@ann:example.org", Body: "ok", Timestamp: time.Unix(1700000180, 0)},
			{ID: "$4", RoomID: "!a:x", Sender: "@bob:example.org", Body: "spam", Timestamp: time.Unix(1700000240, 0)},
		},
	}
	m := markedModel(t, c)
	if got := shownIDs(m); !slices.Equal(got, []domain.EventID{"$0", "$1", "$2", "$3"}) {
		t.Fatalf("drew %v, want Bob hidden by his Telegram ID", got)
	}
	m.timeline.selected = "$1"
	m, _ = m.toggleSenderIn("spam")
	if got := m.conf.base.Tags[1].People; !slices.Equal(got, []string{"telegram:1001", "@cat:example.org"}) {
		t.Fatalf("Spam holds %v, want the sender added", got)
	}
	if got := shownIDs(m); !slices.Equal(got, []domain.EventID{"$0", "$3"}) {
		t.Fatalf("drew %v, want the sender and the reply to them hidden", got)
	}
	if m.timeline.selected != "$3" {
		t.Errorf("cursor on %q, want the next message still drawn", m.timeline.selected)
	}

	// Bob is in Spam as his Telegram ID; the key on his Matrix message takes him out.
	m, _ = m.toggleMarkedShown()
	m.timeline.selected = "$4"
	m, _ = m.toggleSenderIn("Spam")
	if got := m.conf.base.Tags[1].People; !slices.Equal(got, []string{"@cat:example.org"}) {
		t.Errorf("Spam holds %v after Bob was taken out", got)
	}
	if got := m.conf.base.Tags[0].People; !slices.Equal(got, []string{"@cat:example.org"}) {
		t.Errorf("another tag's people changed: %v", got)
	}
}

// personMarker records the person marks and releases it is asked to write.
type personMarker struct {
	apitest.Nop

	mu       sync.Mutex
	released []domain.PersonVerdict
}

func (b *personMarker) MarkPerson(_ context.Context, v domain.PersonVerdict) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.released = append(b.released, v)
	return nil
}

// A tag key on someone a filter marked releases every mark that is them, at once here,
// so no filter puts them back; the config is left alone when it never listed them.
func TestATagKeyReleasesAFiltersMark(t *testing.T) {
	t.Parallel()

	off := false
	c := markedCase{
		identities: []config.Identity{{Alias: "Bob", IDs: []string{"@bob:example.org", "telegram:1001"}}},
		tags:       []config.Tag{{Name: "Spam", Notifies: &off}, {Name: "Work"}},
		verdicts: []domain.PersonVerdict{
			{Person: "telegram:1001", Tag: "spam", Rule: domain.MoveAlways, Filter: "crypto"},
			{Person: "@bob:example.org", Tag: "spam", Rule: domain.MoveAlways, Filter: "links"},
			{Person: "@bob:example.org", Tag: "work", Rule: domain.MoveAlways, Filter: "quotes"},
		},
		msgs: []domain.Message{
			{ID: "$0", RoomID: "!a:x", Sender: "@ann:example.org", Body: "hi", Timestamp: time.Unix(1700000000, 0)},
			{ID: "$1", RoomID: "!a:x", Sender: "@bob:example.org", Body: "buy", Timestamp: time.Unix(1700000060, 0)},
		},
	}
	m := markedModel(t, c)
	backend := &personMarker{}
	m.backend = backend
	if got := shownIDs(m); !slices.Equal(got, []domain.EventID{"$0"}) {
		t.Fatalf("drew %v, want Bob hidden by a filter's mark", got)
	}
	m, _ = m.toggleMarkedShown()
	m.timeline.selected = "$1"
	m, cmd := m.toggleSenderIn("Spam")
	m, _ = m.toggleMarkedShown()
	if got := shownIDs(m); !slices.Equal(got, []domain.EventID{"$0", "$1"}) {
		t.Errorf("drew %v, want Bob back at once", got)
	}
	if got := m.conf.base.Tags[0].People; len(got) != 0 {
		t.Errorf("Spam lists %v, want the config untouched", got)
	}
	deliver(t, m, cmd)
	released := map[string]bool{}
	for _, v := range backend.released {
		if !v.Released || !strings.EqualFold(v.Tag, "spam") {
			t.Errorf("wrote %+v, want only releases from Spam", v)
		}
		released[v.Person] = true
	}
	if !released["telegram:1001"] || !released["@bob:example.org"] || len(backend.released) != 2 {
		t.Errorf("released %v, want both of Bob's Spam marks", backend.released)
	}
}

// You cannot put yourself in a tag; a tag that notifies lists a person but hides nothing.
func TestATagKeyRefusesYouAndHidesOnlyWhenMuted(t *testing.T) {
	t.Parallel()

	msgs := []domain.Message{
		{ID: "$0", RoomID: "!a:x", Sender: markedMe, Body: "mine", Timestamp: time.Unix(1700000000, 0)},
		{ID: "$1", RoomID: "!a:x", Sender: "@ann:example.org", Body: "hers", Timestamp: time.Unix(1700000060, 0)},
	}
	off := false
	m := markedModel(t, markedCase{tags: []config.Tag{{Name: "Spam", Notifies: &off}}, msgs: msgs})
	m.timeline.selected = "$0"
	m, _ = m.toggleSenderIn("Spam")
	if got := m.conf.base.Tags[0].People; len(got) != 0 {
		t.Errorf("filed yourself: %v", got)
	}

	m = markedModel(t, markedCase{tags: []config.Tag{{Name: "Work"}}, msgs: msgs})
	m.timeline.selected = "$1"
	m, _ = m.toggleSenderIn("Work")
	if got := m.conf.base.Tags[0].People; !slices.Equal(got, []string{"@ann:example.org"}) {
		t.Errorf("Work holds %v", got)
	}
	if got := shownIDs(m); !slices.Equal(got, []domain.EventID{"$0", "$1"}) {
		t.Errorf("a tag that notifies hid %v", got)
	}
}

// ! files the sender from the timeline and the room from the room list; z h shows.
func TestTheTagKeyActsOnWhatThePaneShows(t *testing.T) {
	t.Parallel()

	off := false
	c := markedCase{
		tags: []config.Tag{{Name: "Spam", Notifies: &off}},
		msgs: []domain.Message{{ID: "$0", RoomID: "!a:x", Sender: "@ann:example.org", Body: "hi", Timestamp: time.Unix(1700000000, 0)}},
	}
	c.keys = config.TagKeys{{Chord: "!", Tag: "Spam"}}
	m := markedModel(t, c)
	m.focus = paneTimeline
	m.timeline.selected = "$0"
	m = update(t, m, tea.KeyPressMsg{Code: '!', Text: "!"})
	if got := m.conf.base.Tags[0]; !slices.Equal(got.People, []string{"@ann:example.org"}) || len(got.Picked) != 0 {
		t.Fatalf("! in the timeline filed people %v, rooms %v", got.People, got.Picked)
	}
	m = update(t, m, tea.KeyPressMsg{Code: 'z', Text: "z"})
	m = update(t, m, tea.KeyPressMsg{Code: 'h', Text: "h"})
	if !m.markedShownHere() {
		t.Error("z h did not show the hidden people here")
	}
	m.focus = paneRooms
	m = update(t, m, tea.KeyPressMsg{Code: '!', Text: "!"})
	if got := m.conf.base.Tags[0].Picked; len(got) != 1 {
		t.Errorf("! in the room list filed rooms %v, want the room", got)
	}
}
