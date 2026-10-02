package domain

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAllowModelReadsBothLists(t *testing.T) {
	t.Parallel()

	const url = "https://example.invalid/v1/chat/completions"
	room := RoomFacts{
		ID: "!work:example.org", Name: "Work · deploys",
		Spaces: []string{"Work"}, Protocol: ProtocolSlack,
	}

	cases := []struct {
		name      string
		scope     ModelScope
		endpoint  string
		isCrypted bool
		want      string // "" means allowed
	}{
		{"no endpoint is the default and refuses", ModelScope{}, "", false, ModelRefusedOff},
		// Both lists empty: every room.
		{"neither list, so every room", ModelScope{}, url, false, ""},
		// An allow list answers alone.
		{"on the allow list", ModelScope{Only: []string{"!work:example.org"}}, url, false, ""},
		{"not on the allow list", ModelScope{Only: []string{"!other:example.org"}}, url, false, ModelRefusedRoom},
		// A displayed name needs the `room:` prefix now: a bare word that is not a room
		// ID declares no kind and is refused, which is what stops a mistyped entry from
		// being an invisible filter.
		{"named by the name it is shown under", ModelScope{Only: []string{"room:Work · deploys"}}, url, false, ""},
		{"named in another case, since a config is typed by hand", ModelScope{Only: []string{"room:work · DEPLOYS"}}, url, false, ""},
		{"a bare name is refused rather than guessed at", ModelScope{Only: []string{"Work · deploys"}}, url, false, ModelRefusedRoom},
		// A deny list only applies when there is no allow list.
		{"on the deny list", ModelScope{Except: []string{"!work:example.org"}}, url, false, ModelRefusedRoom},
		{"another room is denied, this one is not", ModelScope{Except: []string{"!other:example.org"}}, url, false, ""},
		{
			"an allow list wins over a deny list naming the same room",
			ModelScope{Only: []string{"!work:example.org"}, Except: []string{"!work:example.org"}},
			url, false, "",
		},
		// Blank entries are not a list.
		{"a list of blanks is no list", ModelScope{Only: []string{"", "  "}}, url, false, ""},
		// Encryption is answered before either list, and by its own switch.
		{"encrypted, and encrypted rooms are out", ModelScope{}, url, true, ModelRefusedEncrypted},
		{"encrypted, allowed by name, still out", ModelScope{Only: []string{"!work:example.org"}}, url, true, ModelRefusedEncrypted},
		{"encrypted, and encrypted rooms are in", ModelScope{Encrypted: true}, url, true, ""},
		{
			"encrypted rooms in, but this one is denied",
			ModelScope{Except: []string{"!work:example.org"}, Encrypted: true},
			url, true, ModelRefusedRoom,
		},
		// The entries that make a list writable: a space, a network, a kind of room.
		{"a space it is in", ModelScope{Only: []string{"space:Work"}}, url, false, ""},
		{"a space it is not in", ModelScope{Only: []string{"space:Home"}}, url, false, ModelRefusedRoom},
		{"the network behind it", ModelScope{Only: []string{"protocol:Slack"}}, url, false, ""},
		{"another network", ModelScope{Except: []string{"protocol:WhatsApp"}}, url, false, ""},
		{"that network, denied", ModelScope{Except: []string{"protocol:slack"}}, url, false, ModelRefusedRoom},
		{"it is not a direct message", ModelScope{Only: []string{"dm"}}, url, false, ModelRefusedRoom},
		{"it is a group", ModelScope{Only: []string{"group"}}, url, false, ""},
		{"direct messages kept out", ModelScope{Except: []string{"dm"}}, url, false, ""},
		// The qualified spelling of what a bare entry already meant.
		{"room: by ID", ModelScope{Only: []string{"room:!work:example.org"}}, url, false, ""},
		{"room: by name", ModelScope{Only: []string{"room:Work · deploys"}}, url, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := AllowModel(tc.scope, room, tc.endpoint, tc.isCrypted)
			if (tc.want == "") != got.Allowed {
				t.Fatalf("AllowModel() = %+v, want allowed=%v", got, tc.want == "")
			}
			if got.Refusal != tc.want {
				t.Fatalf("refusal = %q, want %q", got.Refusal, tc.want)
			}
		})
	}
}

