package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// What each request field names, as this test reads it: by the field's name, with the
// exceptions written out. It is the oracle's, kept apart from kithd's own table.
const (
	namesRoom    = "room"    // a room or a space: the profile's rail must hold it
	namesMessage = "message" // every room the cache holds it in must be the profile's
	namesJoin    = "join"    // a room or an address to join: one kithd holds must be the profile's
	namesQueued  = "queued"  // a queued message's ID: its room must be one the queue lists
)

var (
	roomFieldNames = []protoreflect.Name{
		"room_id", "room_ids", "space_id", "space_rooms", "space_room_ids", "parent", "open_room_id", "before_room_id",
	}
	messageFieldNames = []protoreflect.Name{
		"event_id", "target", "root", "reply_to", "edits", "editing", "thread_root", "before_event_id",
	}
	// fieldExceptions are fields whose name says otherwise: rooms a query narrows itself
	// (checked by TestAProfileSeesOnlyItsRooms), the device's filing entries, addresses
	// to join and queued IDs.
	fieldExceptions = map[protoreflect.FullName]string{
		"backend.v1.SearchMessagesRequest.room_ids": "", "backend.v1.SearchSendersRequest.room_ids": "",
		"backend.v1.RoomsWithRequest.room_ids": "", "backend.v1.MessagesFromRequest.room_ids": "",
		"backend.v1.FilingPlacesRequest.filing_spaces": "",
		"backend.v1.JoinRoomRequest.room_id_or_alias":  namesJoin, "backend.v1.KnockRoomRequest.room_id_or_alias": namesJoin,
		"backend.v1.CancelScheduledRequest.id": namesQueued, "backend.v1.RetryScheduledRequest.id": namesQueued,
	}
)

func fieldNames(f protoreflect.FieldDescriptor) string {
	if kind, ok := fieldExceptions[f.FullName()]; ok {
		return kind
	}
	switch {
	case slices.Contains(roomFieldNames, f.Name()):
		return namesRoom
	case slices.Contains(messageFieldNames, f.Name()):
		return namesMessage
	}
	return ""
}

// namedField is one field a call's request names something in, by its path from the
// request.
type namedField struct {
	method protoreflect.MethodDescriptor
	path   []protoreflect.FieldDescriptor
	kind   string
}

func (f namedField) String() string {
	parts := make([]string, len(f.path))
	for i, p := range f.path {
		parts[i] = string(p.Name())
	}
	return string(f.method.Name()) + "." + strings.Join(parts, ".")
}

// namedFieldsOf is every field of every unary call's request that names something.
func namedFieldsOf() []namedField {
	var out []namedField
	methods := v1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	for i := range methods.Len() {
		m := methods.Get(i)
		if m.IsStreamingServer() {
			continue
		}
		var walk func(md protoreflect.MessageDescriptor, path []protoreflect.FieldDescriptor)
		walk = func(md protoreflect.MessageDescriptor, path []protoreflect.FieldDescriptor) {
			for j := range md.Fields().Len() {
				f := md.Fields().Get(j)
				here := append(slices.Clone(path), f)
				switch {
				case f.IsMap():
				case f.Kind() == protoreflect.StringKind:
					if kind := fieldNames(f); kind != "" {
						out = append(out, namedField{method: m, path: here, kind: kind})
					}
				case f.Kind() == protoreflect.MessageKind && len(path) < 4:
					walk(f.Message(), here)
				}
			}
		}
		walk(m.Input(), nil)
	}
	return out
}

// request is a call's request with id at f, and base's fields set beside it.
func (f namedField) request(t *testing.T, id string, base map[protoreflect.Name]string) proto.Message {
	t.Helper()
	mt, err := protoregistry.GlobalTypes.FindMessageByName(f.method.Input().FullName())
	if err != nil {
		t.Fatal(err)
	}
	msg := mt.New()
	for name, value := range base {
		if fd := msg.Descriptor().Fields().ByName(name); fd != nil && name != f.path[0].Name() {
			msg.Set(fd, protoreflect.ValueOfString(value))
		}
	}
	at := msg
	for _, fd := range f.path[:len(f.path)-1] {
		if fd.IsList() {
			list := at.Mutable(fd).List()
			el := list.NewElement()
			list.Append(el)
			at = el.Message()
		} else {
			at = at.Mutable(fd).Message()
		}
	}
	last := f.path[len(f.path)-1]
	if last.IsList() {
		at.Mutable(last).List().Append(protoreflect.ValueOfString(id))
	} else {
		at.Set(last, protoreflect.ValueOfString(id))
	}
	return msg.Interface()
}

