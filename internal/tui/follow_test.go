package tui

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Following a link to a place has three answers: go there, ask before joining, or
// open the switcher.

// linkMsg is a message carrying one address.
func linkMsg(body, html string) domain.Message {
	return domain.Message{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@her:x", Body: body, Format: richtext.FromMarkup(html)}
}

// A link to a room you are in is a move, not a question.
func TestFollowingALinkToAJoinedRoomOpensIt(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("see https://matrix.to/#/!b:x for details", "")})
	m.timeline.selected = mx + "$1"

	next, _ := m.takeContext(actOpenURL)
	got := next
	if got.openRoom != mx+"!b:x" {
		t.Errorf("open room = %q, want !b:x", got.openRoom)
	}
	if got.confirm.active() {
		t.Error("following a link to a room we are in asked a question")
	}
}

// A link to a message queues the jump until the timeline holds it, as a search hit does.
func TestFollowingALinkToAMessageQueuesTheJump(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("https://matrix.to/#/!b:x/$deep", "")})
	m.timeline.selected = mx + "$1"

	next, _ := m.takeContext(actOpenURL)
	got := next
	if got.openRoom != mx+"!b:x" {
		t.Fatalf("open room = %q, want !b:x", got.openRoom)
	}
	if got.jump.to != mx+"$deep" {
		t.Errorf("jump.to = %q, want $deep", got.jump.to)
	}
}

// Joining is visible to everyone in the room, so a link to an unjoined room asks
// first, naming what it would join.
func TestFollowingALinkToARoomWeAreNotInAsksFirst(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("https://matrix.to/#/%23book-club:example.org?via=one.org", "")})
	m.timeline.selected = mx + "$1"

	next, _ := m.takeContext(actOpenURL)
	got := next
	if got.confirm.action != pendingJoinPlace {
		t.Fatalf("confirm action = %v, want pendingJoinPlace", got.confirm.action)
	}
	if got.confirm.address != "#book-club:example.org" {
		t.Errorf("would join %q, want #book-club:example.org", got.confirm.address)
	}
	// The routing servers make a room on an unknown homeserver reachable, so they survive
	// the question.
	if len(got.confirm.via) != 1 || got.confirm.via[0] != "one.org" {
		t.Errorf("via = %+v, want [one.org]", got.confirm.via)
	}
	if q := got.confirmPrompt(); !strings.Contains(q, "#book-club:example.org") {
		t.Errorf("the question does not name the room: %q", q)
	}
}

// A link to a person opens the switcher listing their rooms instead of making a room.
func TestFollowingALinkToAPersonOpensTheSwitcher(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("matrix:u/alice:example.org said so", "")})
	m.timeline.selected = mx + "$1"

	next, _ := m.takeContext(actOpenURL)
	got := next
	if got.picker.kind != pickerJump {
		t.Fatalf("picker = %v, want the switcher", got.picker.kind)
	}
	if got.picker.filter == "" {
		t.Error("the switcher opened unfiltered, so it did not follow anybody")
	}
	if got.confirm.active() {
		t.Error("following a person asked to join something")
	}
}

// A mention pill's address is in the markup, not the words, so the formatted body is read.
func TestFollowingFindsTheAddressInsideAPill(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg(
		"Alice said so",
		`<a href="https://matrix.to/#/@alice:example.org">Alice</a> said so`,
	)})
	m.timeline.selected = mx + "$1"

	next, _ := m.takeContext(actOpenURL)
	got := next
	if got.picker.kind != pickerJump {
		t.Errorf("picker = %v, want the switcher — the pill's target was not read", got.picker.kind)
	}
}

// Several addresses ask which, by the one-or-ask rule.
func TestSeveralPlacesAsksWhich(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg(
		"https://matrix.to/#/!b:x and https://matrix.to/#/@alice:example.org", "")})
	m.timeline.selected = mx + "$1"

	next, _ := m.takeContext(actOpenURL)
	got := next
	if got.picker.kind != pickerContext || got.picker.then != actOpenURL {
		t.Fatalf("picker = %v/%v, want the follow chooser", got.picker.kind, got.picker.then)
	}
	if len(got.picker.items) != 2 {
		t.Errorf("%d rows, want 2", len(got.picker.items))
	}
	// A known room is shown by name, not ID.
	if got.picker.items[0].label != "Bravo" {
		t.Errorf("first row = %q, want the room's name", got.picker.items[0].label)
	}
}