// A Listed scope is least privilege — `[agent.read]`'s reading. The same cases above,
// unlisted, keep `[assist]`'s reading; this pins only what the switch changes.
func TestAllowModelListedScopeSharesOnlyWhatIsNamed(t *testing.T) {
	t.Parallel()

	const url = "configured"
	room := RoomFacts{ID: "!work:example.org", Name: "Standup", Spaces: []string{"Work"}}
	cases := []struct {
		name      string
		scope     ModelScope
		isCrypted bool
		want      string
	}{
		{"nothing listed shares nothing", ModelScope{Listed: true}, false, ModelRefusedNothingListed},
		{"an except alone shares nothing", ModelScope{Listed: true, Except: []string{"dm"}}, false, ModelRefusedNothingListed},
		{"blanks are not a list", ModelScope{Listed: true, Only: []string{" "}}, false, ModelRefusedNothingListed},
		{"nothing listed beats the encryption answer", ModelScope{Listed: true}, true, ModelRefusedNothingListed},
		{"listed by its space", ModelScope{Listed: true, Only: []string{"space:Work"}}, false, ""},
		{"not listed", ModelScope{Listed: true, Only: []string{"space:Home"}}, false, ModelRefusedRoom},
		// The one reading that differs once something is listed: except subtracts.
		{
			"except subtracts from what rooms names",
			ModelScope{Listed: true, Only: []string{"space:Work"}, Except: []string{"room:Standup"}},
			false, ModelRefusedRoom,
		},
		{"listed, but encrypted and those are out", ModelScope{Listed: true, Only: []string{"group"}}, true, ModelRefusedEncrypted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := AllowModel(tc.scope, room, url, tc.isCrypted)
			if got.Refusal != tc.want || got.Allowed != (tc.want == "") {
				t.Fatalf("AllowModel() = %+v, want refusal %q", got, tc.want)
			}
		})
	}
	if (ModelScope{Listed: true}).Shares() || !(ModelScope{}).Shares() {
		t.Error("Shares: a Listed empty scope must share nothing, an unlisted empty one everything")
	}
}

func TestModelContextIsRecentBoundedAndInOrder(t *testing.T) {
	t.Parallel()

	msgs := []Message{
		{SenderName: "Ada", Body: strings.Repeat("a", 40)},
		{SenderName: "Bo", Body: strings.Repeat("b", 40)},
		{SenderName: "Cy", Body: strings.Repeat("c", 40)},
	}

	// 40 characters is ~11 tokens by the estimate, so a budget of 25 takes the last two
	// and drops the oldest — what a model finishing a sentence needs is what was just
	// said.
	got := ModelContext(msgs, nil, 25)
	if len(got) != 2 {
		t.Fatalf("ModelContext() = %d lines, want 2", len(got))
	}
	if !strings.HasPrefix(got[0], "Bo: ") || !strings.HasPrefix(got[1], "Cy: ") {
		t.Fatalf("ModelContext() = %v, want it to read in the order it was said", got)
	}

	if got := ModelContext(msgs, nil, 0); got != nil {
		t.Errorf("ModelContext(no budget) = %v, want nothing", got)
	}
	// A deleted message is not context: the words are gone and quoting them elsewhere
	// would be the deletion undone by another feature.
	deleted := []Message{{SenderName: "Ada", Body: "secret", Redacted: true}, {SenderName: "Bo", Body: "hello"}}
	if got := ModelContext(deleted, nil, 100); len(got) != 1 || !strings.Contains(got[0], "hello") {
		t.Errorf("ModelContext(with a deletion) = %v, want only what still exists", got)
	}
	// One message larger than the whole budget yields nothing rather than being sent
	// anyway: the budget is a limit on what leaves the machine.
	huge := []Message{{SenderName: "Ada", Body: strings.Repeat("x", 4000)}}
	if got := ModelContext(huge, nil, 10); len(got) != 0 {
		t.Errorf("ModelContext(over budget) = %v, want nothing", got)
	}
}

