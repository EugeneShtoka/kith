package daemon

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// kithd names people, so every client shows the same name: each message, member,
// mention, thread and search hit it sends carries the person's name as domain.People
// gives it (KnownAs), beside the network's own. People is built from the
// [[display.identity]] aliases, the peers of the rooms [[display.name]] names (a direct
// chat named after its one other member lends them its name), and the directory.
//
// It is built once and kept. A config (Use) builds it again at once; a cache write that
// may change it (Changed: what a source says of people, a room's members, the rooms)
// wakes Run to. A build that names anyone differently tells every client
// (domain.ChangeNames), which reads again what it shows: a response sent between a
// write and its build carries the names before it, and the word that follows corrects
// them. A read that fails keeps what the last build read, and the next People asks for
// another build.

// namesSource is what people are named from.
type namesSource interface {
	Directory(ctx context.Context) (domain.Directory, error)
	Rooms(ctx context.Context) ([]domain.Room, error)
	Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error)
	Selves(ctx context.Context) ([]string, error)
}

// Names is how kithd names people now.
type Names struct {
	src namesSource
	// told hears a build that names someone differently; nil is silent.
	told func()
	log  *slog.Logger
	// memberNames reads a person's name in a room, for those sent by ID alone (an
	// inviter, a voter, a typist); nil reads none.
	memberNames func(ctx context.Context, room domain.RoomID, user string) (string, error)
	// wake asks Run for a build; one pending is enough.
	wake chan struct{}

	// building holds builds to one at a time, so the last to start lands last.
	building sync.Mutex

	mu sync.Mutex
	// aliases are the identities' aliases (setup.Aliases); rooms the [[display.name]]s.
	aliases map[string]string
	rooms   map[domain.RoomID]string
	// built is the last build; nil before the first.
	built *namesBuilt
}

// namesBuilt is one build: what People is made of.
type namesBuilt struct {
	aliases map[string]string
	// peers maps a named room's peer to its name; peerOf each named room to its peer,
	// what a failed read of its members falls back to.
	peers  map[string]string
	peerOf map[domain.RoomID]string
	dir    domain.Directory
	// stale is a read that failed, what it read kept from before: the next People asks
	// for another build.
	stale bool
}

// NewNames names people from src with cfg's aliases and room names; told hears each
// build that names someone differently. Nothing is read until a name is asked for.
func NewNames(src namesSource, cfg config.Config, told func()) *Names {
	n := &Names{src: src, told: told, log: slog.New(slog.DiscardHandler), wake: make(chan struct{}, 1)}
	n.aliases, n.rooms = setup.Aliases(cfg.Display.Identities), roomAliases(cfg.Display.Names)
	return n
}

// UseLogger sets where failed reads are logged. Call before use.
func (n *Names) UseLogger(log *slog.Logger) {
	if log != nil {
		n.log = log
	}
}

// UseMemberNames sets where a person's name in a room is read, the network's name for
// those kithd sends by ID alone. Call before use.
func (n *Names) UseMemberNames(names func(ctx context.Context, room domain.RoomID, user string) (string, error)) {
	n.memberNames = names
}

// Use puts cfg's aliases and room names in force and builds at once.
func (n *Names) Use(ctx context.Context, cfg config.Config) {
	n.mu.Lock()
	n.aliases, n.rooms = setup.Aliases(cfg.Display.Identities), roomAliases(cfg.Display.Names)
	n.mu.Unlock()
	n.build(ctx)
}

// Changed says a write may have changed how people are named (db.Cache.OnPeopleChange):
// Run builds again. It returns at once.
func (n *Names) Changed() {
	select {
	case n.wake <- struct{}{}:
	default: // a build is already asked for, and reads after this write
	}
}

// Run builds again each time Changed asks, until ctx ends. It blocks.
func (n *Names) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.wake:
			n.build(ctx)
		}
	}
}

// People is how people are named now, built on first use.
func (n *Names) People(ctx context.Context) domain.People {
	n.mu.Lock()
	built := n.built
	n.mu.Unlock()
	if built == nil {
		built = n.first(ctx)
	}
	if built.stale {
		n.Changed()
	}
	return built.people()
}

// first builds unless a build landed while it waited.
func (n *Names) first(ctx context.Context) *namesBuilt {
	n.building.Lock()
	defer n.building.Unlock()
	n.mu.Lock()
	built := n.built
	n.mu.Unlock()
	if built == nil {
		built = n.buildLocked(ctx)
	}
	return built
}

func (b *namesBuilt) people() domain.People {
	return domain.People{
		Alias: func(id string) string {
			if alias := b.aliases[id]; alias != "" {
				return alias
			}
			return b.peers[id]
		},
		Dir: b.dir,
	}
}