// rawCaller makes any BackendService call over the socket, as the Connect protocol
// carries it, naming a profile or none.
type rawCaller struct{ client *http.Client }

func newRawCaller(path string) rawCaller {
	return rawCaller{client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}}
}

// outcome is how a call went for the profile check.
type outcome int

const (
	allowed   outcome = iota + 1 // past the check (the handler's own failure included)
	notInIt                      // refused: the profile holds no such room or message
	noSuchOne                    // refused: the profile is gone from the config
)

func (o outcome) String() string {
	return map[outcome]string{allowed: "allowed", notInIt: "refused", noSuchOne: "refused (no such profile)"}[o]
}

func (c rawCaller) call(ctx context.Context, m protoreflect.MethodDescriptor, msg proto.Message, profile string) (outcome, string) {
	body, err := proto.Marshal(msg)
	if err != nil {
		return 0, err.Error()
	}
	url := fmt.Sprintf("http://kithd/%s/%s", m.Parent().FullName(), m.Name())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	if profile != "" {
		req.Header.Set("Kith-Profile", profile)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		return allowed, ""
	}
	var failed struct{ Code, Message string }
	_ = json.Unmarshal(got, &failed)
	switch {
	case failed.Code == "not_found" && strings.Contains(failed.Message, " holds no "):
		return notInIt, failed.Message
	case strings.Contains(failed.Message, "no such profile"):
		return noSuchOne, failed.Message
	}
	return allowed, failed.Code + ": " + failed.Message
}

// namedWorld is a profileWorld whose typing reaches clients too, and whose link
// resolves to the chat last put on link.
type namedWorld struct {
	*profileWorld
	acts chan domain.Activity
	link chan domain.RoomID
}

func (w namedWorld) ResolveLink(context.Context, string, domain.RoomOwner) (domain.LinkTarget, error) {
	select {
	case id := <-w.link:
		return domain.LinkTarget{Room: domain.Room{ID: id}}, nil
	default:
		return domain.LinkTarget{}, nil
	}
}

// draw is one thing a request names, and whether a profile may name it.
type draw struct {
	label string
	id    string
	may   bool
}

