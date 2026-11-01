package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// The people: Dana, a Matrix user the directory links to her WhatsApp number, whose
// card is written by hand (comments, the tags first, a body); Eli, a family attorney
// with a second card; Ori, an attorney by email; Pia, by her number; Noa, on Telegram; Gil, whom the directory names and no card holds. "home" holds every
// room and hides no one; "work" holds the groups and hides attorneys by their card and
// Noa by her ID.
const (
	danaMX = "matrix:@dana:example.org"
	danaWA = "whatsapp:15550100001@s.whatsapp.net"
	eliMX  = "matrix:@eli:example.org"
	noaTG  = "telegram:77"
	gilTG  = "telegram:99"
	danaDM = mx + "!dm:x"
)

const danaByHand = `---
tags: [person] # kith's
# about her
pets: [Rex]
matrix: ["@dana:example.org"]
origin: Haifa
---
Met at the hiking club.
`

var peopleCards = map[string]string{
	"Dana Levi.md": danaByHand,
	"Eli Stone.md": "---\nmatrix: [\"@eli:example.org\"]\nrole: Family Attorney\nfirm: Stone and Co\n---\n",
	"Noa.md":       "---\ntelegram: [\"77\"]\nrole: architect\n---\n",
	// Eli's second card, after the first by name: his person is hidden, so it is too.
	"Eli.md": "---\nmatrix: [\"@eli:example.org\"]\nnickname: E\n---\n",
	// An attorney kith reaches no one by: hidden by the card alone.
	"Ori.md": "---\nemail: [ori@example.com]\nrole: attorney\n---\n",
	// Yours, an attorney too: never hidden.
	"Me.md": "---\nmatrix: [\"@me:example.org\"]\nrole: attorney\n---\n",
	// The number of a WhatsApp ID work hides, which the directory does not know.
	"Pia.md":   "---\nphone: [\"+15550100003\"]\n---\n",
	"notes.md": "# not a card\n",
}

// cardWorld is a backend whose directory knows the people, with a DM with Dana and a
// group of several.
type cardWorld struct{ apitest.Nop }

func (cardWorld) Directory(context.Context) (domain.Directory, error) {
	return domain.NewDirectory(
		[]domain.PersonName{{Source: "t", ID: danaMX, Name: "Dana Levi"}, {Source: "t", ID: gilTG, Name: "Gil Amir"}},
		[]domain.PersonLink{{Source: "t", ID: danaWA, Other: danaMX}},
	), nil
}

func (cardWorld) Selves(context.Context) ([]string, error) { return []string{me}, nil }

var peopleRooms = []domain.Room{
	{ID: danaDM, Name: "Dana Levi", IsDirect: true, Members: []string{"Dana Levi"}},
	{ID: mx + "!group:x", Name: "Hikers", Members: []string{"Dana", "Eli", "Noa"}},
}

var peopleMembers = map[domain.RoomID][]domain.Member{
	danaDM:          {{UserID: me, DisplayName: "Me"}, {UserID: danaMX, DisplayName: "Dana Levi"}, {UserID: "matrix:@whatsappbot:example.org"}},
	mx + "!group:x": {{UserID: me}, {UserID: danaMX}, {UserID: eliMX}},
}

func (cardWorld) Rooms(context.Context) ([]domain.Room, error) { return peopleRooms, nil }

func (cardWorld) Members(_ context.Context, room domain.RoomID, _ int) ([]domain.Member, error) {
	return peopleMembers[room], nil
}

func (cardWorld) RoomsWith(_ context.Context, ids []string, set domain.RoomSet, _ int) ([]domain.Room, error) {
	var out []domain.Room
	for i := range peopleRooms {
		id := peopleRooms[i].ID
		in := slices.ContainsFunc(peopleMembers[id], func(m domain.Member) bool { return slices.Contains(ids, m.UserID) })
		if in && (set.All || slices.Contains(set.IDs, id)) {
			out = append(out, peopleRooms[i])
		}
	}
	return out, nil
}

