package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// filingWorld is a railWorld whose networks archive chats and file rooms into spaces,
// either able to refuse, and which runs one thing while a FileRoom of one profile is
// between reading the config and writing it (its read of the rooms): another client's
// filing, or a hand edit.
type filingWorld struct {
	*railWorld

	fmu        sync.Mutex
	archiveErr error
	spaceErr   map[domain.SpaceID]error
	calls      []string
	between    func()
	betweenIn  string
}

func (w *filingWorld) Rooms(ctx context.Context) ([]domain.Room, error) {
	w.fmu.Lock()
	run := w.between
	if run != nil && domain.ProfileIn(ctx) == w.betweenIn {
		w.between = nil
	} else {
		run = nil
	}
	w.fmu.Unlock()
	if run != nil {
		run()
	}
	return w.railWorld.Rooms(ctx)
}

func (w *filingWorld) called() []string {
	w.fmu.Lock()
	defer w.fmu.Unlock()
	return slices.Clone(w.calls)
}

func (w *filingWorld) SetRoomArchived(_ context.Context, id domain.RoomID, archived bool) error {
	w.fmu.Lock()
	err := w.archiveErr
	w.calls = append(w.calls, fmt.Sprintf("archive %s %v", id, archived))
	w.fmu.Unlock()
	if err != nil {
		return err
	}
	w.profileWorld.mu.Lock()
	defer w.profileWorld.mu.Unlock()
	rooms := slices.Clone(w.rooms)
	for i := range rooms {
		if rooms[i].ID == id {
			rooms[i].Archived = archived
		}
	}
	w.rooms = rooms
	return nil
}

func (w *filingWorld) AddToSpace(_ context.Context, space domain.SpaceID, room domain.RoomID) error {
	return w.fileSpace(space, room, true)
}

func (w *filingWorld) RemoveFromSpace(_ context.Context, space domain.SpaceID, room domain.RoomID) error {
	return w.fileSpace(space, room, false)
}

// fileSpace files room into space or out of it; a refused parent still files it there.
func (w *filingWorld) fileSpace(space domain.SpaceID, room domain.RoomID, in bool) error {
	w.fmu.Lock()
	err := w.spaceErr[space]
	w.calls = append(w.calls, fmt.Sprintf("space %s %s %v", space, room, in))
	w.fmu.Unlock()
	if err != nil && !errors.Is(err, api.ErrNoSpaceParent) {
		return err
	}
	w.profileWorld.mu.Lock()
	defer w.profileWorld.mu.Unlock()
	spaces := slices.Clone(w.spaces)
	for i := range spaces {
		if spaces[i].ID == space {
			children := slices.DeleteFunc(slices.Clone(spaces[i].Children), func(c domain.RoomID) bool { return c == room })
			if in {
				children = append(children, room)
			}
			spaces[i].Children = children
		}
	}
	w.spaces = spaces
	return err
}

func (w *filingWorld) verdictsNow() map[string]domain.TagVerdict {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]domain.TagVerdict{}
	for room, byTag := range w.verdicts {
		for tag, v := range byTag {
			v.At = time.Time{}
			out[string(room)+" "+tag] = v
		}
	}
	return out
}

// The filing world: three Matrix rooms (one direct) and two Telegram chats (one direct),
// their names drawn from three so an entry can name rooms of both profiles; a space you
// made, a bridge's, and one holding only the direct room. "home" holds every room,
// "work" the groups.
var (
	filingIDs    = []domain.RoomID{mx + "!a:x", mx + "!b:x", mx + "!c:x", "telegram:42/-1", "telegram:42/-2"}
	filingDirect = map[domain.RoomID]bool{mx + "!b:x": true, "telegram:42/-2": true}
	filingNames  = []string{"Alpha", "Bravo", "Charlie"}
	filingSpaces = map[string]domain.SpaceID{"Work": mx + "!work:x", "Bridge": mx + "!bridge:x", "Lone": mx + "!lone:x"}
)