// A direct message on WhatsApp, which is the shape most of an account's rooms take and
// therefore the one a list has to be able to describe without naming each of them.
func TestRoomFactsNamesKindsOfRoom(t *testing.T) {
	t.Parallel()

	dm := RoomFacts{
		ID: "!dana:example.org", Name: "Dana", Direct: true,
		Spaces: []string{"WhatsApp"}, Protocol: ProtocolWhatsApp,
	}
	for _, entry := range []string{
		"dm", "DM", " dm ", "group ", "space:WhatsApp", "protocol:WhatsApp",
		"!dana:example.org", "room:Dana", "room:!dana:example.org",
	} {
		want := entry != "group "
		if got := dm.Names(entry); got != want {
			t.Errorf("Names(%q) = %v, want %v", entry, got, want)
		}
	}
	// Refused: empty, a prefix with nothing after it, the wrong space or network — and
	// a bare word, whether or not it happens to name the room.
	for _, entry := range []string{
		"", "   ", "space:", "protocol:", "space:Work", "protocol:Slack",
		"Dan", "Dana", "WhatsApp",
	} {
		if dm.Names(entry) {
			t.Errorf("Names(%q) = true, want false", entry)
		}
	}

	// And the kind comes back with the answer, because a ranked rule needs to know
	// whether an entry named one room or a set of them.
	for entry, want := range map[string]EntryKind{
		"!dana:example.org": EntryRoom, "room:Dana": EntryRoom,
		"space:WhatsApp": EntryClass, "protocol:WhatsApp": EntryClass,
		"dm": EntryClass, "Dana": EntryInvalid,
	} {
		if got, _ := dm.Match(entry); got != want {
			t.Errorf("Match(%q) kind = %v, want %v", entry, got, want)
		}
	}
	// A room in no space and on no bridge answers the kind entries and nothing else.
	plain := RoomFacts{ID: "!plain:example.org", Protocol: ProtocolMatrix}
	if !plain.Names("group") || plain.Names("dm") || plain.Names("space:Work") {
		t.Error("a plain room answered the wrong entries")
	}
	if !plain.Names("protocol:Matrix") {
		t.Error("a native room is not on the Matrix protocol")
	}
	// An empty name never matches an empty entry by accident, which is the bug a list
	// of blank lines would otherwise cause.
	if plain.Names("") {
		t.Error("a blank entry matched")
	}
}

func TestParseSummarySpan(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		arg   string
		since time.Time
		count int
		bad   bool
	}{
		// Nothing asked for is the default and not an error: "what I missed".
		{arg: ""},
		{arg: "   "},
		// The search box's own vocabulary, so there is one way to say "since when".
		{arg: "2h", since: now.Add(-2 * time.Hour)},
		{arg: "48h", since: now.Add(-48 * time.Hour)},
		{arg: "7d", since: now.AddDate(0, 0, -7)},
		{arg: "2w", since: now.AddDate(0, 0, -14)},
		{arg: "today", since: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)},
		{arg: "yesterday", since: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)},
		{arg: "2026-09-18", since: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)},
		// A bare number is the other way people ask, and one the date vocabulary has no
		// spelling for.
		{arg: "50", count: 50},
		{arg: "1", count: 1},
		// Refused rather than defaulted: somebody who typed this meant something, and
		// summarizing the wrong span silently is worse than being told.
		{arg: "2hours", bad: true},
		{arg: "soon", bad: true},
		{arg: "0", bad: true},
		{arg: "-5", bad: true},
	}
	for _, tc := range cases {
		got, err := ParseSummarySpan(tc.arg, now)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseSummarySpan(%q) = %+v, want an error", tc.arg, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSummarySpan(%q): %v", tc.arg, err)
			continue
		}
		if !got.Since.Equal(tc.since) || got.Count != tc.count {
			t.Errorf("ParseSummarySpan(%q) = %v/%d, want %v/%d", tc.arg, got.Since, got.Count, tc.since, tc.count)
		}
		if given := got.Given(); given != (tc.arg != "" && strings.TrimSpace(tc.arg) != "") {
			t.Errorf("ParseSummarySpan(%q).Given() = %v", tc.arg, given)
		}
	}
}