// phonebookServed is kithd over a config naming a folder holding cards, with the home
// and work profiles; it answers the folder and a client for a profile.
func phonebookServed(t *testing.T, cards map[string]string) (string, func(profile string) people) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "phonebook")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range cards {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		Phonebook: config.Phonebook{Dir: dir},
		Profiles: []config.Profile{
			{Name: "home", Include: []string{"*"}},
			{Name: "work", Include: []string{"group"}, People: config.ProfilePeople{Exclude: []string{"role:attorney", noaTG, "telegram:55", "whatsapp:15550100003@s.whatsapp.net"}}},
		},
	}
	path := filepath.Join(root, "config.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	n, err := daemon.NewNotifications(cfg, scopeOnly{cardWorld{}})
	if err != nil {
		t.Fatal(err)
	}
	h := serve(t, cardWorld{Nop: nopWithChannels()}, func(d *daemon.Daemon) {
		d.Notifications = n
		d.Config = daemon.NewConfigFile(path)
	})
	c := daemon.Client(h.path)
	return dir, func(profile string) people { return people{c: c, profile: profile} }
}

// people is the phonebook's calls in one profile.
type people struct {
	c       backendv1connect.BackendServiceClient
	profile string
}

func inProfile[T any](profile string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set("Kith-Profile", profile)
	return r
}