// filingRooms is the rooms in one draw: names, and which chats Telegram archives.
func filingRooms(rng *rand.Rand) []domain.Room {
	rooms := make([]domain.Room, len(filingIDs))
	for i, id := range filingIDs {
		rooms[i] = domain.Room{ID: id, Name: filingNames[rng.IntN(len(filingNames))], IsDirect: filingDirect[id]}
		if domain.NetworkOf(string(id)) == domain.ProtocolTelegram {
			rooms[i].Archived = rng.IntN(2) == 0
		}
	}
	return rooms
}

func filingSpacesDrawn(rng *rand.Rand) []domain.Space {
	var work []domain.RoomID
	for _, id := range filingIDs {
		if rng.IntN(2) == 0 {
			work = append(work, id)
		}
	}
	return []domain.Space{
		{ID: filingSpaces["Work"], Name: "Work", Children: work},
		{ID: filingSpaces["Bridge"], Name: "Bridge", Keeper: "matrix:@bot:x", Children: []domain.RoomID{"telegram:42/-1", "telegram:42/-2"}},
		{ID: filingSpaces["Lone"], Name: "Lone", Children: []domain.RoomID{mx + "!b:x"}},
	}
}

// filingConfig is a config in one draw: tags reading rules, state and each other, with
// picked and excluded entries naming rooms by ID or name; silent and exclusive
// ones; Telegram's archive followed or not, mirrored or not; and the two profiles.
func filingConfig(rng *rand.Rand, rooms []domain.Room) config.Config {
	off := false
	terms := []string{"*", "dm", "unread", "unread", "space:Work", "room:Alpha", "room:Bravo", "not dm", "not room:Charlie", "tag:T0"}
	entry := func() string {
		if rng.IntN(2) == 0 {
			return "room:" + filingNames[rng.IntN(len(filingNames))]
		}
		return string(rooms[rng.IntN(len(rooms))].ID)
	}
	var tags []config.Tag
	for i := range 1 + rng.IntN(3) {
		tag := config.Tag{Name: fmt.Sprintf("T%d", i)}
		if rng.IntN(4) == 0 {
			tag.Exclusive = []string{config.ExclusiveTags}
		}
		if rng.IntN(3) == 0 {
			tag.CountsUnread = &off
		}
		for range rng.IntN(3) {
			if term := terms[rng.IntN(len(terms))]; i > 0 || term != "tag:T0" {
				tag.Rule = append(tag.Rule, term)
			}
		}
		for range rng.IntN(3) {
			tag.Picked = append(tag.Picked, entry())
		}
		for range rng.IntN(2) {
			tag.Excluded = append(tag.Excluded, entry())
		}
		tags = append(tags, tag)
	}
	follow := rng.IntN(2) == 0
	return config.Config{
		Tags: tags,
		Profiles: []config.Profile{
			{Name: "home", Include: []string{"*"}},
			{Name: "work", Include: []string{"group"}},
		},
		Telegram: config.Telegram{Archive: config.NetworkArchive{Follow: &follow, Mirror: rng.IntN(3) > 0, Tag: tags[rng.IntN(len(tags))].Name}},
	}
}

// filingServed is a daemon over w whose config is the file at path, re-read on every
// write as kithd re-reads it.
func filingServed(t *testing.T, w *filingWorld, path string) (*harness, *daemon.Notifications) {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := daemon.NewNotifications(cfg, scopeOnly{w})
	if err != nil {
		t.Fatal(err)
	}
	h := serve(t, w, func(d *daemon.Daemon) {
		d.Notifications = n
		d.Config = daemon.NewConfigFile(path)
		d.Reload = func(context.Context) error {
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			return n.Reload(cfg)
		}
	})
	return h, n
}