// Answering the join question carries the address through to the call.
func TestConfirmingAJoinJoinsWhatTheLinkNamed(t *testing.T) {
	t.Parallel()

	b := &membershipBackend{}
	m := withRooms(t, sized(t, starterNew(b, config.Display{})))
	m.accounts = []domain.Account{{Network: domain.ProtocolMatrix, On: domain.RoomOwner(mx)}}
	m.confirm = confirmState{action: pendingJoinPlace, address: "#book-club:example.org", via: []string{"one.org"}}

	next, cmd := m.resolveConfirm(true)
	got := next
	if got.confirm.active() {
		t.Error("the question outlived its answer")
	}
	if cmd == nil {
		t.Fatal("confirming produced no join")
	}
	cmd() // the command is what calls the backend
	if len(b.joined) != 1 || b.joined[0] != mx+"#book-club:example.org" {
		t.Fatalf("joined %+v, want [#book-club:example.org] on the one Matrix account", b.joined)
	}
	if len(b.joinedVia) != 1 || b.joinedVia[0] != "one.org" {
		t.Errorf("joined via %+v, want [one.org]", b.joinedVia)
	}
}

// A link handed over by the desktop while running moves the view and says so.
func TestAHandedOverLinkMovesAndSaysSo(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	next, _ := m.handleFollow(followMsg{uri: "https://matrix.to/#/!b:x"})
	got := next

	if got.openRoom != mx+"!b:x" {
		t.Errorf("open room = %q, want !b:x", got.openRoom)
	}
	if !strings.Contains(got.status(), "Bravo") {
		t.Errorf("status = %q, want it to name where it went", got.status())
	}
}

// A handed-over non-link is reported instead of moving.
func TestAHandedOverNonLinkIsReported(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	next, _ := m.handleFollow(followMsg{uri: "https://example.org/nope"})
	got := next

	if got.openRoom == mx+"!b:x" {
		t.Error("a non-link moved the client")
	}
	if !strings.Contains(got.status(), "not a Matrix, Telegram, WhatsApp or Signal link") {
		t.Errorf("status = %q, want it to say what was wrong", got.status())
	}
}

// A tg:// link to a chat the Telegram account sees is a move to that chat.
func TestAHandedOverTelegramLinkOpensTheChat(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "telegram:42/-1000000000021", Name: "Novo Salsa", Handle: "novosalsa"},
		{ID: mx + "!b:x", Name: "Bravo"},
	}})

	next, _ := m.handleFollow(followMsg{uri: "tg://resolve?domain=novosalsa&post=35"})
	if next.openRoom != "telegram:42/-1000000000021" {
		t.Errorf("open room = %q, want the chat matching @novosalsa", next.openRoom)
	}
	if next.jump.to != "telegram:42/-1000000000021/35" {
		t.Errorf("jump.to = %q, want the post as a kith event ID", next.jump.to)
	}
	if !strings.Contains(next.status(), "Novo Salsa") {
		t.Errorf("status = %q, want it to name the chat", next.status())
	}
}

// A tg:// link whose @username matches no chat, cached or on Telegram, says so instead
// of guessing.
func TestAHandedOverTelegramLinkToAnUnknownChatIsReported(t *testing.T) {
	t.Parallel()

	for name, b := range map[string]*linkBackend{
		"Telegram finds nothing": {},
		"the lookup fails":       {err: errors.New("USERNAME_NOT_OCCUPIED")},
	} {
		m := withRooms(t, sized(t, starterNew(b, config.Display{})))
		next := followed(t, m, "tg://resolve?domain=novosalsa")
		if next.openRoom != m.openRoom {
			t.Errorf("%s: an unknown Telegram @username moved the client to %q", name, next.openRoom)
		}
		if !strings.Contains(next.status(), "no chat found") {
			t.Errorf("%s: status = %q, want it to say the chat was not found", name, next.status())
		}
	}
}

