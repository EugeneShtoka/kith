package domain

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// shuffled is rows in an order of r's choosing.
func shuffled[T any](r *rand.Rand, rows []T) []T {
	out := slices.Clone(rows)
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// A person is every identifier the sources link, through chains and cycles, and the
// number a WhatsApp ID is; their name is the best any source gives any of them, the
// same whatever order the rows come in. A name that is only a number names nobody,
// and a link to oneself joins nothing.
func TestAPersonIsEveryLinkedIdentifierInAnyOrder(t *testing.T) {
	t.Parallel()
	const (
		tg      = "telegram:7"
		slackID = "slack:T1/U7"
		wa      = "whatsapp:15550100001@s.whatsapp.net"
		other   = "telegram:8"
	)
	dana := PhoneID("15550100001")
	names := []PersonName{
		{Source: "telegram:1", ID: tg, Name: "Dana T", Rank: RankChosen},
		{Source: "slack:T1", ID: slackID, Name: "dana.levi", Rank: RankChosen},
		{Source: "whatsapp:2", ID: dana, Name: "Dana Levi", Rank: RankSaved},
		{Source: "whatsapp:3", ID: dana, Name: "Dana from work", Rank: RankSaved}, // a tie: decided by source
		{Source: "bridge:x", ID: wa, Name: "+15550100001", Rank: RankSaved},       // a number names nobody
		{Source: "telegram:1", ID: other, Name: "Eli", Rank: RankChosen},
	}
	links := []PersonLink{
		{Source: "telegram:1", ID: tg, Other: dana},      // Telegram knows her number
		{Source: "slack:T1", ID: slackID, Other: tg},     // a chain to Slack…
		{Source: "slack:T1", ID: dana, Other: slackID},   // …closed into a cycle
		{Source: "telegram:1", ID: other, Other: other},  // to oneself: nothing
		{Source: "telegram:1", ID: "", Other: "missing"}, // half a link: nothing
	}
	r := rand.New(rand.NewPCG(7, 9))
	var first Directory
	for i := range 50 {
		d := NewDirectory(shuffled(r, names), shuffled(r, links))
		if i == 0 {
			first = d
		} else if !d.Equal(first) {
			t.Fatal("the same rows in another order built another directory")
		}
		for _, id := range []string{tg, slackID, wa, dana} {
			if name, rank, ok := d.Name(id); !ok || name != "Dana Levi" || rank != RankSaved {
				t.Fatalf("order %d: %s is %q (%v, %v), want Dana Levi, saved", i, id, name, rank, ok)
			}
		}
		if got := d.Identifiers(wa); !slices.Equal(got, []string{slackID, dana, tg, wa}) {
			t.Fatalf("order %d: Dana's identifiers = %v", i, got)
		}
		if name, _, _ := d.Name(other); name != "Eli" || len(d.Identifiers(other)) != 1 {
			t.Fatalf("order %d: Eli = %q with %v, want alone", i, name, d.Identifiers(other))
		}
		if name, ok := d.Named("+1 555 010 0001"); !ok || name != "Dana Levi" {
			t.Fatalf("order %d: her number written out = %q, %v", i, name, ok)
		}
	}
	if _, _, ok := first.Name("telegram:404"); ok {
		t.Error("a stranger has a name")
	}
}

// People name a person by your alias on any of their identifiers, then a name you
// saved, then a name given now, then the name they chose, then their number.
func TestPeopleNameByTheBestTheyKnow(t *testing.T) {
	t.Parallel()
	dir := NewDirectory([]PersonName{
		{Source: "whatsapp:2", ID: PhoneID("15550100001"), Name: "Dana Levi", Rank: RankSaved},
		{Source: "telegram:1", ID: "telegram:8", Name: "Eli's profile", Rank: RankChosen},
	}, []PersonLink{
		{Source: "telegram:1", ID: "telegram:7", Other: PhoneID("15550100001")},
		{Source: "telegram:1", ID: "telegram:9", Other: PhoneID("15550100009")},
	})
	aliases := map[string]string{PhoneID("15550100009"): "Fay (mine)"}
	p := People{Alias: func(id string) string { return aliases[id] }, Dir: dir}
	for _, tc := range []struct {
		user  string
		known []string
		want  string
	}{
		{"telegram:7", []string{"Dana T"}, "Dana Levi"},           // saved beats given now
		{"telegram:8", []string{"Eli"}, "Eli"},                    // given now beats chosen
		{"telegram:8", nil, "Eli's profile"},                      // chosen beats the ID
		{"telegram:9", []string{"Fay Telegram"}, "Fay (mine)"},    // an alias on a linked number
		{"telegram:10", []string{"+15550100010"}, "+15550100010"}, // a number, written as one
	} {
		if got := p.Name(tc.user, tc.known...); got != tc.want {
			t.Errorf("Name(%s, %q) = %q, want %q", tc.user, tc.known, got, tc.want)
		}
	}
}

// A bridge is known by its bot, else by the protocol its bridge state names; neither
// known is no network.
func TestABridgeIsKnownByItsBotOrProtocol(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		bot, protocol string
		want          Protocol
	}{
		{"@whatsappbot_bg:x", "", ProtocolWhatsApp},
		{"@telegrambot:x", "telegram", ProtocolTelegram},
		{"@somebot:x", "telegram", ProtocolTelegram},
		{"", "slackgo", ProtocolSlack},
		{"", "gmessages", ProtocolGMessages},
		{"@alice:x", "", ""},
		{"", "carrier-pigeon", ""},
	} {
		if got := BridgedBy(tc.bot, tc.protocol); got != tc.want {
			t.Errorf("BridgedBy(%q, %q) = %q, want %q", tc.bot, tc.protocol, got, tc.want)
		}
	}
}

// A room's network is its own once read, over its spaces' bridge; a room not read is
// its first bridged space's, else its ID's.
func TestARoomsOwnNetworkComesFirst(t *testing.T) {
	t.Parallel()
	slack := []Space{{ID: "!s:x", Name: "Workspace", Bridge: ProtocolSlack}}
	for _, tc := range []struct {
		room    Room
		holders []Space
		want    Protocol
	}{
		{Room{ID: "!r:x", Network: ProtocolTelegram}, slack, ProtocolTelegram},
		{Room{ID: "!r:x", Network: ProtocolMatrix}, slack, ProtocolMatrix},
		{Room{ID: "!r:x"}, slack, ProtocolSlack},
		{Room{ID: "!r:x"}, nil, ProtocolMatrix},
		{Room{ID: "telegram:1/7"}, nil, ProtocolTelegram},
	} {
		if got := (Places{}).Facts(tc.room, tc.holders).Protocol; got != tc.want {
			t.Errorf("%+v in %v: network %q, want %q", tc.room, tc.holders, got, tc.want)
		}
	}
}