// fileRoom is FileRoom over c in profile.
func fileRoom(ctx context.Context, c backendv1connect.BackendServiceClient, profile string, msg *v1.FileRoomRequest) (*v1.FileRoomResponse, error) {
	req := connect.NewRequest(msg)
	req.Header().Set("Kith-Profile", profile)
	resp, err := c.FileRoom(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// judged is, per profile and room, the tags holding it, judged afresh from the file at
// path and the world now.
func (w *filingWorld) judged(t *testing.T, path string) map[string]map[domain.RoomID][]string {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[domain.RoomID][]string{}
	for _, p := range []string{"home", "work"} {
		out[p] = map[domain.RoomID][]string{}
		rooms := w.fresh(cfg, p).Rooms
		for i := range rooms {
			in := slices.Clone(rooms[i].In)
			slices.Sort(in)
			out[p][domain.RoomID(rooms[i].Facts.ID)] = in
		}
	}
	return out
}

// factsIn is the room's facts in profile, judged from cfg and the world now.
func (w *filingWorld) factsIn(cfg config.Config, profile string, room domain.RoomID) domain.RoomFacts {
	rooms := w.fresh(cfg, profile).Rooms
	for i := range rooms {
		if rooms[i].Facts.ID == string(room) {
			return rooms[i].Facts
		}
	}
	return domain.RoomFacts{}
}

// listsOf says whether the tag's picked and excluded lists name room in profile, and
// whether its rule alone (and the filters' moves, caught) holds it, in its state as that
// tag sees it (a silent tag holding it reads it as read for any other).
func (w *filingWorld) listsOf(t *testing.T, cfg config.Config, profile string, room domain.RoomID, tag string, caught []string) (picked, excluded, byRule bool) {
	t.Helper()
	view := setup.ProfileConfig(cfg, profile)
	at := slices.IndexFunc(view.Tags, func(tg config.Tag) bool { return tg.Name == tag })
	filed := view.Tags[at]
	var silenced bool
	var facts domain.RoomFacts
	rooms := w.fresh(cfg, profile).Rooms
	for i := range rooms {
		if rooms[i].Facts.ID == string(room) {
			facts, silenced = rooms[i].Facts, rooms[i].Silenced
		}
	}
	facts.Caught = caught
	view = view.Clone()
	view.Tags[at].Picked, view.Tags[at].Excluded = nil, nil
	bare, _, err := setup.Tags(view)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	count := w.unread[room]
	w.mu.Unlock()
	_, highlights := count.Count(false)
	raw := domain.RoomState{Unread: count.HasUnread(false), Mention: highlights > 0}
	return slices.ContainsFunc(filed.Picked, facts.Names), slices.ContainsFunc(filed.Excluded, facts.Names),
		bare.HasAt(at, facts, bare.StateIn(at, raw, silenced))
}

// profileSpaceIDs is the spaces the profile holds, as kithd narrows them.
func profileSpaceIDs(t *testing.T, c backendv1connect.BackendServiceClient, profile string) []string {
	t.Helper()
	req := connect.NewRequest(&v1.SpacesRequest{})
	req.Header().Set("Kith-Profile", profile)
	resp, err := c.Spaces(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range resp.Msg.GetSpaces() {
		out = append(out, s.GetId())
	}
	return out
}

// A filing does what it asks of its room alone, whatever else writes. Over random tags,
// same-named rooms in two profiles, filter moves, Telegram's archive (followed or not,
// mirrored or not, refusing or not) and spaces (refusing, keeping their parent): FileRoom
// leaves the tag holding the room as asked (where kith follows the network's archive, as
// the network then holds it) and changes no other room's tags in any profile; another
// client's filing (another room, or the same) or a hand edit landing while it is between
// reading the config and writing it is kept, and the filing judged again on it; a room,
// space or tag the profile does not hold (a tag renamed meanwhile among them) is refused
// with nothing filed and nothing asked of a network; the network is asked to archive only
// when the mirrored tag is crossed; a release is recorded exactly when the room leaves a
// tag a filter moved it into; and two clients filing at once both stick.
func TestAFilingChangesItsRoomAloneWhateverElseWrites(t *testing.T) {
	t.Parallel()
	seeds, steps := 60, 8
	if testing.Short() {
		seeds = 10
	}
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			walkFiling(t, seed, steps)
		})
	}
}