// linkBackend answers link lookups with target (or err), and joins to joinTo; it has
// accounts (one Telegram and one WhatsApp account when nil).
type linkBackend struct {
	apitest.Nop
	target   domain.LinkTarget
	err      error
	joinTo   domain.RoomID
	accounts []domain.Account
	resolved []string
	joined   []string
	// on is the account each lookup and join was asked on.
	on []domain.RoomOwner
}

// linkAccounts are a test backend's accounts by default.
var linkAccounts = []domain.Account{
	{Network: domain.ProtocolTelegram, On: domain.AccountRooms(domain.ProtocolTelegram, "42"), Name: "tg"},
	{Network: domain.ProtocolWhatsApp, On: domain.AccountRooms(domain.ProtocolWhatsApp, "15550100001"), Name: "wa"},
	{Network: domain.ProtocolSignal, On: domain.AccountRooms(domain.ProtocolSignal, "11111111-1111-4111-8111-111111111111"), Name: "sig"},
}

func (b *linkBackend) Accounts(context.Context) ([]domain.Account, error) {
	if b.accounts == nil {
		return linkAccounts, nil
	}
	return b.accounts, nil
}

func (b *linkBackend) ResolveLink(_ context.Context, uri string, on domain.RoomOwner) (domain.LinkTarget, error) {
	b.resolved = append(b.resolved, uri)
	b.on = append(b.on, on)
	return b.target, b.err
}

func (b *linkBackend) JoinLink(_ context.Context, uri string, on domain.RoomOwner) (domain.RoomID, error) {
	b.joined = append(b.joined, uri)
	b.on = append(b.on, on)
	return b.joinTo, nil
}

// followed hands uri over and delivers what it waits on in turn: the accounts it picks
// among, then the lookup it starts.
func followed(t *testing.T, m Model, uri string) Model {
	t.Helper()
	next, cmd := m.handleFollow(followMsg{uri: uri})
	return linkSteps(t, next, cmd)
}

// linkSteps runs cmd and delivers the account list or link lookup it yields, and what
// that yields, until neither comes.
func linkSteps(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for range 4 {
		var picked *accountsPickedMsg
		var resolved *linkResolvedMsg
		for _, msg := range ranMsgs(cmd) {
			switch msg := msg.(type) {
			case accountsPickedMsg:
				picked = &msg
			case linkResolvedMsg:
				resolved = &msg
			}
		}
		switch {
		case picked != nil:
			m, cmd = m.handleAccountsPicked(*picked)
		case resolved != nil:
			return update(t, m, *resolved)
		default:
			return m
		}
	}
	return m
}

// ranMsgs runs cmd and every command a batch holds, and returns what they yield.
func ranMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		out = append(out, ranMsgs(c)...)
	}
	return out
}

// ezraChat is the private chat a link to somebody new opens.
var ezraChat = domain.Room{ID: "telegram:42/9", Name: "Ezra", IsDirect: true, Handle: "ezra"}

// A link to somebody kith has no chat with opens a fresh chat to write in, with the
// message the link suggests in the composer.
func TestAHandedOverLinkToSomebodyNewOpensAFreshChat(t *testing.T) {
	t.Parallel()

	b := &linkBackend{target: domain.LinkTarget{Room: ezraChat}}
	m := withRooms(t, sized(t, starterNew(b, config.Display{})))
	next := followed(t, m, "tg://resolve?domain=ezra&text=hello%20Ezra")

	if len(b.resolved) != 1 {
		t.Fatalf("asked the network %v, want the link once", b.resolved)
	}
	if next.openRoom != ezraChat.ID {
		t.Errorf("open room = %q, want the fresh chat with Ezra", next.openRoom)
	}
	if room, ok := next.currentRoom(); !ok || room.Name != "Ezra" {
		t.Errorf("current room = (%+v, %v), want Ezra's chat to be one the composer sends to", room, ok)
	}
	if next.compose.input != "hello Ezra" {
		t.Errorf("composer = %q, want the suggested message", next.compose.input)
	}
	if !strings.Contains(next.status(), "Ezra") {
		t.Errorf("status = %q, want it to name the chat", next.status())
	}
}