func (p people) list(ctx context.Context, query string) (*v1.PeopleResponse, error) {
	r, err := p.c.People(ctx, inProfile(p.profile, &v1.PeopleRequest{Query: query}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (p people) card(ctx context.Context, asked *v1.PersonCardRequest) (*v1.PersonCardResponse, error) {
	r, err := p.c.PersonCard(ctx, inProfile(p.profile, asked))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (p people) set(ctx context.Context, asked *v1.SetPersonFieldRequest) (*v1.PhonebookCard, error) {
	r, err := p.c.SetPersonField(ctx, inProfile(p.profile, asked))
	if err != nil {
		return nil, err
	}
	return r.Msg.GetCard(), nil
}

func (p people) keys(ctx context.Context) ([]string, error) {
	r, err := p.c.PersonKeys(ctx, inProfile(p.profile, &v1.PersonKeysRequest{}))
	if err != nil {
		return nil, err
	}
	return r.Msg.GetKeys(), nil
}

// whom is how a window names a person: by an ID, or by a card People listed.
type whom struct {
	label  string
	person string
	card   string
}

func (w whom) asked() *v1.PersonCardRequest {
	return &v1.PersonCardRequest{Person: w.person, Card: w.card}
}

func valuesOf(card *v1.PhonebookCard, key string) []string {
	for _, f := range slices.Concat(card.GetIds(), card.GetFields()) {
		if f.GetKey() == key {
			return f.GetValues()
		}
	}
	return nil
}

// writeAsTheWindow writes key = values as the desktop does: over the card it read,
// and, refused (ABORTED), over the card read again while it is the same card (or still
// none) and the key holds what was read. It reports whether the key landed.
func writeAsTheWindow(ctx context.Context, p people, w whom, key string, values []string) (bool, error) {
	shown, err := p.card(ctx, w.asked())
	if err != nil {
		return false, err
	}
	return writeOver(ctx, p, w, shown, key, values)
}

// writeOver is writeAsTheWindow over the card as shown.
func writeOver(ctx context.Context, p people, w whom, shown *v1.PersonCardResponse, key string, values []string) (bool, error) {
	read := shown
	for range 100 {
		_, err := p.set(ctx, &v1.SetPersonFieldRequest{
			Person: w.person, Card: w.card, Name: "Gil", Revision: read.GetCard().GetRevision(), Key: key, Values: values,
		})
		if connect.CodeOf(err) != connect.CodeAborted {
			return err == nil, err
		}
		if read, err = p.card(ctx, w.asked()); err != nil {
			return false, err
		}
		before, now := shown.GetCard(), read.GetCard()
		if (before.GetCard() != "" && now.GetCard() != before.GetCard()) || !slices.Equal(valuesOf(now, key), valuesOf(before, key)) {
			return false, nil
		}
	}
	return false, errors.New("never landed")
}

// What a window reads of the phonebook, through kithd, is the folder as it is, as its
// profile shows it: People lists every card the profile does not hide (narrowed by
// words and field terms, with the keys the terms named), and names the cards that
// cannot be read only to a profile that hides no one; a card is read by a person's ID
// (any ID linked to theirs), by its name, or by a room's one person, with every ID they
// go by and your DM with them; a name that is no card's, or a path, reads nothing.
func TestAWindowReadsThePhonebookThroughKithd(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cards := maps.Clone(peopleCards)
	cards["Broken.md"] = "---\nrole: [unclosed\n---\n"
	_, in := phonebookServed(t, cards)
	home, work := in("home"), in("work")
	listed := func(p people, query string) string {
		t.Helper()
		r, err := p.list(ctx, query)
		if err != nil {
			t.Fatalf("%s %q: %v", p.profile, query, err)
		}
		var out []string
		for _, l := range r.GetPeople() {
			row := l.GetCard() + "(" + strings.Join(l.GetIdKeys(), ",")
			for _, m := range l.GetMatched() {
				row += " " + m.GetKey() + "=" + strings.Join(m.GetValues(), ",")
			}
			out = append(out, row+")")
		}
		for _, u := range r.GetUnreadable() {
			out = append(out, "unreadable "+u.GetCard())
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		p           people
		query, want string
	}{
		{home, "", "Dana Levi(matrix) Eli Stone(matrix) Eli(matrix) Me(matrix) Noa(telegram) Ori(email) Pia(phone) unreadable Broken"},
		{work, "", "Dana Levi(matrix) Me(matrix)"},
		{home, "role:a", "Eli Stone(matrix role=Family Attorney) Me(matrix role=attorney) Noa(telegram role=architect) Ori(email role=attorney) unreadable Broken"},
		{home, "stone ROLE:attorney", "Eli Stone(matrix role=Family Attorney) unreadable Broken"},
		{work, "role:attorney", "Me(matrix role=attorney)"},
		{home, "pets:rex", "Dana Levi(matrix pets=Rex) unreadable Broken"},
	} {
		if got := listed(c.p, c.query); got != c.want {
			t.Errorf("%s %q lists %q, want %q", c.p.profile, c.query, got, c.want)
		}
	}
	if keys, err := work.keys(ctx); err != nil || strings.Join(keys, ",") != "origin,pets,role,tags" {
		t.Errorf("work's keys: %v %v", keys, err)
	}
	if keys, err := home.keys(ctx); err != nil || strings.Join(keys, ",") != "firm,nickname,origin,pets,role,tags" {
		t.Errorf("home's keys: %v %v", keys, err)
	}

	for _, asked := range []*v1.PersonCardRequest{{Person: danaWA}, {Person: danaMX}, {Card: "Dana Levi"}, {RoomId: danaDM}} {
		got, err := home.card(ctx, asked)
		if err != nil {
			t.Fatalf("%v: %v", asked, err)
		}
		if got.GetCard().GetCard() != "Dana Levi" || strings.Join(valuesOf(got.GetCard(), "pets"), ",") != "Rex" ||
			!slices.Contains(got.GetPeople(), danaWA) || !slices.Contains(got.GetPeople(), danaMX) || got.GetConversation() != danaDM {
			t.Errorf("%v reads %+v", asked, got)
		}
		if asked.GetRoomId() != "" && (got.GetPerson() != danaMX || got.GetName() != "Dana Levi") {
			t.Errorf("the DM's person: %q %q", got.GetPerson(), got.GetName())
		}
	}
	// Your DM with her is home's, not work's.
	if got, err := work.card(ctx, &v1.PersonCardRequest{Person: danaWA}); err != nil || got.GetCard().GetCard() != "Dana Levi" || got.GetConversation() != "" {
		t.Errorf("work reads Dana's card: %+v %v", got, err)
	}
	if got, err := home.card(ctx, &v1.PersonCardRequest{Person: gilTG}); err != nil || got.GetCard().GetCard() != "" || got.GetPerson() != gilTG {
		t.Errorf("a person with no card: %+v %v", got, err)
	}
	for _, c := range []struct {
		p     people
		asked *v1.PersonCardRequest
		code  connect.Code
		says  string
	}{
		{work, &v1.PersonCardRequest{Person: eliMX}, connect.CodeNotFound, " holds no "},
		{work, &v1.PersonCardRequest{Card: "Eli Stone"}, connect.CodeNotFound, " holds no "},
		{work, &v1.PersonCardRequest{Person: noaTG}, connect.CodeNotFound, " holds no "},
		{work, &v1.PersonCardRequest{Card: "Noa"}, connect.CodeNotFound, " holds no "},
		{work, &v1.PersonCardRequest{Card: "Eli"}, connect.CodeNotFound, " holds no "},
		{work, &v1.PersonCardRequest{Card: "Ori"}, connect.CodeNotFound, " holds no "},
		{work, &v1.PersonCardRequest{Card: "Pia"}, connect.CodeNotFound, " holds no "},
		{work, &v1.PersonCardRequest{Person: "whatsapp:15550100003@s.whatsapp.net"}, connect.CodeNotFound, " holds no "},
		{work, &v1.PersonCardRequest{Card: "Broken"}, connect.CodeNotFound, "no card"},
		{work, &v1.PersonCardRequest{RoomId: danaDM}, connect.CodeNotFound, " holds no "},
		{home, &v1.PersonCardRequest{Card: "Broken"}, connect.CodeFailedPrecondition, "front matter"},
		{home, &v1.PersonCardRequest{Card: "../phonebook/Dana Levi"}, connect.CodeNotFound, "no card"},
		{home, &v1.PersonCardRequest{Card: "Dana Levi.md"}, connect.CodeNotFound, "no card"},
		{home, &v1.PersonCardRequest{Card: "notes"}, connect.CodeNotFound, "no card"},
		{home, &v1.PersonCardRequest{RoomId: mx + "!group:x"}, connect.CodeFailedPrecondition, "one person"},
		{home, &v1.PersonCardRequest{Person: danaMX, Card: "Dana Levi"}, connect.CodeInvalidArgument, "one person"},
		{home, &v1.PersonCardRequest{}, connect.CodeInvalidArgument, "one person"},
	} {
		_, err := c.p.card(ctx, c.asked)
		if connect.CodeOf(err) != c.code || err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s %v: %v, want %v saying %q", c.p.profile, c.asked, err, c.code, c.says)
		}
	}
	if got, err := home.card(ctx, &v1.PersonCardRequest{Card: "Eli Stone"}); err != nil || got.GetCard().GetCard() != "Eli Stone" {
		t.Errorf("home reads Eli's card: %+v %v", got, err)
	}
}

// meddled is what another writer does between a window's read of Dana's card and its
// write of her pets: the outcome of the write (ok, ABORTED, or another code), and
// whether the window, reading again, writes again.
type meddled struct {
	name    string
	do      func(t *testing.T, dir string, other people)
	refused connect.Code
	again   bool
	// byCard is how it goes for a window naming the card rather than her ID, when other.
	byCard *meddled
	holds  []string
}

func editCard(t *testing.T, dir, name, old, replacement string) {
	t.Helper()
	path := filepath.Join(dir, name)
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), old) {
		t.Fatalf("%s: %v, holds no %q", name, err, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), old, replacement, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

var meddlers = []meddled{
	{name: "nothing", do: func(*testing.T, string, people) {}},
	{name: "the same key by hand", refused: connect.CodeAborted, do: func(t *testing.T, dir string, _ people) {
		editCard(t, dir, "Dana Levi.md", "pets: [Rex]", "pets: [Bo]")
	}},
	{name: "another key by hand", refused: connect.CodeAborted, again: true, holds: []string{"origin: Tel Aviv"}, do: func(t *testing.T, dir string, _ people) {
		editCard(t, dir, "Dana Levi.md", "origin: Haifa", "origin: Tel Aviv")
	}},
	{name: "the body only", refused: connect.CodeAborted, again: true, holds: []string{"club.\nAnd climbing.\n"}, do: func(t *testing.T, dir string, _ people) {
		editCard(t, dir, "Dana Levi.md", "club.\n", "club.\nAnd climbing.\n")
	}},
	{name: "renamed", refused: connect.CodeAborted, byCard: &meddled{refused: connect.CodeNotFound}, do: func(t *testing.T, dir string, _ people) {
		if err := os.Rename(filepath.Join(dir, "Dana Levi.md"), filepath.Join(dir, "Dana.md")); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "removed", refused: connect.CodeAborted, byCard: &meddled{refused: connect.CodeNotFound}, do: func(t *testing.T, dir string, _ people) {
		if err := os.Remove(filepath.Join(dir, "Dana Levi.md")); err != nil {
			t.Fatal(err)
		}
	}},
	{
		name: "a second card claiming her, first by name", refused: connect.CodeAborted,
		// Named by its card, the card read is written: it is as it was.
		byCard: &meddled{},
		do: func(t *testing.T, dir string, _ people) {
			if err := os.WriteFile(filepath.Join(dir, "Aaron.md"), []byte("---\nmatrix: [\"@dana:example.org\"]\n---\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	},
	{name: "its front matter broken by hand", refused: connect.CodeAborted, byCard: &meddled{refused: connect.CodeFailedPrecondition}, do: func(t *testing.T, dir string, _ people) {
		editCard(t, dir, "Dana Levi.md", "pets: [Rex]", "pets: [Rex")
	}},
	{name: "another window, the same key", refused: connect.CodeAborted, do: func(t *testing.T, _ string, other people) {
		if _, err := writeAsTheWindow(t.Context(), other, whom{person: danaWA}, "pets", []string{"Bo"}); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "another window, another key", refused: connect.CodeAborted, again: true, holds: []string{"origin: Tel Aviv"}, do: func(t *testing.T, _ string, other people) {
		if _, err := writeAsTheWindow(t.Context(), other, whom{card: "Dana Levi"}, "origin", []string{"Tel Aviv"}); err != nil {
			t.Fatal(err)
		}
	}},
}

// A window's write lands only over Dana's card as it read it, named by her ID or by the
// card. Whatever another writer did meanwhile (a hand edit of the same key, of another,
// of the body; a rename; a removal; a second card claiming her; front matter broken; a
// second window writing) refuses it and leaves the folder as that writer left it; the
// window, reading again, writes again only over the same card still holding what it
// read, so both changes stand. Unmeddled, the card keeps its comments, its keys' order
// and its body, and only the key changes.
func TestAWindowsWriteGivesWayToEveryOtherWriter(t *testing.T) {
	t.Parallel()
	for _, m := range meddlers {
		for _, w := range []whom{{label: "her ID", person: danaWA}, {label: "her card", card: "Dana Levi"}} {
			t.Run(m.name+", by "+w.label, func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				dir, in := phonebookServed(t, peopleCards)
				p := in("home")
				read, err := p.card(ctx, w.asked())
				if err != nil {
					t.Fatal(err)
				}
				m.do(t, dir, in("home"))
				want := m
				if w.card != "" && m.byCard != nil {
					want = *m.byCard
				}
				left := folder(t, dir)
				_, err = p.set(ctx, &v1.SetPersonFieldRequest{
					Person: w.person, Card: w.card, Revision: read.GetCard().GetRevision(), Key: "pets", Values: []string{"Rex", "Mia"}, List: true,
				})
				if (want.refused == 0) != (err == nil) || (err != nil && connect.CodeOf(err) != want.refused) {
					t.Fatalf("write: %v; want %v", err, want.refused)
				}
				if err != nil {
					if after := folder(t, dir); !maps.Equal(after, left) {
						t.Fatalf("a refused write changed the folder:\n%v\nwant\n%v", after, left)
					}
					landed, err := writeOver(ctx, p, w, read, "pets", []string{"Rex", "Mia"})
					if want.refused == connect.CodeAborted && (err != nil || landed != want.again) {
						t.Fatalf("written again: %v, %v; want %v", landed, err, want.again)
					}
					if !landed {
						return
					}
				}
				text := folder(t, dir)["Dana Levi.md"]
				if len(want.holds) == 0 && text != strings.Replace(danaByHand, "pets: [Rex]", "pets: [Rex, Mia]", 1) {
					t.Errorf("card:\n%s", text)
				}
				for _, h := range append(want.holds, "pets: [Rex, Mia]") {
					if !strings.Contains(text, h) {
						t.Errorf("lost %q:\n%s", h, text)
					}
				}
			})
		}
	}
}

// folder is every file of dir, hidden ones too, by name.
func folder(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(raw)
	}
	return out
}

// Two windows and two terminals (writing the folder themselves, as the TUI does), all
// at once, writing Gil's first fields: one card is made, after him, holding every
// identifier kith knows of his and every key each wrote; the one key two of them wrote
// holds one of their values.
func TestWindowsAndTerminalsAtOnceMakeOneCard(t *testing.T) {
	t.Parallel()
	for round := range 15 {
		ctx := t.Context()
		dir, in := phonebookServed(t, peopleCards)
		gil := whom{person: gilTG}
		var wg sync.WaitGroup
		landed := map[string]bool{}
		var mu sync.Mutex
		var failed []error
		write := func(writer, key, value string, do func() (bool, error)) {
			wg.Go(func() {
				ok, err := do()
				mu.Lock()
				defer mu.Unlock()
				landed[writer] = ok
				if err != nil {
					failed = append(failed, fmt.Errorf("%s: %w", writer, err))
				}
			})
		}
		for i, profile := range []string{"home", "work"} {
			write("window"+profile, fmt.Sprintf("w%d", i), "x", func() (bool, error) {
				return writeAsTheWindow(ctx, in(profile), gil, fmt.Sprintf("w%d", i), []string{"x"})
			})
		}
		write("window same", "same", "window", func() (bool, error) {
			return writeAsTheWindow(ctx, in("home"), gil, "same", []string{"window"})
		})
		for i := range 2 {
			write(fmt.Sprintf("terminal%d", i), fmt.Sprintf("t%d", i), "x", func() (bool, error) {
				return writeAsATerminal(dir, fmt.Sprintf("t%d", i), "x")
			})
		}
		write("terminal same", "same", "terminal", func() (bool, error) { return writeAsATerminal(dir, "same", "terminal") })
		wg.Wait()
		if len(failed) > 0 {
			t.Fatalf("round %d: %v", round, failed)
		}
		cards, err := phonebook.New(dir).Cards()
		if err != nil {
			t.Fatal(err)
		}
		var his []phonebook.Card
		for _, c := range cards {
			if slices.Contains(c.IDs["telegram"], "99") {
				his = append(his, c)
			}
		}
		if len(his) != 1 {
			t.Fatalf("round %d: %d cards for Gil", round, len(his))
		}
		card := his[0]
		if !strings.HasPrefix(card.Name, "Gil") {
			t.Errorf("round %d: Gil's card is %q", round, card.Name)
		}
		for _, key := range []string{"w0", "w1", "t0", "t1"} {
			if strings.Join(card.Field(key), "") != "x" {
				t.Errorf("round %d: %s lost: %+v (landed %v)", round, key, card.Fields, landed)
			}
		}
		same := strings.Join(card.Field("same"), "")
		if !landed[map[string]string{"window": "window same", "terminal": "terminal same"}[same]] {
			t.Errorf("round %d: the key two wrote holds %q (landed %v)", round, same, landed)
		}
	}
}

// writeAsATerminal writes Gil's key as the TUI does: through the folder itself, over the
// card it read, reading again when refused.
func writeAsATerminal(dir, key, value string) (bool, error) {
	b := phonebook.New(dir)
	ids := phonebook.Identifiers{"telegram": {"99"}}
	shown, _, err := b.Find(ids)
	if err != nil {
		return false, err
	}
	read := shown
	for range 100 {
		_, err := b.Write(read, "Gil Amir", ids, key, []string{value}, false)
		if !errors.Is(err, phonebook.ErrMoved) {
			return err == nil, err
		}
		if read, _, err = b.Find(ids); err != nil {
			return false, err
		}
		if !slices.Equal(read.Field(key), shown.Field(key)) {
			return false, nil
		}
	}
	return false, errors.New("never landed")
}

// A person the profile hides, by their card or by an ID, is neither read nor written,
// nor given a card; another profile writes them. A key or value no card can hold is
// refused, and nothing is written.
func TestAWindowWritesOnlyWhomItsProfileShows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir, in := phonebookServed(t, peopleCards)
	home, work := in("home"), in("work")
	before := folder(t, dir)
	revisions := map[string]string{}
	for _, card := range []string{"Eli Stone", "Noa"} {
		r, err := home.card(ctx, &v1.PersonCardRequest{Card: card})
		if err != nil {
			t.Fatal(err)
		}
		revisions[card] = r.GetCard().GetRevision()
	}
	for _, asked := range []*v1.SetPersonFieldRequest{
		{Person: eliMX, Revision: revisions["Eli Stone"]},
		{Card: "Eli Stone", Revision: revisions["Eli Stone"]},
		{Person: noaTG, Revision: revisions["Noa"]},
		{Card: "Noa", Revision: revisions["Noa"]},
		{Person: "telegram:55", Name: "Hidden"},
	} {
		asked.Key, asked.Values = "pets", []string{"Rex"}
		if _, err := work.set(ctx, asked); connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), " holds no ") {
			t.Errorf("work writes %v: %v", asked, err)
		}
	}
	for _, f := range []*v1.SetPersonFieldRequest{
		{Person: danaMX, Key: "a:b", Values: []string{"x"}}, {Person: danaMX, Key: "-x", Values: []string{"x"}},
		{Person: danaMX, Key: "pets", Values: []string{"a\nb"}}, {Person: danaMX, Key: "", Values: []string{"x"}},
		{Person: gilTG, Key: "pets", Values: []string{"\x00"}},
	} {
		if _, err := home.set(ctx, f); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%q = %q: %v", f.GetKey(), f.GetValues(), err)
		}
	}
	if after := folder(t, dir); !maps.Equal(after, before) {
		t.Fatalf("a refused write changed the folder: %v", slices.Sorted(maps.Keys(after)))
	}
	if card, err := home.set(ctx, &v1.SetPersonFieldRequest{Card: "Eli Stone", Revision: revisions["Eli Stone"], Key: "pets", Values: []string{"Rex"}}); err != nil ||
		strings.Join(valuesOf(card, "pets"), "") != "Rex" {
		t.Errorf("home writes Eli's card: %+v %v", card, err)
	}
}

// A new card is named by kithd after the name the window gives, made a file of the
// folder's own whatever that name holds, numbered past a card already so named; with
// none given, after the directory's name for them. It holds every identifier kith
// knows of theirs.
func TestANewCardFromAWindowIsAlwaysTheFoldersOwn(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir, in := phonebookServed(t, peopleCards)
	home := in("home")
	parent := filepath.Dir(dir)
	for i, name := range []string{"../../escaped", "/etc/passwd", "..", "a\x00b", "Noa", strings.Repeat("ש", 300), "\u202egnp.exe", ""} {
		person := fmt.Sprintf("telegram:%d", 1000+i)
		if name == "" {
			person = gilTG
		}
		card, err := home.set(ctx, &v1.SetPersonFieldRequest{Person: person, Name: name, Key: "met", Values: []string{"at work"}})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		path := filepath.Join(dir, card.GetCard()+".md")
		raw, err := os.ReadFile(path)
		if err != nil || len(card.GetCard()) > 250 || strings.ContainsAny(card.GetCard(), "/\\\x00\u202e") || strings.HasPrefix(card.GetCard(), ".") {
			t.Errorf("%q made %q: %v", name, card.GetCard(), err)
		}
		if !strings.Contains(string(raw), strings.TrimPrefix(person, "telegram:")) || !strings.Contains(string(raw), "met: at work") {
			t.Errorf("%q: the card holds\n%s", name, raw)
		}
		if name == "Noa" && card.GetCard() != "Noa 2" || name == "" && card.GetCard() != "Gil Amir" {
			t.Errorf("%q named %q", name, card.GetCard())
		}
	}
	if left, _ := os.ReadDir(parent); len(left) != 2 {
		t.Errorf("made outside the folder: %v", left)
	}
	if folder(t, dir)["Noa.md"] != peopleCards["Noa.md"] {
		t.Error("Noa's card was written over")
	}
}