func (b *namesBuilt) equal(o *namesBuilt) bool {
	return maps.Equal(b.aliases, o.aliases) && maps.Equal(b.peers, o.peers) && b.dir.Equal(o.dir)
}

// build reads what people are named from and puts it in force, telling when anyone is
// named differently.
func (n *Names) build(ctx context.Context) {
	n.building.Lock()
	defer n.building.Unlock()
	n.buildLocked(ctx)
}

// buildLocked is build with building held; it returns the build.
func (n *Names) buildLocked(ctx context.Context) *namesBuilt {
	n.mu.Lock()
	aliases, rooms, before := n.aliases, n.rooms, n.built
	n.mu.Unlock()
	next := &namesBuilt{aliases: aliases}
	dir, err := n.src.Directory(ctx)
	next.stale = err != nil
	switch {
	case err == nil:
		next.dir = dir
	case before != nil:
		n.log.Warn("read the directory; people are named as before", "err", err)
		next.dir = before.dir
	default:
		n.log.Warn("read the directory; people are named without it", "err", err)
	}
	var read bool
	next.peers, next.peerOf, read = n.readPeers(ctx, rooms, before)
	next.stale = next.stale || !read
	n.mu.Lock()
	n.built = next
	n.mu.Unlock()
	if before != nil && !before.equal(next) && n.told != nil {
		n.told()
	}
	return next
}

// readPeers names each named room's peer by the room's name, the first room to name
// them winning; read is false when a read failed, and what it read kept from before.
func (n *Names) readPeers(ctx context.Context, named map[domain.RoomID]string, before *namesBuilt) (
	peers map[string]string, peerOf map[domain.RoomID]string, read bool,
) {
	peers, peerOf = map[string]string{}, map[domain.RoomID]string{}
	if len(named) == 0 {
		return peers, peerOf, true
	}
	rooms, err := n.src.Rooms(ctx)
	if err != nil {
		n.log.Warn("read the rooms; their peers are named as before", "err", err)
		if before != nil {
			return before.peers, before.peerOf, false
		}
		return peers, peerOf, false
	}
	selves, err := n.src.Selves(ctx)
	if err != nil {
		n.log.Warn("ask who this person is; a named room's peer may be them", "err", err)
	}
	byID := make(map[domain.RoomID]domain.Room, len(rooms))
	for i := range rooms {
		byID[rooms[i].ID] = rooms[i]
	}
	read = true
	for _, id := range slices.Sorted(maps.Keys(named)) {
		room, ok := byID[id]
		if named[id] == "" || !ok || room.IsInvite() {
			continue
		}
		peer := ""
		if members, err := n.src.Members(ctx, id, peerMembersLimit); err == nil {
			peer = peerOfRoom(room, members, selves)
		} else {
			n.log.Warn("read a named room's members; its peer is named as before", "room", id, "err", err)
			read = false
			if before != nil {
				peer = before.peerOf[id]
			}
		}
		if peer == "" {
			continue
		}
		peerOf[id] = peer
		if _, taken := peers[peer]; !taken {
			peers[peer] = named[id]
		}
	}
	return peers, peerOf, read
}

// peerOfRoom is the room's one member who is neither this person nor a bridge bot,
// when the room's own name is that member's name; "" for none.
func peerOfRoom(room domain.Room, members []domain.Member, selves []string) string {
	if room.Name == "" || len(members) >= peerMembersLimit {
		return ""
	}
	var peer domain.Member
	for _, member := range members {
		if member.UserID == "" || slices.Contains(selves, member.UserID) || domain.IsBridgeBot(member.UserID) {
			continue
		}
		if peer.UserID != "" {
			return ""
		}
		peer = member
	}
	if peer.DisplayName != room.Name {
		return ""
	}
	return peer.UserID
}

// What kithd sends, named. Each copies what it stamps: a backend may hand the same
// slice to every caller.

// messages names each message's sender and the people it mentions.
func (n *Names) messages(ctx context.Context, msgs []domain.Message) []domain.Message {
	if len(msgs) == 0 {
		return msgs
	}
	p := n.People(ctx)
	out := slices.Clone(msgs)
	for i := range out {
		nameMessage(p, &out[i])
	}
	return out
}

// message is messages for one.
func (n *Names) message(ctx context.Context, msg domain.Message) domain.Message {
	nameMessage(n.People(ctx), &msg)
	return msg
}

// page is messages for a page.
func (n *Names) page(ctx context.Context, page domain.TimelinePage) domain.TimelinePage {
	page.Messages = n.messages(ctx, page.Messages)
	return page
}