// A suggested message never replaces a draft already in the composer.
func TestASuggestedMessageKeepsTheDraft(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = update(t, m, roomsMsg{rooms: []domain.Room{ezraChat}})
	m, _ = m.goToRoom(ezraChat.ID)
	m.compose.input = "my own words"
	next := followed(t, m, "https://t.me/ezra?text=hello")
	if next.compose.input != "my own words" {
		t.Errorf("composer = %q, want the draft kept", next.compose.input)
	}
}

// A group or channel the account is not in is asked about, then joined, then opened
// once the room list holds it.
func TestALinkToAChatNotJoinedAsksThenJoinsAndEnters(t *testing.T) {
	t.Parallel()

	salsa := domain.Room{ID: "telegram:42/-1000000000021", Name: "Novo Salsa"}
	b := &linkBackend{target: domain.LinkTarget{Room: salsa, Join: true, About: "channel, 1204 members"}, joinTo: salsa.ID}
	m := withRooms(t, sized(t, starterNew(b, config.Display{})))
	next := followed(t, m, "https://t.me/novosalsa")

	if !next.confirm.active() || next.openRoom == salsa.ID {
		t.Fatalf("confirm %+v, open %q: want a question and no move", next.confirm, next.openRoom)
	}
	if prompt := next.confirmPrompt(); !strings.Contains(prompt, "Novo Salsa") || !strings.Contains(prompt, "1204 members") {
		t.Errorf("prompt = %q, want the chat's name and what it is", prompt)
	}
	next, cmd := next.resolveConfirm(true)
	joined, ok := drainFor[joinedMsg](cmd)
	if !ok || len(b.joined) != 1 || b.joined[0] != "https://t.me/novosalsa" {
		t.Fatalf("joined %v (%v), want the link joined", b.joined, ok)
	}
	next = update(t, next, joined)
	next = update(t, next, roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, salsa}})
	if next.openRoom != salsa.ID {
		t.Errorf("open room = %q, want the joined channel once listed", next.openRoom)
	}
}

// Fresh chats meet the room list in any order: a chat a link opened stays open while
// unlisted, gives way to the listing's copy once listed, and goes once left unlisted;
// a listed room is never lost and no room is held twice. The walk generates links to
// three new people, listings holding any of them, and moves elsewhere.
func TestFreshChatsHoldOverAnyListingSequence(t *testing.T) {
	t.Parallel()

	people := []domain.Room{
		{ID: "telegram:42/9", Name: "Ezra", IsDirect: true},
		{ID: "telegram:42/10", Name: "Fay", IsDirect: true},
		{ID: "telegram:42/11", Name: "Gil", IsDirect: true},
	}
	base := []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo", IsDirect: true}}
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 0x6c696e6b))
		b := &linkBackend{}
		m := sized(t, starterNew(b, config.Display{}))
		m = update(t, m, roomsMsg{rooms: base})
		listed := map[domain.RoomID]bool{}
		var log []string
		for step := range 30 {
			switch rng.IntN(3) {
			case 0: // a link to one of them
				p := people[rng.IntN(len(people))]
				b.target = domain.LinkTarget{Room: p}
				m = followed(t, m, fmt.Sprintf("tg://resolve?phone=1555%s", p.ID[len("telegram:42/"):]))
				log = append(log, "link "+p.Name)
				if m.openRoom != p.ID {
					t.Fatalf("seed %d step %d: open %q after a link to %s\n  %s", seed, step, m.openRoom, p.Name, strings.Join(log, "\n  "))
				}
			case 1: // a listing holding some of them, named as the network names them
				rooms := slices.Clone(base)
				// A chat open as a fresh one (not in the last listing) stays open; a listed
				// one the network drops is gone, as a chat deleted elsewhere is.
				open, freshOpen := m.openRoom, m.openRoom != "" && !listed[m.openRoom] && !slices.ContainsFunc(base, func(r domain.Room) bool { return r.ID == m.openRoom })
				listed = map[domain.RoomID]bool{}
				for _, p := range people {
					if rng.IntN(2) == 0 {
						p.Name += " (listed)"
						rooms = append(rooms, p)
						listed[p.ID] = true
					}
				}
				m = update(t, m, roomsMsg{rooms: rooms})
				log = append(log, fmt.Sprintf("listing %v", listed))
				if freshOpen && m.openRoom != open {
					t.Fatalf("seed %d step %d: a listing moved the open room from %q to %q\n  %s", seed, step, open, m.openRoom, strings.Join(log, "\n  "))
				}
				for _, p := range people {
					room, held := m.rooms.byID(p.ID)
					switch {
					case listed[p.ID] && (!held || room.Name != p.Name+" (listed)"):
						t.Fatalf("seed %d step %d: listed %s held as (%+v, %v), want the listing's copy\n  %s", seed, step, p.Name, room, held, strings.Join(log, "\n  "))
					case !listed[p.ID] && held && m.openRoom != p.ID:
						t.Fatalf("seed %d step %d: %s, left and unlisted, is still held\n  %s", seed, step, p.Name, strings.Join(log, "\n  "))
					}
				}
			case 2: // a move to a listed room
				to := base[rng.IntN(len(base))].ID
				m, _ = m.goToRoom(to)
				log = append(log, "go to "+string(to))
			}
			seen := map[domain.RoomID]bool{}
			for _, r := range m.rooms.all {
				if seen[r.ID] {
					t.Fatalf("seed %d step %d: %s held twice\n  %s", seed, step, r.ID, strings.Join(log, "\n  "))
				}
				seen[r.ID] = true
			}
			if _, ok := m.currentRoom(); m.openRoom != "" && !ok {
				t.Fatalf("seed %d step %d: the open room %q is not held\n  %s", seed, step, m.openRoom, strings.Join(log, "\n  "))
			}
		}
	}
}

