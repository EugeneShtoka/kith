package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The people namesWorld names, all invented: Svetlana's Telegram account calls itself
// "neke" and her bridge puppet "neke (Telegram)", Dana is a number the phone book names,
// and Bo is the one peer of a direct chat.
const (
	nwMe       = "matrix:@me:example.org"
	nwSvetlana = "telegram:66919467"
	nwPuppet   = "matrix:@telegram_66919467:example.org"
	nwDana     = "whatsapp:15550100001@s.whatsapp.net"
	nwBo       = "matrix:@bo:example.org"
	nwBoRoom   = domain.RoomID("matrix:0123456789abcdef/!bo:example.org")
)

// namesWorld is what Names reads, as kithd's cache would answer, and the config.
type namesWorld struct {
	mu sync.Mutex
	// alias is Svetlana's [[display.identity]] alias; chatName the [[display.name]] of
	// Bo's chat.
	alias, chatName bool
	// linked: a bridge links the puppet to Svetlana's account. danaName is the phone
	// book's name for her number, roomName the network's for Bo's chat, boName his.
	linked                     bool
	danaName, roomName, boName string
	// failDir, failRooms and failMembers fail the next read of each.
	failDir, failRooms, failMembers bool
	// during runs inside the next directory read, after it read: a write landing while
	// a build reads.
	during func()
}

func (w *namesWorld) config() config.Config {
	w.mu.Lock()
	defer w.mu.Unlock()
	var cfg config.Config
	if w.alias {
		cfg.Display.Identities = []config.Identity{{Alias: "Svetlana", IDs: []string{nwSvetlana}}}
	}
	if w.chatName {
		cfg.Display.Names = []config.DisplayName{{Target: string(nwBoRoom), Name: "Bohdan"}}
	}
	return cfg
}

var errNamesLocked = errors.New("database is locked")

func (w *namesWorld) Directory(context.Context) (domain.Directory, error) {
	w.mu.Lock()
	names := []domain.PersonName{{Source: "book", ID: domain.PhoneID("15550100001"), Name: w.danaName, Rank: domain.RankSaved}}
	var links []domain.PersonLink
	if w.linked {
		links = append(links, domain.PersonLink{Source: "bridge", ID: nwPuppet, Other: nwSvetlana})
	}
	fail, during := w.failDir, w.during
	w.failDir, w.during = false, nil
	w.mu.Unlock()
	if during != nil {
		during()
	}
	if fail {
		return domain.Directory{}, errNamesLocked
	}
	return domain.NewDirectory(names, links), nil
}

func (w *namesWorld) Rooms(context.Context) ([]domain.Room, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failRooms {
		w.failRooms = false
		return nil, errNamesLocked
	}
	return []domain.Room{{ID: nwBoRoom, Name: w.roomName, IsDirect: true}}, nil
}

func (w *namesWorld) Members(_ context.Context, room domain.RoomID, _ int) ([]domain.Member, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failMembers {
		w.failMembers = false
		return nil, errNamesLocked
	}
	if room != nwBoRoom {
		return nil, nil
	}
	return []domain.Member{{UserID: nwMe, DisplayName: "Me"}, {UserID: nwBo, DisplayName: w.boName}}, nil
}

func (w *namesWorld) Selves(context.Context) ([]string, error) { return []string{nwMe}, nil }

// known is what each probed person's network calls them now.
func (w *namesWorld) known() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return map[string]string{nwSvetlana: "neke", nwPuppet: "neke (Telegram)", nwDana: "+15550100001", nwBo: w.boName}
}

// want is what every client should show for each probed person now, worked out by hand
// from domain.People's rules: an alias under any linked ID, then the phone book, then a
// named chat's name for its one peer (when the chat's own name is theirs), then the
// network's.
func (w *namesWorld) want() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]string{nwSvetlana: "neke", nwPuppet: "neke (Telegram)", nwDana: w.danaName, nwBo: w.boName}
	if w.alias {
		out[nwSvetlana] = "Svetlana"
		if w.linked {
			out[nwPuppet] = "Svetlana"
		}
	}
	if w.chatName && w.roomName == w.boName {
		out[nwBo] = "Bohdan"
	}
	return out
}

func nwToggle(s *string, a, b string) {
	if *s == a {
		*s = b
	} else {
		*s = a
	}
}