func walkFiling(t *testing.T, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x66696c65)) // #nosec G404 -- reproducible
	rooms := filingRooms(rng)
	w := &filingWorld{railWorld: newRailWorld(), spaceErr: map[domain.SpaceID]error{}}
	w.set(rooms, filingSpacesDrawn(rng), nil)
	cfg := filingConfig(rng, rooms)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	h, _ := filingServed(t, w, path)
	c := daemon.Client(h.path)
	ctx := t.Context()
	marker := h.client()
	marker.UseProfile("home")
	watching := h.client()
	go func() { _ = watching.Start(t.Context()) }()
	defer watching.Stop()
	h.waitAttached(1)
	for range rng.IntN(4) {
		v := domain.TagVerdict{Room: rooms[rng.IntN(len(rooms))].ID, Tag: cfg.Tags[rng.IntN(len(cfg.Tags))].Name,
			Rule: domain.MoveFirstMessage, Filter: "crypto", At: time.Now()}
		if err := marker.MarkTag(ctx, v); err != nil && !errors.Is(err, domain.ErrReleased) {
			t.Fatal(err)
		}
	}
	for i := range rooms {
		if rng.IntN(2) == 0 {
			w.setUnread(domain.Unread{RoomID: rooms[i].ID, Notifications: 1, Messages: 1, Counted: true})
		}
	}
	var log []string
	for step := range steps {
		if rng.IntN(6) == 0 {
			log = append(log, fileAtOnce(t, w, c, path, rng))
			continue
		}
		profile := []string{"home", "work"}[rng.IntN(2)]
		room := rooms[rng.IntN(len(rooms))].ID
		before := w.judged(t, path)
		tagName := cfg.Tags[rng.IntN(len(cfg.Tags))].Name
		if rng.IntN(8) == 0 {
			tagName = "Nope"
		}
		heldNow := slices.Contains(before[profile][room], tagName)
		in := !heldNow
		if rng.IntN(6) == 0 {
			in = heldNow // as it is already
		}
		msg := &v1.FileRoomRequest{RoomId: string(room), Tags: []*v1.TagFiling{{Tag: tagName, In: in}}}
		if rng.IntN(3) == 0 {
			name := []string{"Work", "Work", "Bridge", "Lone"}[rng.IntN(4)]
			msg.Spaces = []*v1.SpaceFiling{{SpaceId: string(filingSpaces[name]), In: rng.IntN(2) == 0}}
		}
		w.fmu.Lock()
		w.archiveErr = nil
		if rng.IntN(3) == 0 {
			w.archiveErr = errors.New("telegram: not now")
		}
		clear(w.spaceErr)
		switch rng.IntN(4) {
		case 0:
			w.spaceErr[filingSpaces["Work"]] = errors.New("matrix: no power")
		case 1:
			w.spaceErr[filingSpaces["Work"]] = api.ErrNoSpaceParent
		}
		w.fmu.Unlock()

		spacesHeld := profileSpaceIDs(t, c, profile) // before anything is armed: it reads the rooms

		// What lands while the filing is between its read and its write.
		var mid map[string]map[domain.RoomID][]string
		var midFile []byte
		var midCalls int
		var midVerdicts map[string]domain.TagVerdict
		var other *v1.FileRoomRequest
		var arm func()
		otherProfile, renamed, handTag := "", false, ""
		settle := func() {
			mid, midCalls, midVerdicts = w.judged(t, path), len(w.called()), w.verdictsNow()
			var err error
			if midFile, err = os.ReadFile(path); err != nil {
				t.Error(err)
			}
		}
		what := fmt.Sprintf("file %s in %s: %s in=%v spaces %v", room, profile, tagName, in, msg.GetSpaces())
		switch rng.IntN(5) {
		case 0:
			otherProfile = []string{"home", "work"}[rng.IntN(2)]
			otherRoom := rooms[rng.IntN(len(rooms))].ID
			if rng.IntN(2) == 0 {
				otherRoom = room
			}
			otherTag := cfg.Tags[rng.IntN(len(cfg.Tags))].Name
			other = &v1.FileRoomRequest{RoomId: string(otherRoom), Tags: []*v1.TagFiling{{Tag: otherTag, In: rng.IntN(2) == 0}}}
			what += fmt.Sprintf("; meanwhile %s files %s: %s", otherProfile, otherRoom, other.GetTags()[0])
			arm = func() {
				_, _ = fileRoom(ctx, c, otherProfile, other) // refused or not: whatever it left
				settle()
			}
		case 1:
			handTag = fmt.Sprintf("Hand%d", step)
			what += "; meanwhile a hand edit adds " + handTag
			arm = func() {
				edited := mustLoad(t, path)
				edited.Tags = append(edited.Tags, config.Tag{Name: handTag, Rule: []string{"room:Nobody"}})
				handSave(t, path, edited)
				settle()
			}
		case 2:
			renamed = tagName != "Nope"
			what += "; meanwhile a hand edit renames " + tagName
			arm = func() {
				edited := mustLoad(t, path)
				for i := range edited.Tags {
					if edited.Tags[i].Name == tagName {
						edited.Tags[i].Name += "x"
					}
					for j, term := range edited.Tags[i].Rule {
						if term == "tag:"+tagName {
							edited.Tags[i].Rule[j] += "x"
						}
					}
				}
				if edited.Telegram.Archive.Tag == tagName {
					edited.Telegram.Archive.Tag += "x"
				}
				handSave(t, path, edited)
				settle()
			}
		}
		settle()
		w.fmu.Lock()
		w.between, w.betweenIn = arm, profile
		w.fmu.Unlock()
		got, err := fileRoom(ctx, c, profile, msg)
		w.fmu.Lock()
		w.between = nil
		w.fmu.Unlock()
		log = append(log, what)
		where := fmt.Sprintf("seed %d step %d\n  %s", seed, step, strings.Join(log, "\n  "))
		after := w.judged(t, path)
		calls := w.called()[midCalls:]
		cfg = mustLoad(t, path) // whatever else wrote meanwhile is the config now

		inProfile := profile == "home" || !filingDirect[room]
		for _, f := range msg.GetSpaces() {
			inProfile = inProfile && slices.Contains(spacesHeld, f.GetSpaceId())
		}
		if !inProfile || tagName == "Nope" || (renamed && heldNow != in) {
			if connect.CodeOf(err) != connect.CodeNotFound {
				t.Fatalf("%s: answered %v, %v; want it refused", where, got, err)
			}
			if file, _ := os.ReadFile(path); !bytes.Equal(file, midFile) {
				t.Fatalf("%s: refused, yet the config was written:\n%s\nwas\n%s", where, file, midFile)
			}
			if len(calls) > 0 {
				t.Fatalf("%s: refused, yet the networks were asked %v", where, calls)
			}
			if v := w.verdictsNow(); !maps.Equal(v, midVerdicts) {
				t.Fatalf("%s: refused, yet the moves are %v, were %v", where, v, midVerdicts)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		if renamed {
			// Judged on the config read before the rename landed, the tag held the room
			// as asked already: nothing to write, nothing to ask.
			if file, _ := os.ReadFile(path); !bytes.Equal(file, midFile) || slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "archive ") }) {
				t.Fatalf("%s: a filing as it was wrote %s, asked %v", where, file, calls)
			}
			continue
		}
		if file, _ := os.ReadFile(path); !bytes.Equal(file, midFile) {
			heard(t, watching, config.Revision(file), where)
		}
		if handTag != "" && !slices.ContainsFunc(cfg.Tags, func(tg config.Tag) bool { return tg.Name == handTag }) {
			t.Fatalf("%s: the hand edit was lost: %+v", where, cfg.Tags)
		}
		// A filing that writes is judged again on whatever landed meanwhile, and changes
		// what that left. One as it was already when it read writes nothing, and whatever
		// lands after its read stands: it came first. (Overlapping another client's, it
		// may come second instead: then it wrote.)
		file, _ := os.ReadFile(path)
		wrote := !bytes.Equal(file, midFile)
		wasHeld := slices.Contains(mid[profile][room], tagName)
		changed, want := wasHeld != in, in
		if heldNow == in && !wrote {
			changed, want = false, wasHeld
		}
		if wrote && !changed {
			t.Fatalf("%s: written though the tag held the room as asked:\n%s", where, file)
		}
		archive := cfg.Telegram.Archive
		crosses := changed && domain.NetworkOf(string(room)) == domain.ProtocolTelegram && archive.Mirror && archive.Tag == tagName
		asked := ""
		if crosses {
			asked = fmt.Sprintf("archive %s %v", room, in)
		}
		var archiveCalls []string
		for _, call := range calls {
			if strings.HasPrefix(call, "archive ") {
				archiveCalls = append(archiveCalls, call)
			}
		}
		if (asked == "") != (len(archiveCalls) == 0) || (asked != "" && (len(archiveCalls) != 1 || archiveCalls[0] != asked)) {
			t.Fatalf("%s: the network was asked %v, want %q", where, archiveCalls, asked)
		}
		refused := crosses && w.archiveErr != nil
		if refused != (got.GetArchiveFailed() != "") {
			t.Fatalf("%s: archive_failed %q, the network refusing %v", where, got.GetArchiveFailed(), refused)
		}
		if crosses && archive.Follows() {
			// The network decides its archive: the lists say no more of the room than
			// its rule needs, whether it did as asked or refused.
			facts := w.factsIn(cfg, profile, room)
			filed := cfg.Tags[slices.IndexFunc(cfg.Tags, func(tg config.Tag) bool { return tg.Name == tagName })]
			if slices.ContainsFunc(filed.Picked, facts.Names) || (in && slices.ContainsFunc(filed.Excluded, facts.Names)) {
				t.Fatalf("%s: Telegram decides its archive, and %s's lists say more of the room: %+v", where, tagName, filed)
			}
		}
		if changed && (!crosses || !archive.Follows()) {
			// The lists name the room only where its rule, in its state as the tag sees
			// it, says otherwise than asked: one entry, nothing more.
			var caught []string // the moves as they were when it was filed: a release follows
			for k, v := range midVerdicts {
				if tag, ok := strings.CutPrefix(k, string(room)+" "); ok && v.In() {
					caught = append(caught, tag)
				}
			}
			slices.Sort(caught)
			picked, excluded, byRule := w.listsOf(t, cfg, profile, room, tagName, caught)
			if picked != (in && !byRule) || excluded != (!in && byRule) {
				t.Fatalf("%s: %s's lists name the room picked %v, excluded %v, its rule holding it %v: %+v",
					where, tagName, picked, excluded, byRule, cfg.Tags)
			}
		}
		if (!refused || !archive.Follows()) && slices.Contains(after[profile][room], tagName) != want {
			t.Fatalf("%s: %s holds it %v, want %v (it was %v); tags %+v", where, tagName, !want, want, wasHeld, cfg.Tags)
		}
		for p, byRoom := range mid {
			for id, tags := range byRoom {
				if id != room && !slices.Equal(after[p][id], tags) {
					t.Fatalf("%s: %s in %s went from %v to %v; tags %+v", where, id, p, tags, after[p][id], cfg.Tags)
				}
			}
		}
		// The release: exactly when the room leaves a tag a filter moved it into.
		key := string(room) + " " + strings.ToLower(tagName)
		moved := midVerdicts[key].In()
		wantVerdicts := maps.Clone(midVerdicts)
		if changed && !in && moved {
			wantVerdicts[key] = domain.TagVerdict{Room: room, Tag: tagName, Released: true}
		}
		if v := w.verdictsNow(); !sameVerdicts(v, wantVerdicts) {
			t.Fatalf("%s: the moves are %v, want %v", where, v, wantVerdicts)
		}
		// The spaces: each asked of the network, its outcome told.
		if len(got.GetSpaces()) != len(msg.GetSpaces()) {
			t.Fatalf("%s: %d space outcomes for %d asked", where, len(got.GetSpaces()), len(msg.GetSpaces()))
		}
		for i, f := range msg.GetSpaces() {
			wantOutcome := v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_FILED
			switch err := w.spaceErr[domain.SpaceID(f.GetSpaceId())]; {
			case errors.Is(err, api.ErrNoSpaceParent):
				wantOutcome = v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_PARENT_KEPT
			case err != nil:
				wantOutcome = v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_REFUSED
			}
			if o := got.GetSpaces()[i]; o.GetSpaceId() != f.GetSpaceId() || o.GetOutcome() != wantOutcome {
				t.Fatalf("%s: space outcome %v, want %v", where, o, wantOutcome)
			}
			if call := fmt.Sprintf("space %s %s %v", f.GetSpaceId(), room, f.GetIn()); !slices.Contains(calls, call) {
				t.Fatalf("%s: %q never asked; asked %v", where, call, calls)
			}
		}
	}
}