// A tg://privatepost resolves by the channel's -100<id> native, so a shared private
// channel link opens its kith room.
func TestAHandedOverTelegramPrivateLinkOpensTheChannel(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "telegram:42/-1000000000021", Name: "Private channel"},
	}})

	next, _ := m.handleFollow(followMsg{uri: "tg://privatepost?channel=21&post=78"})
	if next.openRoom != "telegram:42/-1000000000021" {
		t.Errorf("open room = %q, want the private channel's room", next.openRoom)
	}
	if next.jump.to != "telegram:42/-1000000000021/78" {
		t.Errorf("jump.to = %q, want the post", next.jump.to)
	}
}

// A client started for a link waits for the room list: until it arrives, every room
// looks unjoined.
func TestAStartupLinkWaitsForTheRoomList(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel()).WithFollow("https://matrix.to/#/!b:x")

	// No rooms yet: the request stands instead of being spent on an empty list.
	if _, _, followed := m.followPendingLink(); followed {
		t.Fatal("the link was followed before the room list arrived")
	}
	if m.followAt == "" {
		t.Fatal("the pending link was cleared while it was still pending")
	}

	m = withRooms(t, m)
	if m.openRoom != mx+"!b:x" {
		t.Errorf("open room = %q, want !b:x — the rooms arrived and nothing followed", m.openRoom)
	}
	if m.followAt != "" {
		t.Error("the link stayed pending after it was followed")
	}
}

// raising is a sized model whose [clipboard] raise_command is command.
func raising(t *testing.T, command string) Model {
	t.Helper()
	cfg := config.Config{Clipboard: config.Clipboard{RaiseCommand: command}}
	return sized(t, newModel().WithConfigFile("", asStarted(cfg)))
}

// raiseMarker is a raise command that writes its parent's PID to a file, and that file.
func raiseMarker(t *testing.T) (command, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "raised")
	return `echo "$PPID" > '` + marker + `'`, marker
}