// namesStep is one thing that changes how people are named, done as kithd is told of it:
// a config through Use, a cache write through Changed.
type namesStep struct {
	name string
	do   func(ctx context.Context, w *namesWorld, n *Names, h *namesHarness)
}

// namesHarness holds what a step leaves running.
type namesHarness struct {
	pending []chan struct{}
}

// nwWrite changes the world under its lock, then tells n as the cache would.
func nwWrite(w *namesWorld, n *Names, change func()) {
	w.mu.Lock()
	change()
	w.mu.Unlock()
	n.Changed()
}

func nwReload(ctx context.Context, w *namesWorld, n *Names, change func()) {
	w.mu.Lock()
	change()
	w.mu.Unlock()
	n.Use(ctx, w.config())
}

var namesSteps = []namesStep{
	{"the alias set or unset", func(ctx context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwReload(ctx, w, n, func() { w.alias = !w.alias })
	}},
	{"the chat named or unnamed", func(ctx context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwReload(ctx, w, n, func() { w.chatName = !w.chatName })
	}},
	{"the puppet linked or unlinked", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() { w.linked = !w.linked })
	}},
	{"Dana renamed in the phone book", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() { nwToggle(&w.danaName, "Dana Levi", "Dana Cohen") })
	}},
	{"the network renaming Bo's chat", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() { nwToggle(&w.roomName, "Bo", "Bobby") })
	}},
	{"Bo renaming himself", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() { nwToggle(&w.boName, "Bo", "Bobby") })
	}},
	{"reads failing, nothing written", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() { w.failDir, w.failRooms, w.failMembers = true, true, true })
	}},
	{"Dana renamed, the directory's read failing", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() { nwToggle(&w.danaName, "Dana Levi", "Dana Cohen"); w.failDir = true })
	}},
	{"the chat renamed, the rooms' read failing", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() { nwToggle(&w.roomName, "Bo", "Bobby"); w.failRooms = true })
	}},
	{"Bo renamed, the members' read failing", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() { nwToggle(&w.boName, "Bo", "Bobby"); w.failMembers = true })
	}},
	{"the puppet linked while a build reads", func(_ context.Context, w *namesWorld, n *Names, _ *namesHarness) {
		nwWrite(w, n, func() {
			w.during = func() { nwWrite(w, n, func() { w.linked = !w.linked }) }
		})
	}},
	{"a reload while a build reads", func(ctx context.Context, w *namesWorld, n *Names, h *namesHarness) {
		done := make(chan struct{})
		h.pending = append(h.pending, done)
		nwWrite(w, n, func() {
			w.during = func() {
				go func() {
					defer close(done)
					nwReload(ctx, w, n, func() { w.alias = !w.alias })
				}()
				// Held by the build reading now, it lands after it; without the hold it
				// would land first, and the build reading the old config last.
				select {
				case <-done:
				case <-time.After(5 * time.Millisecond):
				}
			}
		})
	}},
}

// settleNames runs what kithd's Run would until nothing is asked for, calling built after
// each build; each People asks a build again after a read failed.
func settleNames(ctx context.Context, n *Names, h *namesHarness, built func()) {
	for range 8 {
		n.People(ctx)
		select {
		case <-n.wake:
			n.build(ctx)
			built()
			continue
		default:
		}
		if len(h.pending) == 0 {
			return
		}
		for _, done := range h.pending {
			<-done
		}
		h.pending = nil
	}
}

