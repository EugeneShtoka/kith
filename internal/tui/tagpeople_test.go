package tui

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// onlyMuted is the oracle for one tag: what it would hide were it the only muted one.
func onlyMuted(c markedCase, tag int) markedOracle {
	only := c
	only.tags = slices.Clone(c.tags)
	for i := range only.tags {
		notifies := i != tag
		only.tags[i].Notifies = &notifies
	}
	return newMarkedOracle(only)
}

// listedIn is the pool's IDs a tag's people list holds: an ID is on a row when it is that
// row's person (an account found only by a number the directory has is theirs too), and
// it is an error to be on two rows.
func listedIn(m Model, tag string) ([]string, error) {
	rows := m.tagPeopleRows(tag)
	var out []string
	for _, id := range markedPool {
		on := 0
		for _, row := range rows {
			if domain.NewMarkedPeople([]string{row.id}, m.identityIDs(), m.linkedIDs()).Has(id) && !m.fromMe(id) {
				on++
			}
		}
		if on > 1 {
			return nil, fmt.Errorf("%s is on %d rows", id, on)
		}
		if on == 1 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// checkTagPeople fails unless every tag's people list holds exactly whom the tag hides
// (or would, were it muted), one row per person.
func checkTagPeople(t *testing.T, m Model, c markedCase, step string) {
	t.Helper()
	for i := range c.tags {
		o := onlyMuted(c, i)
		var hidden []string
		for _, id := range markedPool {
			if o.hides(id) {
				hidden = append(hidden, id)
			}
		}
		slices.Sort(hidden)
		listed, err := listedIn(m, c.tags[i].Name)
		if err != nil || !slices.Equal(listed, hidden) {
			t.Fatalf("%s: %s lists %v (%v), hides %v\n%v", step, c.tags[i].Name, listed, err, hidden, c)
		}
		// Linked IDs are one row.
		rows := m.tagPeopleRows(c.tags[i].Name)
		rowOf := func(id string) int {
			return slices.IndexFunc(rows, func(r tagPerson) bool {
				return domain.NewMarkedPeople([]string{r.id}, m.identityIDs(), m.linkedIDs()).Has(id)
			})
		}
		// And no row is somebody it does not hide (you, for one).
		for r := range rows {
			if !slices.ContainsFunc(listed, func(id string) bool { return rowOf(id) == r }) {
				t.Fatalf("%s: row %s in %s hides nobody\n%v", step, rows[r].id, c.tags[i].Name, c)
			}
		}
		for _, a := range listed {
			for _, b := range listed {
				ra, rb := rowOf(a), rowOf(b)
				if (o.find(a) == o.find(b)) != (ra == rb) {
					t.Fatalf("%s: %s and %s are rows %d and %d in %s\n%v", step, a, b, ra, rb, c.tags[i].Name, c)
				}
			}
		}
	}
}

// showingPeopleOf puts the rail on tag and switches its room list to its people.
func showingPeopleOf(t *testing.T, m Model, tag string) Model {
	t.Helper()
	key := tagGroupKey(tag)
	at := slices.IndexFunc(m.rail.groups, func(g group) bool { return g.key == key })
	if at < 0 {
		t.Fatalf("no rail row %s", key)
	}
	m.rail.cursor = at
	m.focus = paneRooms
	if !m.showingTagPeople() {
		m, _ = m.toggleTagPeople()
	}
	return m
}

// A tag's people list is exactly whom it hides, one row per person, whatever the links,
// the hand-listed people and the filters' marks — and stays so as a link is learned, a
// mark or release arrives from another machine, and a person is taken out from the list.
func TestATagListsExactlyThePeopleItHides(t *testing.T) {
	t.Parallel()

	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 0x7470)) // #nosec G404 -- reproducible
		c := genMarkedCase(rng)
		c.shownHere = false
		m := markedModel(t, c)
		checkTagPeople(t, m, c, fmt.Sprintf("seed %d first", seed))

		link := domain.PersonLink{Source: "later", ID: markedPool[rng.IntN(len(markedPool))], Other: markedPool[rng.IntN(len(markedPool))]}
		c.links = append(c.links, link)
		m = update(t, m, directoryMsg{dir: domain.NewDirectory(c.names, c.links)})
		checkTagPeople(t, m, c, fmt.Sprintf("seed %d after a link was learned", seed))

		c.verdicts = append(slices.Clone(c.verdicts), genVerdict(rng, c.tags))
		m = update(t, m, personVerdictsLoadedMsg{verdicts: c.verdicts})
		checkTagPeople(t, m, c, fmt.Sprintf("seed %d after another machine's verdict", seed))

		// Out, by the tag's key on its people list.
		ti := rng.IntN(len(c.tags))
		rows := m.tagPeopleRows(c.tags[ti].Name)
		if len(rows) == 0 {
			continue
		}
		pick := rows[rng.IntN(len(rows))]
		m = showingPeopleOf(t, m, c.tags[ti].Name)
		m.tagPeople.cursor = slices.IndexFunc(m.shownTagPeople(), func(r tagPerson) bool { return r.id == pick.id })
		m, _ = m.takeTagPersonOut(c.tags[ti].Name)

		o := newMarkedOracle(c)
		them := o.find(pick.id)
		c.tags = slices.Clone(c.tags)
		c.tags[ti].People = slices.DeleteFunc(slices.Clone(c.tags[ti].People), func(id string) bool { return o.find(id) == them })
		c.verdicts = slices.Clone(c.verdicts)
		for i := range c.verdicts {
			if v := &c.verdicts[i]; v.In() && strings.EqualFold(v.Tag, c.tags[ti].Name) && o.find(v.Person) == them {
				v.Released = true
			}
		}
		if got := m.conf.base.Tags[ti].People; !slices.Equal(got, c.tags[ti].People) {
			t.Fatalf("seed %d: after taking %s out, %s lists %v, want %v\n%v", seed, pick.id, c.tags[ti].Name, got, c.tags[ti].People, c)
		}
		checkTagPeople(t, m, c, fmt.Sprintf("seed %d after %s was taken out of %s", seed, pick.id, c.tags[ti].Name))
	}
}