// A handed-over link runs raise_command in this process, so it sees this window's
// environment; $PPID is kith.
func TestAHandedOverLinkRaisesTheWindow(t *testing.T) {
	t.Parallel()

	command, marker := raiseMarker(t)
	m := withRooms(t, raising(t, command))
	next, cmd := asModel(m.Update(followMsg{uri: "https://matrix.to/#/!b:x"}))
	raised, ok := msgOf[raisedMsg](t, cmd)
	if !ok {
		t.Fatal("a handed-over link did not run raise_command")
	}
	if raised.err != nil {
		t.Fatalf("raise_command failed: %v", raised.err)
	}
	if next.openRoom != mx+"!b:x" {
		t.Errorf("open room = %q, want !b:x", next.openRoom)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("raise_command left no mark: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()); strings.TrimSpace(string(got)) != want {
		t.Errorf("$PPID = %q, want kith's own PID %s", strings.TrimSpace(string(got)), want)
	}
}

// The link a client was started with does not raise: that terminal was just opened.
func TestAStartupLinkDoesNotRaise(t *testing.T) {
	t.Parallel()

	command, marker := raiseMarker(t)
	m := raising(t, command).WithFollow("https://matrix.to/#/!b:x")
	next, cmd := asModel(m.Update(roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"},
		{ID: mx + "!b:x", Name: "Bravo", IsDirect: true},
	}}))
	if next.openRoom != mx+"!b:x" {
		t.Fatalf("open room = %q, want !b:x — the startup link was not followed", next.openRoom)
	}
	if _, ok := msgOf[raisedMsg](t, cmd); ok {
		t.Error("the startup link ran raise_command")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the startup link ran raise_command")
	}
}

// With no raise_command, following a handed-over link runs nothing.
func TestNoRaiseCommandRunsNothing(t *testing.T) {
	t.Parallel()

	m := withRooms(t, raising(t, ""))
	_, cmd := asModel(m.Update(followMsg{uri: "https://matrix.to/#/!b:x"}))
	if _, ok := msgOf[raisedMsg](t, cmd); ok {
		t.Error("an empty raise_command ran")
	}
}

// A raise_command that fails says so; the link was still followed.
func TestAFailingRaiseCommandIsReported(t *testing.T) {
	t.Parallel()

	m := withRooms(t, raising(t, "exit 3"))
	next, cmd := asModel(m.Update(followMsg{uri: "https://matrix.to/#/!b:x"}))
	raised, ok := msgOf[raisedMsg](t, cmd)
	if !ok || raised.err == nil {
		t.Fatalf("raisedMsg = %+v, %v; want the failure", raised, ok)
	}
	next = update(t, next, raised)
	if next.openRoom != mx+"!b:x" {
		t.Errorf("open room = %q, want !b:x", next.openRoom)
	}
	if !strings.Contains(next.status(), "could not raise this window") {
		t.Errorf("status = %q, want it to say the window was not raised", next.status())
	}
}

// A WhatsApp link to a number kith has a chat with opens it without asking WhatsApp;
// another number is looked up and opened fresh, with the suggested message.
func TestAHandedOverWhatsAppLinkOpensTheNumbersChat(t *testing.T) {
	t.Parallel()

	known := domain.Room{ID: "whatsapp:44000000001/15550100009@s.whatsapp.net", Name: "Dana", IsDirect: true}
	fresh := domain.Room{ID: "whatsapp:44000000001/15550100010@s.whatsapp.net", Name: "+15550100010", IsDirect: true}
	b := &linkBackend{target: domain.LinkTarget{Room: fresh}}
	m := sized(t, starterNew(b, config.Display{}))
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, known}})

	next := followed(t, m, "whatsapp://send?phone=15550100009&text=hi")
	if next.openRoom != known.ID || len(b.resolved) != 0 {
		t.Errorf("open room = %q, asked %v: want Dana's chat, found in the list", next.openRoom, b.resolved)
	}
	if next.compose.input != "hi" {
		t.Errorf("composer = %q, want the suggested message", next.compose.input)
	}

	next = followed(t, next, "https://wa.me/15550100010?text=hello")
	if len(b.resolved) != 1 || next.openRoom != fresh.ID {
		t.Fatalf("open room = %q, asked %v: want the number looked up and its chat opened", next.openRoom, b.resolved)
	}
	if _, ok := next.currentRoom(); !ok || next.compose.input != "hello" {
		t.Errorf("composer = %q (room held: %v), want a fresh chat holding the suggested message", next.compose.input, ok)
	}
}

