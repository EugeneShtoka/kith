package chatexport

import (
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	dana domain.RoomID = "whatsapp:me/dana"
	team domain.RoomID = "whatsapp:me/team"
)

var (
	t0      = time.Date(2021, 3, 4, 21, 0, 0, 0, time.UTC)
	danaW   = domain.Member{UserID: "whatsapp:4915550001", DisplayName: "Dana W."}
	lee     = domain.Member{UserID: "whatsapp:4915550002", DisplayName: "Lee Park"}
	leeToo  = domain.Member{UserID: "whatsapp:4915550003", DisplayName: "Lee Park"}
	me      = domain.Member{UserID: "whatsapp:4915550009", DisplayName: "Robin Gale"}
	longish = "see you at the station at eight"
)

func line(minutes int, sender, body string) Line {
	return Line{At: t0.Add(time.Duration(minutes) * time.Minute), Minute: true, Sender: sender, Body: body}
}

// The rooms offered are those that cached the export's messages, the most first, and
// those its file is named after; none else.
func TestRoomsAreThoseHoldingItOrNamedInTheFile(t *testing.T) {
	t.Parallel()
	e := Export{Title: "WhatsApp Chat with Dana Wolfe"}
	rooms := []domain.Room{
		{ID: "whatsapp:me/other", Name: "Book club"},
		{ID: dana, Members: []string{"Dana Wolfe"}},
		{ID: team, Name: "Team"},
		{ID: "whatsapp:me/wolfe", Name: "Wolfe"}, // the name's end, but a word of it
	}
	got := e.Rooms(rooms, map[domain.RoomID]int{team: 3, dana: 1})
	want := []domain.ExportRoom{{ID: team, Found: 3}, {ID: dana, Found: 1, Named: true}, {ID: "whatsapp:me/wolfe", Named: true}}
	if !slices.Equal(got, want) {
		t.Errorf("Rooms() = %+v, want %+v", got, want)
	}
}

// A name is who the room cached its messages from, at the same minute; else the one
// member called so; else no one, with the members who share a word offered.
func TestSendersAreWhoTheCacheSaysElseTheirName(t *testing.T) {
	t.Parallel()
	e := Export{Lines: []Line{
		line(0, "Mom", longish), line(1, "Mom", "ok"), line(2, "Lee Park", "hi"),
		line(3, "robin gale", "hey"), line(4, "Dana Wolfe", "yo"), line(5, "Mom", "and bring the umbrella please"),
	}}
	cached := []domain.Message{
		{Sender: danaW.UserID, SenderName: danaW.DisplayName, Body: longish, Timestamp: t0.Add(30 * time.Second)},
		// The same words a day later are another message, no vote.
		{Sender: lee.UserID, Body: longish, Timestamp: t0.Add(24 * time.Hour)},
	}
	got := e.Senders([]domain.Member{danaW, lee, leeToo, me}, cached)
	byName := map[string]domain.ExportSender{}
	var order []string
	for _, s := range got {
		byName[s.Name], order = s, append(order, s.Name)
	}
	if order[0] != "Mom" || byName["Mom"].Messages != 3 {
		t.Errorf("order %v, Mom %+v; want Mom first with 3", order, byName["Mom"])
	}
	for name, want := range map[string]string{"Mom": danaW.UserID, "robin gale": me.UserID, "Lee Park": "", "Dana Wolfe": ""} {
		if byName[name].Chosen != want {
			t.Errorf("%s chosen %q, want %q", name, byName[name].Chosen, want)
		}
	}
	if likely := byName["Lee Park"].Likely; len(likely) != 2 || likely[0] != lee || likely[1] != leeToo {
		t.Errorf("Lee Park likely %+v, want both Lee Parks", likely)
	}
	if likely := byName["Mom"].Likely; len(likely) != 1 || likely[0] != danaW {
		t.Errorf("Mom likely %+v, want Dana", likely)
	}
}

// Only what is older than the room's oldest cached message is added, a minute-written
// line only when its whole minute is; each is from whom it was chosen as, named as the
// member is; and the IDs are the same each time, apart for the same words twice.
func TestMessagesAddOnlyWhatIsOlderOnce(t *testing.T) {
	t.Parallel()
	e := Export{Lines: []Line{
		line(0, "Mom", "ok"), line(0, "Mom", "ok"), line(1, "robin gale", "hey"),
		line(2, "Mom", "this one is cached"),
	}}
	before := t0.Add(time.Minute + 30*time.Second) // the cache's oldest, mid-minute 1
	got := e.Messages(dana, before, map[string]string{"Mom": danaW.UserID}, []domain.Member{danaW})
	if len(got) != 2 || got[0].ID == got[1].ID {
		t.Fatalf("Messages() = %+v; want the two oks, apart", got)
	}
	if got[0].Sender != danaW.UserID || got[0].SenderName != danaW.DisplayName || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("first = %+v, want Dana's, named as she is, in order", got[0])
	}
	again := e.Messages(dana, before, nil, nil)
	if again[0].ID != got[0].ID || again[1].ID != got[1].ID || again[0].Sender != "" || again[0].SenderName != "Mom" {
		t.Errorf("again = %+v; want the same IDs, as written", again)
	}
	if all := e.Messages(dana, time.Time{}, nil, nil); len(all) != 4 || e.Adds(time.Time{}) != 4 || e.Adds(before) != 2 {
		t.Errorf("into an empty room: %d messages, Adds %d/%d; want 4, 4/2", len(all), e.Adds(time.Time{}), e.Adds(before))
	}
}