func TestMessagesInHonorsWhatWasAsked(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	var msgs []Message
	for i := range 10 {
		msgs = append(msgs, Message{
			ID:        EventID(string(rune('a' + i))),
			Body:      string(rune('a' + i)),
			Timestamp: base.Add(time.Duration(i) * time.Hour),
		})
	}

	// Since: everything at or after the moment, including a message exactly on it.
	got := MessagesIn(msgs, base.Add(7*time.Hour), 0)
	if len(got) != 3 || got[0].Body != "h" {
		t.Fatalf("MessagesIn(since) = %v, want the last three", bodies(got))
	}
	// Count: the last n, whatever their age.
	if got := MessagesIn(msgs, time.Time{}, 4); len(got) != 4 || got[0].Body != "g" {
		t.Fatalf("MessagesIn(count) = %v, want the last four", bodies(got))
	}
	// Both: the span first, then the cap — "the last two of the last hour".
	if got := MessagesIn(msgs, base.Add(5*time.Hour), 2); len(got) != 2 || got[0].Body != "i" {
		t.Fatalf("MessagesIn(both) = %v, want the last two inside the span", bodies(got))
	}
	// A span that covers nothing returns nothing rather than widening itself: somebody
	// who asked for the last hour is owed that answer.
	if got := MessagesIn(msgs, base.Add(100*time.Hour), 0); len(got) != 0 {
		t.Fatalf("MessagesIn(empty span) = %v, want nothing", bodies(got))
	}
}

func bodies(msgs []Message) []string {
	out := make([]string, 0, len(msgs))
	for i := range msgs {
		out = append(out, msgs[i].Body)
	}
	return out
}

// The send list is the second question and it answers only its own: whether an
// assistant may post to this room without a person looking first.
func TestAllowSendNamesRoomsTheSameWayEveryOtherListDoes(t *testing.T) {
	t.Parallel()

	work := RoomFacts{
		ID: "!work:example.org", Name: "Standup",
		Spaces: []string{"Work"}, Protocol: ProtocolSlack,
	}
	dm := RoomFacts{ID: "!dm:example.org", Name: "Dana", Direct: true, Protocol: ProtocolWhatsApp}

	cases := []struct {
		name string
		send []string
		room RoomFacts
		want bool
	}{
		// The default, and the one worth stating first: nothing listed drafts
		// everywhere, which is what an account that has never thought about this gets.
		{"an empty list posts nowhere", nil, work, false},
		{"a list of blanks is still empty", []string{"", "  "}, work, false},
		{"by name", []string{"room:Standup"}, work, true},
		{"by ID", []string{"!work:example.org"}, work, true},
		{"by space", []string{"space:Work"}, work, true},
		{"by network", []string{"protocol:WhatsApp"}, dm, true},
		{"by kind", []string{"dm"}, dm, true},
		{"a kind that does not describe it", []string{"dm"}, work, false},
		{"one of several", []string{"space:Family", "room:Standup"}, work, true},
		// A bare word declares no kind and is refused by RoomFacts.Match rather than
		// guessed at — the same rule the other lists live under since #83.
		{"a bare word names nothing", []string{"Standup"}, work, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := AllowSend(tc.send, tc.room); got != tc.want {
				t.Errorf("AllowSend(%v) = %v, want %v", tc.send, got, tc.want)
			}
		})
	}
}

func TestNeedsPlaces(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		scope ModelScope
		want  bool
	}{
		{ModelScope{}, false},
		{ModelScope{Only: []string{"dm", "room:Standup", "!a:x"}}, false},
		{ModelScope{Only: []string{" Space:Work"}}, true},
		{ModelScope{Except: []string{"protocol:whatsapp"}}, true},
	} {
		if got := c.scope.NeedsPlaces(); got != c.want {
			t.Errorf("%+v.NeedsPlaces() = %v, want %v", c.scope, got, c.want)
		}
	}
}

// Every ID that is this person is quoted as "You": their WhatsApp account's lines as
// well as their Matrix account's.
func TestModelContextQuotesEveryAccountAsYou(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		{Sender: "@me:x", Body: "from Matrix"},
		{Sender: "whatsapp:359880000001@s.whatsapp.net", Body: "from WhatsApp"},
		{Sender: "@dana:x", SenderName: "Dana", Body: "hi"},
	}
	got := ModelContext(msgs, []string{"@me:x", "whatsapp:359880000001@s.whatsapp.net"}, 1000)
	want := []string{"You: from Matrix", "You: from WhatsApp", "Dana: hi"}
	if !slices.Equal(got, want) {
		t.Errorf("ModelContext = %q, want %q", got, want)
	}
}