// salsaRooms is a room list with a Telegram channel and a Matrix room in it.
var salsaRooms = roomsMsg{rooms: []domain.Room{
	{ID: "telegram:42/-1000000000021", Name: "Novo Salsa", Handle: "novosalsa"},
	{ID: mx + "!b:x", Name: "Bravo"},
}}

// gx follows a Telegram or WhatsApp chat link as it follows a Matrix one, and asks
// which when the message holds several, the web links first.
func TestFollowingTakesTelegramAndWhatsAppLinks(t *testing.T) {
	t.Parallel()

	m := update(t, sized(t, newModel()), salsaRooms)
	m = m.setMessages([]domain.Message{linkMsg("salsa tonight: https://t.me/novosalsa.", "")})
	m.timeline.selected = "$1"
	next, _ := m.takeContext(actOpenURL)
	if next.openRoom != "telegram:42/-1000000000021" {
		t.Errorf("open room = %q, want the channel the t.me link names", next.openRoom)
	}

	m = m.setMessages([]domain.Message{linkMsg("https://matrix.to/#/!b:x or wa.me/15550100009",
		`<a href="https://t.me/novosalsa">the channel</a>`)})
	next, _ = m.takeContext(actOpenURL)
	var rows []string
	for _, item := range next.picker.items {
		rows = append(rows, item.value)
	}
	want := []string{"https://matrix.to/#/!b:x", "https://t.me/novosalsa", "wa.me/15550100009"}
	if next.picker.then != actOpenURL || !slices.Equal(rows, want) {
		t.Errorf("chooser = %q, want %q", rows, want)
	}
}

// gx and gX follow a link kith can open on a network the room list is on, and hand
// every other link to the browser.
func TestOpeningALinkKithFollowsStaysInKith(t *testing.T) {
	t.Parallel()

	for _, act := range []action{actOpenURL, actOpenURLFocus} {
		m := update(t, sized(t, newModel()), salsaRooms)
		for link, want := range map[string]domain.RoomID{
			"https://t.me/novosalsa":    "telegram:42/-1000000000021",
			"https://matrix.to/#/!b:x":  mx + "!b:x",
			"https://wa.me/15550100009": "", // no WhatsApp chat listed: the browser's
			"https://example.org/docs":  "",
		} {
			m = m.setMessages([]domain.Message{linkMsg("see "+link, "")})
			m.timeline.selected = "$1"
			m.openRoom = ""
			next, _ := m.takeContext(act)
			if next.openRoom != want {
				t.Errorf("%v on %s: open room = %q, want %q", act, link, next.openRoom, want)
			}
			if browsed := strings.HasPrefix(next.status(), "opening "); browsed != (want == "") {
				t.Errorf("%v on %s: status %q, want the browser only for a link kith does not follow", act, link, next.status())
			}
		}
	}
}

// A handed-over Signal link is always looked up (no chat stores a number or username
// link) and opens what Signal found: a chat already listed, or a fresh one.
func TestAHandedOverSignalLinkOpensWhatSignalFound(t *testing.T) {
	t.Parallel()

	known := domain.Room{ID: "signal:11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222", Name: "Dana", IsDirect: true}
	b := &linkBackend{target: domain.LinkTarget{Room: known}}
	m := sized(t, starterNew(b, config.Display{}))
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, known}})

	next := followed(t, m, "sgnl://signal.me/#p/+15550100002")
	if len(b.resolved) != 1 || next.openRoom != known.ID || b.on[0] != linkAccounts[2].On {
		t.Errorf("open room = %q, asked %v: want the number looked up and Dana's chat opened", next.openRoom, b.resolved)
	}

	fresh := domain.Room{ID: "signal:11111111-1111-4111-8111-111111111111/33333333-3333-4333-8333-333333333333", Name: "ofer.42", IsDirect: true}
	b.target = domain.LinkTarget{Room: fresh}
	next = followed(t, next, "https://signal.me/#p/+15550100009")
	if next.openRoom != fresh.ID {
		t.Fatalf("open room = %q, want the fresh chat Signal found", next.openRoom)
	}
	if room, ok := next.currentRoom(); !ok || room.Name != "ofer.42" {
		t.Errorf("current room = (%+v, %v), want a fresh chat to write in", room, ok)
	}
}