// FilingPlaces offers a room of the request's profile the spaces you made on its rail
// and the tags filed by hand, ticked where they hold it, and nothing for a room the
// profile does not hold or is only invited to; filing_spaces names the spaces offered (a
// bridge's too), and naming none offers nothing. A filing moves the ticks.
func TestFilingPlacesAreTheProfilesTicked(t *testing.T) {
	t.Parallel()
	w := &filingWorld{railWorld: newRailWorld(), spaceErr: map[domain.SpaceID]error{}}
	alpha := mx + "!a:x"
	w.set([]domain.Room{
		{ID: domain.RoomID(alpha), Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo", IsDirect: true}, {ID: "telegram:42/-1", Name: "Tele"},
	}, []domain.Space{
		{ID: filingSpaces["Work"], Name: "Work", Children: []domain.RoomID{domain.RoomID(alpha)}},
		{ID: filingSpaces["Bridge"], Name: "Bridge", Keeper: "matrix:@bot:x", Children: []domain.RoomID{"telegram:42/-1", domain.RoomID(alpha)}},
		{ID: filingSpaces["Lone"], Name: "Lone", Children: []domain.RoomID{mx + "!b:x"}},
	}, []domain.Room{{ID: mx + "!i:x", Name: "Asked", Membership: domain.MembershipInvite}})
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.Config{
		Tags:     []config.Tag{{Name: "Unread", Rule: []string{"unread"}}, {Name: "Pinned", Picked: []string{alpha}}, {Name: "Later"}},
		Profiles: []config.Profile{{Name: "home", Include: []string{"*"}}, {Name: "work", Include: []string{"group"}}},
	}); err != nil {
		t.Fatal(err)
	}
	h, _ := filingServed(t, w, path)
	c := daemon.Client(h.path)
	places := func(profile, room string, named ...string) (string, error) {
		req := connect.NewRequest(&v1.FilingPlacesRequest{RoomId: room, FilingSpaces: named})
		req.Header().Set("Kith-Profile", profile)
		resp, err := c.FilingPlaces(t.Context(), req)
		if err != nil {
			return "", err
		}
		var out []string
		for _, p := range resp.Msg.GetPlaces() {
			out = append(out, fmt.Sprintf("%s%s %s %v", p.GetSpace(), p.GetTag(), p.GetLabel(), p.GetIn()))
		}
		slices.Sort(out)
		if resp.Msg.GetUnmatched() {
			out = append(out, "unmatched")
		}
		return strings.Join(out, ", "), nil
	}
	for _, tc := range []struct {
		what, profile, room string
		named               []string
		want                string
	}{
		{"its own spaces and the hand-filed tags", "home", alpha, nil,
			fmt.Sprintf("Later Later false, Pinned Pinned true, %s Lone false, %s Work true", filingSpaces["Lone"], filingSpaces["Work"])},
		{"the same in another profile holding it", "work", alpha, nil,
			fmt.Sprintf("Later Later false, Pinned Pinned true, %s Work true", filingSpaces["Work"])},
		{"filing_spaces naming a bridge's", "home", alpha, []string{"space:Bridge"},
			fmt.Sprintf("Later Later false, Pinned Pinned true, %s Bridge true", filingSpaces["Bridge"])},
		{"filing_spaces naming nothing held", "home", alpha, []string{"space:Gone"}, "unmatched"},
		{"filing_spaces naming a space of another profile", "work", alpha, []string{"space:Lone"}, "unmatched"},
	} {
		if got, err := places(tc.profile, tc.room, tc.named...); err != nil || got != tc.want {
			t.Errorf("%s: %q (%v), want %q", tc.what, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ what, profile, room string }{
		{"a room of another profile", "work", mx + "!b:x"},
		{"an invitation", "home", mx + "!i:x"},
		{"a room kithd does not hold", "home", mx + "!nowhere:x"},
	} {
		if got, err := places(tc.profile, tc.room); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s: %q (%v), want it refused", tc.what, got, err)
		}
	}
	if _, err := fileRoom(t.Context(), c, "work", &v1.FileRoomRequest{RoomId: alpha, Tags: []*v1.TagFiling{{Tag: "Pinned"}, {Tag: "Later", In: true}}}); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Later Later true, Pinned Pinned false, %s Lone false, %s Work true", filingSpaces["Lone"], filingSpaces["Work"])
	if got, err := places("home", alpha); err != nil || got != want {
		t.Errorf("after a filing in another profile: %q (%v), want %q", got, err, want)
	}
}

// fileAtOnce has two clients file two rooms of "home" into tags at once: both stick.
func fileAtOnce(t *testing.T, w *filingWorld, c backendv1connect.BackendServiceClient, path string, rng *rand.Rand) string {
	t.Helper()
	cfg := mustLoad(t, path)
	before := w.judged(t, path)
	ids := slices.Clone(filingIDs)
	rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	type ask struct {
		room domain.RoomID
		tag  string
		in   bool
	}
	asks := make([]ask, 2)
	for i := range asks {
		tag := cfg.Tags[rng.IntN(len(cfg.Tags))].Name
		asks[i] = ask{ids[i], tag, !slices.Contains(before["home"][ids[i]], tag)}
	}
	w.fmu.Lock()
	w.archiveErr = nil
	w.fmu.Unlock()
	var wg sync.WaitGroup
	errs := make([]error, len(asks))
	for i, a := range asks {
		wg.Go(func() {
			_, errs[i] = fileRoom(t.Context(), c, "home", &v1.FileRoomRequest{RoomId: string(a.room), Tags: []*v1.TagFiling{{Tag: a.tag, In: a.in}}})
		})
	}
	wg.Wait()
	what := fmt.Sprintf("at once: %+v", asks)
	after := w.judged(t, path)
	for i, a := range asks {
		if errs[i] != nil {
			t.Fatalf("%s: %v", what, errs[i])
		}
		// A rule reading the other's tag, or the archive's network, may move it too: the
		// filing's own room and tag are what must stick.
		if slices.Contains(after["home"][a.room], a.tag) != a.in {
			t.Fatalf("%s: %s holds %s %v, want %v; tags %+v", what, a.tag, a.room, !a.in, a.in, mustLoad(t, path).Tags)
		}
	}
	return what
}

// heard waits for client to be told the config at revision: every client hears a filing.
func heard(t *testing.T, client *daemon.Remote, revision, what string) {
	t.Helper()
	deadline := time.After(settle)
	for {
		select {
		case snap := <-client.ConfigChanges():
			if snap.Revision == revision {
				return
			}
		case <-deadline:
			t.Fatalf("%s: other clients were never told the config filed", what)
		}
	}
}

func mustLoad(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// handSave writes cfg as a person would: straight to the file, kithd not told.
func handSave(t *testing.T, path string, cfg config.Config) {
	t.Helper()
	if err := os.WriteFile(path, []byte(config.Encode(cfg)), 0o600); err != nil {
		t.Error(err)
	}
}

// sameVerdicts compares moves and releases by room, tag, kind and filter.
func sameVerdicts(a, b map[string]domain.TagVerdict) bool {
	return maps.EqualFunc(a, b, func(x, y domain.TagVerdict) bool {
		return x.Room == y.Room && strings.EqualFold(x.Tag, y.Tag) && x.Released == y.Released && x.In() == y.In()
	})
}