// mustLand waits until the cache holds each message in its room.
func mustLand(t *testing.T, cache *messageCache, held map[domain.EventID]domain.RoomID) {
	t.Helper()
	deadline := time.Now().Add(settle)
	for event, room := range held {
		for {
			rooms, _ := cache.RoomsOf(t.Context(), event)
			if slices.Contains(rooms, room) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the cache never held %s in %s", event, room)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// A client naming a profile is answered about a room, a space or a message only when the
// profile holds it, as its rail does; a client naming none is answered about any. Over
// generated worlds and profiles, every field of every call that names something is drawn
// a room of the profile, of another, an invitation, a space, a room kithd does not hold,
// a message held in each and one held nowhere, an address and a queued message; then the
// profile is drawn again (rooms moving between profiles on a config change), taken away,
// and not named at all. Each call is refused or answered exactly by the rule, and the
// streams of every room (messages, reactions, typing) carry only the profile's rooms'.
func TestAProfiledCallNamesOnlyItsProfilesRooms(t *testing.T) {
	t.Parallel()
	fields := namedFieldsOf()
	if len(fields) < 80 {
		t.Fatalf("only %d named fields found: the oracle's names have drifted from the protocol", len(fields))
	}
	pw := &profileWorld{Nop: nopWithChannels()}
	w := namedWorld{profileWorld: pw, acts: make(chan domain.Activity), link: make(chan domain.RoomID, 1)}
	pw.Acts = w.acts
	n, err := daemon.NewNotifications(asStarted(config.Config{}), scopeOnly{w})
	if err != nil {
		t.Fatal(err)
	}
	var cache *messageCache
	h := serve(t, w, func(d *daemon.Daemon) {
		d.Notifications = n
		cache = cacheOf(d.Streams)
		d.MessageLog = cache
	})
	pw.unnamed = h.client()
	caller := newRawCaller(h.path)
	ctx := t.Context()
	// The first profile, which a client naming none is narrowed to on the calls that
	// narrow, holds only some rooms, so a narrowing where none belongs shows.
	first := config.Profile{Name: "first", Include: []string{"protocol:telegram"}}
	if err := n.Reload(config.Config{Profiles: []config.Profile{first, {Name: "p", Include: []string{"*"}}}}); err != nil {
		t.Fatal(err)
	}
	profiled, unnamed := streamingClient(t, "p", h), streamingClient(t, "", h)
	h.waitAttached(2)
	warmTyping(t, w, profiled, unnamed)

	seeds := 10
	if testing.Short() {
		seeds = 3
	}
	for seed := range uint64(seeds) {
		rng := rand.New(rand.NewPCG(seed, 0x6e616d65))
		g := generate(rng)
		for i := range g.invites {
			g.invites[i].Membership = domain.MembershipInvite // as kithd's cache lists them
		}
		pw.set(g.rooms, g.spaces, g.invites)
		n.InvalidateScope()
		reload := func(profiles ...config.Profile) {
			t.Helper()
			cfg := notifsOn("all")
			cfg.Tags, cfg.Profiles = g.tags, append([]config.Profile{first}, profiles...)
			if err := n.Reload(cfg); err != nil {
				t.Fatalf("seed %d: reload %+v: %v", seed, profiles, err)
			}
		}
		unknown := domain.RoomID(fmt.Sprintf(mx+"!nowhere%d:x", seed))

		// A message held in each room, in a room kithd does not hold, and one held nowhere.
		held := map[domain.EventID]domain.RoomID{}
		for _, id := range slices.Concat(idsOf(g.rooms), idsOf(g.invites), []domain.RoomID{unknown}) {
			event := domain.EventID(fmt.Sprintf("%s$%d-%s", mx, seed, domain.ParseID(string(id)).Native))
			held[event] = id
			pw.Msgs <- domain.Message{ID: event, RoomID: id, Body: "held"}
		}
		mustLand(t, cache, held)

		judged := g
		for _, ph := range []string{"p", "p drawn again", "p taken away", "none named"} {
			header := "p"
			switch ph {
			case "p":
				reload(judged.profile)
			case "p drawn again":
				// The same rooms, the profile's entries changed: no scope is invalidated but
				// the reload's own.
				judged = g.withProfile(rng)
				reload(judged.profile)
			case "p taken away":
				reload()
			case "none named":
				// A filing judges its room against the first profile.
				header, judged = "", g.judgedBy(first)
			}
			what := fmt.Sprintf("seed %d, %s %+v", seed, ph, judged.profile)
			// A message queued in each room, by a client naming no profile; afresh in each
			// phase, as a call allowed in the last may have canceled one.
			queued := map[string]domain.RoomID{}
			for _, id := range slices.Concat(idsOf(g.rooms), idsOf(g.invites), []domain.RoomID{unknown}) {
				qid, err := pw.unnamed.Schedule(ctx, domain.ScheduledMessage{RoomID: id, Body: "later", At: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				queued[qid] = id
			}
			draws := judged.draws(header, ph, unknown, held, queued)
			for _, f := range fields {
				// A filing judges its room and spaces itself too, by its own rule: a joined
				// room, and spaces, of the profile named or the first.
				pool := draws[f.kind]
				if name := f.method.Name(); name == "FilingPlaces" || name == "FileRoom" {
					pool = draws[map[protoreflect.Name]string{"room_id": "filed", "spaces": "filed into"}[f.path[0].Name()]]
				}
				base := filingBase(f, draws)
				if base == nil {
					continue
				}
				for _, d := range sample(rng, pool) {
					want := allowed
					switch {
					case ph == "p taken away":
						want = noSuchOne
					case !d.may:
						want = notInIt
					}
					got, why := caller.call(ctx, f.method, f.request(t, d.id, base), header)
					if got != want {
						t.Errorf("%s: %s naming %s (%s): %v, want %v (%s)", what, f, d.label, d.id, got, want, why)
					}
				}
			}
			for _, d := range draws[namesJoin] {
				if strings.HasPrefix(d.id, "#") || !strings.Contains(d.id, "/") {
					continue // an address or a network's own ID: not a chat ResolveLink answers
				}
				want := allowed
				switch {
				case ph == "p taken away":
					want = noSuchOne
				case !d.may:
					want = notInIt
				}
				w.link <- domain.RoomID(d.id)
				got, why := caller.call(ctx, v1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods().ByName("ResolveLink"),
					&v1.ResolveLinkRequest{Uri: "matrix:roomid/x"}, header)
				select { // a call refused before the backend leaves it
				case <-w.link:
				default:
				}
				if got != want {
					t.Errorf("%s: ResolveLink answering %s (%s): %v, want %v (%s)", what, d.label, d.id, got, want, why)
				}
			}
			if ph == "p" || ph == "p drawn again" || ph == "p taken away" {
				checkProfiledStreams(t, what, pw, w, profiled, unnamed, judged, ph == "p taken away", unknown)
			}
		}
	}
}

// filingBase is the fields a filing call needs beside the one drawn: a room the profile
// files, which it judges first; nil when there is none to file. Other calls need none.
func filingBase(f namedField, draws map[string][]draw) map[protoreflect.Name]string {
	if name := f.method.Name(); (name != "FilingPlaces" && name != "FileRoom") || f.path[0].Name() == "room_id" {
		return map[protoreflect.Name]string{}
	}
	for _, d := range draws["filed"] {
		if d.may {
			return map[protoreflect.Name]string{"room_id": d.id}
		}
	}
	return nil
}

// sample is one draw of each label, picked at random among those it has.
func sample(rng *rand.Rand, pool []draw) []draw {
	byLabel := map[string][]draw{}
	var labels []string
	for _, d := range pool {
		if _, ok := byLabel[d.label]; !ok {
			labels = append(labels, d.label)
		}
		byLabel[d.label] = append(byLabel[d.label], d)
	}
	out := make([]draw, 0, len(labels))
	for _, l := range labels {
		out = append(out, byLabel[l][rng.IntN(len(byLabel[l]))])
	}
	return out
}

// draws is what g's profile may name, by kind, for a client naming header ("" none). A
// room is the profile's as its rail holds it; a queued message's and a stream's room as
// the profile's lists judge it (a room kithd does not hold by its ID and network).
func (g generated) draws(header, phase string, unknown domain.RoomID, held map[domain.EventID]domain.RoomID, queued map[string]domain.RoomID) map[string][]draw {
	named := header != "" && phase != "p taken away"
	may := func(in bool) bool { return !named || in }
	listed := func(id domain.RoomID) bool { return !named || g.listed(id) }
	out := map[string][]draw{}
	add := func(kind, label string, id string, ok bool) {
		out[kind] = append(out[kind], draw{label: label, id: id, may: ok})
	}
	for _, id := range idsOf(g.rooms) {
		label := map[bool]string{true: "a room of the profile", false: "a room of another"}[g.in[id]]
		add(namesRoom, label, string(id), may(g.in[id]))
		add(namesJoin, label, string(id), may(g.in[id]))
		// A filing judges the room itself, against the first profile when none is named.
		add("filed", label, string(id), g.in[id])
		add("filed into", "a room, not a space", string(id), false)
		if native := domain.ParseID(string(id)).Native; strings.HasPrefix(native, "!") {
			add(namesJoin, "a room by the network's own ID", native, may(g.in[id]))
		}
	}
	for _, id := range idsOf(g.invites) {
		label := map[bool]string{true: "an invitation of the profile", false: "an invitation of another"}[g.in[id]]
		add(namesRoom, label, string(id), may(g.in[id]))
		add(namesJoin, label, string(id), may(g.in[id]))
		add("filed", "an invitation", string(id), false) // filing files a joined room only
	}
	for _, s := range g.spaces {
		label := map[bool]string{true: "a space the profile reaches", false: "a space it does not"}[g.spacesIn[domain.RoomID(s.ID)]]
		add(namesRoom, label, string(s.ID), may(g.spacesIn[domain.RoomID(s.ID)]))
		add(namesJoin, label, string(s.ID), may(g.spacesIn[domain.RoomID(s.ID)]))
		add("filed into", label, string(s.ID), g.spacesIn[domain.RoomID(s.ID)])
	}
	add(namesRoom, "a room kithd does not hold", string(unknown), !named)
	add("filed", "a room kithd does not hold", string(unknown), false)
	add("filed into", "a room kithd does not hold", string(unknown), false)
	add(namesJoin, "a room kithd does not hold", string(unknown), true)
	add(namesJoin, "an alias", "#somewhere:example.org", true)
	for event, room := range held {
		in := g.in[room] && room != unknown
		label := map[bool]string{true: "a message of the profile's", false: "a message of another room"}[in]
		if room == unknown {
			label = "a message in a room kithd does not hold"
		}
		add(namesMessage, label, string(event), may(in))
	}
	add(namesMessage, "a message held nowhere", mx+"$nowhere", true)
	for id, room := range queued {
		label := map[bool]string{true: "a message queued in the profile's room", false: "a message queued elsewhere"}[g.listed(room)]
		add(namesQueued, label, id, listed(room))
	}
	add(namesQueued, "an ID nothing is queued under", "0000000000000000", true)
	return out
}

// listed is whether the profile's lists hold a room (ScheduledMessages, the streams): a
// joined room as the rail does; another, an invitation among them, by what kithd knows
// of it unplaced, its ID and network.
func (g generated) listed(id domain.RoomID) bool {
	if slices.Contains(idsOf(g.rooms), id) {
		return g.in[id]
	}
	return g.holds(&domain.Room{ID: id}, false)
}

// streamingClient is a client of h that names profile ("" none), its streams opening.
func streamingClient(t *testing.T, profile string, h *harness) *daemon.Remote {
	t.Helper()
	r := h.client()
	r.UseProfile(profile)
	go func() { _ = r.Start(t.Context()) }()
	t.Cleanup(r.Stop)
	return r
}

// warmTyping sends typing until both clients have it: the typing stream is not among
// those Attached counts, so it may open after the rest.
func warmTyping(t *testing.T, w namedWorld, clients ...*daemon.Remote) {
	t.Helper()
	deadline := time.Now().Add(settle)
	for _, c := range clients {
		for got := false; !got; {
			if time.Now().After(deadline) {
				t.Fatal("the typing stream never opened")
			}
			w.acts <- domain.Activity{RoomID: mx + "!warm:x", Typing: []string{"warm"}}
			select {
			case <-c.Activity():
				got = true
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}

// checkProfiledStreams has every room (and one kithd does not hold) send a message, a
// reaction and typing, and reads which reach the profiled client and the one naming no
// profile: the profile's rooms' alone, and every room's.
func checkProfiledStreams(t *testing.T, what string, pw *profileWorld, w namedWorld, profiled, unnamed *daemon.Remote, g generated, gone bool, unknown domain.RoomID) {
	t.Helper()
	rooms := slices.Concat(idsOf(g.rooms), idsOf(g.invites), []domain.RoomID{unknown})
	var want []domain.RoomID
	for _, id := range rooms {
		if !gone && g.listed(id) {
			want = append(want, id)
		}
	}
	slices.Sort(want)
	every := slices.Sorted(slices.Values(rooms))
	tag := fmt.Sprintf("%s %d", what, time.Now().UnixNano())
	for _, id := range rooms {
		pw.Msgs <- domain.Message{ID: domain.EventID(fmt.Sprintf("%s$%s-%s", mx, tag, id)), RoomID: id, Body: tag}
		pw.Reacts <- domain.ReactionUpdate{Reaction: domain.Reaction{ID: domain.EventID(tag + string(id)), RoomID: id, Key: tag}}
		w.acts <- domain.Activity{RoomID: id, Typing: []string{tag}}
	}
	for name, client := range map[string]*daemon.Remote{"the profiled client": profiled, "the client naming none": unnamed} {
		expect := every
		if client == profiled {
			expect = want
		}
		got := readTagged(t, len(expect), client.Messages(), func(m domain.Message) (domain.RoomID, bool) { return m.RoomID, m.Body == tag })
		if !slices.Equal(got, expect) {
			t.Errorf("%s: %s was sent messages of %v, want %v", what, name, got, expect)
		}
		got = readTagged(t, len(expect), client.Reactions(), func(u domain.ReactionUpdate) (domain.RoomID, bool) {
			return u.Reaction.RoomID, u.Reaction.Key == tag
		})
		if !slices.Equal(got, expect) {
			t.Errorf("%s: %s was sent reactions in %v, want %v", what, name, got, expect)
		}
		got = readTagged(t, len(expect), client.Activity(), func(a domain.Activity) (domain.RoomID, bool) {
			return a.RoomID, slices.Equal(a.Typing, []string{tag})
		})
		if !slices.Equal(got, expect) {
			t.Errorf("%s: %s was sent typing in %v, want %v", what, name, got, expect)
		}
	}
}

// readTagged reads the rooms of the values carrying this round's tag, until want have
// come and a little longer, so one too many shows; others are passed over.
func readTagged[T any](t *testing.T, want int, ch <-chan T, roomOf func(T) (domain.RoomID, bool)) []domain.RoomID {
	t.Helper()
	var got []domain.RoomID
	quiet := time.After(settle)
	for {
		wait := quiet
		if len(got) >= want {
			wait = time.After(30 * time.Millisecond)
		}
		select {
		case v := <-ch:
			if room, ours := roomOf(v); ours {
				got = append(got, room)
			}
		case <-wait:
			slices.Sort(got)
			return got
		}
	}
}