// tagPeopleBackend counts each person's messages as the number of their IDs.
type tagPeopleBackend struct{ apitest.Nop }

func (tagPeopleBackend) MessageCounts(_ context.Context, people [][]string) ([]int, error) {
	out := make([]int, len(people))
	for i := range people {
		out[i] = 10 + len(people[i])
	}
	return out, nil
}

// On a tag the room list switches to its people and back; a row says how they got there
// and how much they wrote; enter is their card and the said key what they said.
func TestATagsRoomListSwitchesToItsPeople(t *testing.T) {
	t.Parallel()

	muted := false
	cfg := config.Config{Tags: []config.Tag{{Name: "Spam", Notifies: &muted, People: []string{"matrix:@bob:example.org"}}}}
	cfg.Display.Identities = []config.Identity{{Alias: "Bob", IDs: []string{"matrix:@bob:example.org", "telegram:1001"}}}
	m := sized(t, withRooms(t, New(context.Background(), tagPeopleBackend{}, cfg.Display, config.DeviceDisplay{}).WithConfigFile("", asStarted(cfg))))
	m = update(t, m, personVerdictsLoadedMsg{verdicts: []domain.PersonVerdict{
		{Person: "matrix:@cat:example.org", Tag: "spam", Rule: domain.MoveAlways, Filter: "crypto", At: at(86400 * 400)},
	}})
	key := tagGroupKey("Spam")
	m.rail.cursor = slices.IndexFunc(m.rail.groups, func(g group) bool { return g.key == key })
	m.focus = paneRooms

	m, cmd := chord(t, m, "g", "p")
	if !m.showingTagPeople() {
		t.Fatal("gp did not switch Spam's room list to its people")
	}
	m = deliver(t, m, cmd)
	pane := stripStyles(m.renderRooms(40, 20))
	for _, want := range []string{"Spam · 2 people", "Bob", "12", "Matrix, Telegram · you", "filter crypto"} {
		if !strings.Contains(pane, want) {
			t.Errorf("the people list is missing %q:\n%s", want, pane)
		}
	}
	m, cmd = press(t, m, keyCode(tea.KeyEnter))
	if !strings.Contains(m.status(), "Bob") || cmd == nil {
		t.Errorf("enter on Bob read no card (status %q)", m.status())
	}
	m, cmd = chord(t, m, "g", "I")
	if !m.said.open || m.said.id != "matrix:@bob:example.org" || cmd == nil {
		t.Errorf("gI opened %+v, want what Bob said", m.said)
	}
	m.said = saidState{}
	m.focus = paneRooms
	// The tag's key takes the person under the cursor out: Bob, listed by hand.
	m, _ = press(t, m, keyText("!"))
	if got := m.conf.base.Tags[0].People; len(got) != 0 {
		t.Errorf("Spam still lists %v after its key on Bob", got)
	}
	if rows := m.shownTagPeople(); len(rows) != 1 || rows[0].id != "matrix:@cat:example.org" {
		t.Errorf("rows after taking Bob out: %+v", rows)
	}
	m, _ = chord(t, m, "g", "p")
	if m.showingTagPeople() {
		t.Error("gp again did not switch back to the rooms")
	}
	// Elsewhere there is nobody to list.
	m.rail.cursor = slices.IndexFunc(m.rail.groups, func(g group) bool { return !isTagGroup(g.key) })
	if m, _ = m.toggleTagPeople(); m.showingTagPeople() {
		t.Errorf("on %s: showing=%v status %q", m.rail.key(), m.showingTagPeople(), m.status())
	}
}

// Settings lists a tag's people beside its rooms, and the filters' marks read-only.
func TestATagsPageListsItsPeople(t *testing.T) {
	t.Parallel()

	muted := false
	cfg := config.Config{Tags: []config.Tag{{Name: "Spam", Notifies: &muted, People: []string{"matrix:@bob:example.org"}}}}
	m := sized(t, withRooms(t, New(context.Background(), apitest.Nop{}, cfg.Display, config.DeviceDisplay{}).WithConfigFile("", asStarted(cfg))))
	m = update(t, m, personVerdictsLoadedMsg{verdicts: []domain.PersonVerdict{
		{Person: "matrix:@cat:example.org", Tag: "spam", Rule: domain.MoveAlways, Filter: "crypto"},
	}})
	rows := m.tagPageRows("Spam", "place:tag:Spam")
	shown := map[string]string{}
	for _, r := range rows {
		shown[r.label] = stripIsolates(r.show(m.settings()))
	}
	if got := shown["People"]; !strings.HasPrefix(got, "1: ") || !strings.Contains(got, "bob") {
		t.Errorf("People shows %q, want Bob", got)
	}
	if got := shown["Put here by filters"]; !strings.Contains(got, "(crypto)") {
		t.Errorf("filters' marks show %q, want Cat by crypto", got)
	}
}