// changes is messages for the message stream's changes.
func (n *Names) changes(ctx context.Context, changes []domain.MessageChange) []domain.MessageChange {
	if len(changes) == 0 {
		return changes
	}
	p := n.People(ctx)
	out := slices.Clone(changes)
	for i := range out {
		nameMessage(p, &out[i].Message)
	}
	return out
}

// threads names each thread's latest sender.
func (n *Names) threads(ctx context.Context, threads []domain.Thread) []domain.Thread {
	if len(threads) == 0 {
		return threads
	}
	p := n.People(ctx)
	out := slices.Clone(threads)
	for i := range out {
		if out[i].LatestSender != "" {
			out[i].LatestSenderKnownAs = p.Name(out[i].LatestSender, out[i].LatestSenderName)
		}
	}
	return out
}

// members names each member.
func (n *Names) members(ctx context.Context, members []domain.Member) []domain.Member {
	if len(members) == 0 {
		return members
	}
	p := n.People(ctx)
	out := slices.Clone(members)
	for i := range out {
		if out[i].UserID != "" {
			out[i].KnownAs = p.Name(out[i].UserID, out[i].DisplayName)
		}
	}
	return out
}

// hits names each hit's sender and the people it mentions.
func (n *Names) hits(ctx context.Context, hits []domain.SearchHit) []domain.SearchHit {
	if len(hits) == 0 {
		return hits
	}
	p := n.People(ctx)
	out := slices.Clone(hits)
	for i := range out {
		if out[i].Sender != "" {
			out[i].SenderKnownAs = p.Name(out[i].Sender, out[i].SenderName)
		}
		out[i].Mentions = nameMentions(p, out[i].Mentions)
	}
	return out
}

// invites names each invitation's inviter.
func (n *Names) invites(ctx context.Context, rooms []domain.Room) []domain.Room {
	if len(rooms) == 0 {
		return rooms
	}
	p := n.People(ctx)
	out := slices.Clone(rooms)
	for i := range out {
		if out[i].InvitedBy != "" {
			out[i].InvitedByKnownAs = p.Name(out[i].InvitedBy, n.memberName(ctx, out[i].ID, out[i].InvitedBy))
		}
	}
	return out
}

// voters names each of a poll's voters in room.
func (n *Names) voters(ctx context.Context, room domain.RoomID, voters domain.PollVoters) domain.PollVoters {
	if len(voters.Voters) == 0 {
		return voters
	}
	p := n.People(ctx)
	voters.Voters = slices.Clone(voters.Voters)
	for i := range voters.Voters {
		if v := &voters.Voters[i]; v.ID != "" {
			v.KnownAs = p.Name(v.ID, v.Name, n.memberName(ctx, room, v.ID))
		}
	}
	return voters
}

// activity names everyone typing.
func (n *Names) activity(ctx context.Context, a domain.Activity) domain.Activity {
	if len(a.Typing) == 0 {
		return a
	}
	p := n.People(ctx)
	a.TypingKnownAs = make([]string, len(a.Typing))
	for i, id := range a.Typing {
		a.TypingKnownAs[i] = p.Name(id, n.memberName(ctx, a.RoomID, id))
	}
	return a
}

// memberName is user's name in room as the network gives it, "" when unknown.
func (n *Names) memberName(ctx context.Context, room domain.RoomID, user string) string {
	if n.memberNames == nil {
		return ""
	}
	name, err := n.memberNames(ctx, room, user)
	if err != nil {
		n.log.Warn("read a member's name; they are named without it", "err", err)
	}
	return name
}

func nameMessage(p domain.People, msg *domain.Message) {
	if msg.Sender != "" {
		msg.SenderKnownAs = p.Name(msg.Sender, msg.SenderName)
	}
	msg.Mentions = nameMentions(p, msg.Mentions)
}

// nameMentions names each person mentioned by their name in the room, then what the
// mention calls them.
func nameMentions(p domain.People, mentions []domain.Mention) []domain.Mention {
	if len(mentions) == 0 {
		return mentions
	}
	out := slices.Clone(mentions)
	for i := range out {
		if out[i].UserID != "" {
			out[i].KnownAs = p.Name(out[i].UserID, out[i].Known, out[i].Words())
		}
	}
	return out
}

// KnownAs names each person asked for as everything kithd sends names them.
func (s *server) KnownAs(ctx context.Context, r *req[v1.KnownAsRequest]) (*resp[v1.KnownAsResponse], error) {
	p := s.Names.People(ctx)
	ids := r.Msg.GetUserIds()
	out := make([]string, len(ids))
	for i, id := range ids {
		if id != "" {
			out[i] = p.Name(id)
		}
	}
	return connect.NewResponse(&v1.KnownAsResponse{KnownAs: out}), nil
}