// Every sequence of three steps, from two named starts: once kithd has built after the
// last, everyone is named as the rules say, and whenever a name changed, every client was
// told (CHANGE_NAMES); no build on the way names anyone otherwise than before the step or
// after it. The steps include reads that fail, and a write and a reload landing while a
// build reads.
func TestKithdsNamesHoldOverAnyChangeSequence(t *testing.T) {
	t.Parallel()
	starts := map[string]func() *namesWorld{
		"nothing set": func() *namesWorld { return &namesWorld{danaName: "Dana Levi", roomName: "Bo", boName: "Bo"} },
		"alias and a name": func() *namesWorld {
			return &namesWorld{alias: true, chatName: true, linked: true, danaName: "Dana Levi", roomName: "Bo", boName: "Bo"}
		},
	}
	ctx := context.Background()
	for startName, start := range starts {
		for _, a := range namesSteps {
			for _, b := range namesSteps {
				for _, c := range namesSteps {
					w := start()
					told := 0
					n := NewNames(w, w.config(), func() { told++ })
					h := &namesHarness{}
					var path []string
					for _, step := range []namesStep{a, b, c} {
						path = append(path, step.name)
						before, held := told, n.People(ctx)
						// Never a name from neither side: a failed read must not name anyone by
						// less than it knew.
						between := func() {
							got := fmt.Sprint(nwNames(n.People(ctx), w))
							if was, now := fmt.Sprint(nwNames(held, w)), fmt.Sprint(w.want()); got != was && got != now {
								t.Fatalf("from %s, %s: a build named %v, neither %v as before nor %v", startName, strings.Join(path, ", then "), got, was, now)
							}
						}
						step.do(ctx, w, n, h)
						between()
						settleNames(ctx, n, h, between)
						got, want := nwNames(n.People(ctx), w), w.want()
						if fmt.Sprint(got) != fmt.Sprint(want) {
							t.Fatalf("from %s, %s: named %v, want %v", startName, strings.Join(path, ", then "), got, want)
						}
						// A client holding the names from before reads what it shows again only
						// when told: what it reads now, named as before, must be what it shows.
						if stale := nwNames(held, w); fmt.Sprint(got) != fmt.Sprint(stale) && told == before {
							t.Fatalf("from %s, %s: kithd names %v where it named %v, and no client was told", startName, strings.Join(path, ", then "), got, stale)
						}
					}
				}
			}
		}
	}
}

// nwNames is each probed person's name as p gives it, with what their network calls them now.
func nwNames(p domain.People, w *namesWorld) map[string]string {
	out := map[string]string{}
	for id, known := range w.known() {
		out[id] = p.Name(id, known)
	}
	return out
}

// peersWorld is rooms with their members, for the named rooms' peers.
type peersWorld struct {
	rooms   []domain.Room
	members map[domain.RoomID][]domain.Member
	selves  []string
}

func (peersWorld) Directory(context.Context) (domain.Directory, error) {
	return domain.Directory{}, nil
}
func (w peersWorld) Rooms(context.Context) ([]domain.Room, error) { return w.rooms, nil }
func (w peersWorld) Selves(context.Context) ([]string, error)     { return w.selves, nil }
func (w peersWorld) Members(_ context.Context, room domain.RoomID, _ int) ([]domain.Member, error) {
	return w.members[room], nil
}

// A private chat named after its one peer names that peer everywhere; a group, a chat
// named for someone else, and yourself (your bridge puppet too) are left alone, and a
// bridge's bot is no peer. The rules the terminal client named people by, now kithd's.
func TestARoomNamedAfterItsPeerNamesThePeer(t *testing.T) {
	t.Parallel()
	const (
		me, puppet, bot  = "matrix:@me:x", "matrix:@linkedin_me:x", "matrix:@linkedinbot:x"
		dana, eli, title = "matrix:@linkedin_dana:x", "matrix:@linkedin_eli:x", "Dana Levi ✨Hiring✨"
		dm, group, other = domain.RoomID("matrix:0123456789abcdef/!dm:x"), domain.RoomID("matrix:0123456789abcdef/!group:x"), domain.RoomID("matrix:0123456789abcdef/!other:x")
	)
	w := peersWorld{
		rooms: []domain.Room{{ID: dm, Name: title}, {ID: group, Name: title}, {ID: other, Name: "Eli's chat"}},
		members: map[domain.RoomID][]domain.Member{
			dm:    {{UserID: me, DisplayName: "Me"}, {UserID: puppet, DisplayName: "Me"}, {UserID: bot, DisplayName: "Bridge Bot"}, {UserID: dana, DisplayName: title}},
			group: {{UserID: me}, {UserID: dana, DisplayName: title}, {UserID: eli, DisplayName: "Eli"}},
			other: {{UserID: me}, {UserID: bot}, {UserID: eli, DisplayName: "Eli"}},
		},
		selves: []string{me, puppet},
	}
	var cfg config.Config
	cfg.Display.Names = []config.DisplayName{{Target: string(dm), Name: "Dana"}, {Target: string(group), Name: "Team"}, {Target: string(other), Name: "Eli L"}}
	p := NewNames(w, cfg, nil).People(context.Background())
	for _, c := range []struct{ who, known, want string }{
		{dana, title, "Dana"},
		{eli, "Eli", "Eli"},
		{puppet, "Me", "Me"},
	} {
		if got := p.Name(c.who, c.known); got != c.want {
			t.Errorf("%s (%q) = %q, want %q", c.who, c.known, got, c.want)
		}
	}
}
