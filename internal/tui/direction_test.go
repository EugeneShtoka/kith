package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// directed is a model in !a:x under [display.direction] dir, its page loaded.
func directed(t *testing.T, dir config.Direction, msgs []domain.Message) Model {
	t.Helper()
	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, config.Display{Direction: dir})))
	return loadPage(t, m, msgs)
}

// said is n messages from Dana, each body as given.
func said(bodies ...string) []domain.Message {
	msgs := make([]domain.Message, len(bodies))
	for i, body := range bodies {
		msgs[i] = domain.Message{ID: domain.EventID("$" + string(rune('a'+i))), RoomID: "!a:x",
			Sender: "@dana:x", Body: body, Timestamp: at(1000 + 60*i)}
	}
	return msgs
}

var hebrew = said("שלום לכולם", "מה נשמע היום", "https://x.com/a/status/1\nהחבר היה הראשון לדחות", "ok")

// rowsOf is message i's rows at width 80, as the timeline draws them.
func rowsOf(m Model, msgs []domain.Message, i int) []string {
	return m.messageRows(msgs[i], 80, m.nameColWidth(), m.senderColorMap(), false, "")
}

// A room that reads right to left is mirrored: each first row ends in the name and
// the time at the right edge. Only those move: each message keeps its own direction,
// right-to-left words against the name, left-to-right ones from the left edge.
func TestARightToLeftRoomIsMirrored(t *testing.T) {
	t.Parallel()
	msgs := append(slices.Clone(hebrew), said("", "", "", "", "a word in English")[4])
	m := directed(t, config.Direction{RTL: []string{"!a:x"}}, msgs)
	if !m.mirrored() {
		t.Fatal("a room set rtl is not mirrored")
	}
	for i := range msgs {
		rows := rowsOf(m, msgs, i)
		first := ansi.Strip(rows[0])
		if !strings.HasSuffix(first, " dana "+domain.Clock{}.Time(msgs[i].Timestamp)) {
			t.Errorf("message %d: first row %q does not end in the name and the time", i, first)
		}
		if w := ansi.StringWidth(rows[0]); w != 80 {
			t.Errorf("message %d: first row is %d wide, want the full 80", i, w)
		}
		body := strings.TrimSuffix(first, " dana "+domain.Clock{}.Time(msgs[i].Timestamp))
		rtl := paragraphDir(msgs[i].Body) == bidi.RightToLeft
		if against := strings.TrimRight(body, " ") == body; rtl != against {
			t.Errorf("message %d (right to left %v): the text %q, want it against the name only when right to left", i, rtl, body)
		}
		if !rtl && strings.TrimLeft(body, " ") != body {
			t.Errorf("message %d: left-to-right text %q does not start at the left edge", i, body)
		}
	}

	ltr := directed(t, config.Direction{}, msgs)
	if first := ansi.Strip(rowsOf(ltr, msgs, 0)[0]); !strings.HasPrefix(first, domain.Clock{}.Time(msgs[0].Timestamp)+" dana") {
		t.Errorf("left to right: first row %q does not lead with the time and the name", first)
	}
}

// What trails the words in reading order (edited, the star) leads them on screen in a
// mirrored room; a reply's quote and the reactions sit on the right as well.
func TestAMirroredRoomMirrorsWhatHangsOnAMessage(t *testing.T) {
	t.Parallel()
	// The reply answers a message not directly above it, so it shows its quote.
	msgs := said("שלום לכולם", "בוקר טוב", "מה נשמע")
	msgs[2].Edited, msgs[2].ReplyTo = true, msgs[0].ID
	m := directed(t, config.Direction{RTL: []string{"!a:x"}}, msgs)
	m.timeline.reactions = map[domain.EventID][]domain.Reaction{
		msgs[2].ID: {{ID: "$r1", Target: msgs[2].ID, Sender: "@sam:x", Key: "👍"}},
	}

	w := m.walkOver(m.derivedFor())
	rows := m.renderRows(w, 2, false)
	plain := make([]string, len(rows))
	for i := range rows {
		plain[i] = strings.TrimRight(ansi.Strip(rows[i]), " ")
	}
	bodyEdge := w.width - m.bodyColumn(w.nameW)
	if !strings.Contains(plain[0], ": dana ↩") && !strings.Contains(plain[0], ":dana ↩") {
		t.Errorf("the quote %q is not read from the right (marker, name, quote)", plain[0])
	}
	body := plain[1]
	if !strings.HasPrefix(strings.TrimLeft(body, " "), editedMarker) {
		t.Errorf("the edited mark does not lead the mirrored words: %q", body)
	}
	// An English message in the same room keeps its mark after its words.
	english := said("", "", "a word in English")[2]
	english.Edited = true
	if last := ansi.Strip(rowsOf(m, []domain.Message{english}, 0)[0]); !strings.HasPrefix(last, "a word in English "+editedMarker) {
		t.Errorf("an English message's edited mark does not follow its words: %q", last)
	}
	chips := plain[len(plain)-1]
	if !strings.Contains(chips, "👍 1") || ansi.StringWidth(chips) != bodyEdge {
		t.Errorf("the reactions %q do not end at the body's right edge (%d)", chips, bodyEdge)
	}
}

// auto guesses once per opening and keeps it while you read: an English burst does
// not flip the room under you. Off, a Hebrew room reads left to right; set by hand,
// the hand wins over the guess.
func TestTheAutoGuessHoldsForTheOpening(t *testing.T) {
	t.Parallel()
	m := directed(t, config.Direction{Auto: true}, hebrew)
	if !m.mirrored() {
		t.Fatal("auto: a Hebrew room is not mirrored")
	}
	english := append(slices.Clone(hebrew), said("one", "two", "three", "four", "five", "six", "seven")...)
	for i := range english {
		english[i].ID = domain.EventID("$e" + string(rune('a'+i)))
	}
	if m = loadPage(t, m, english); !m.mirrored() {
		t.Error("auto: the room flipped while it was open")
	}

	if off := directed(t, config.Direction{}, hebrew); off.mirrored() {
		t.Error("auto off: a Hebrew room is mirrored")
	}
	if hand := directed(t, config.Direction{Auto: true, LTR: []string{"!a:x"}}, hebrew); hand.mirrored() {
		t.Error("a room set ltr by hand is mirrored by the guess")
	}
	if none := directed(t, config.Direction{Auto: true}, said("👍")); none.mirrored() || none.timeline.layout.settled {
		t.Error("auto: nothing to read decided the room anyway")
	}
}

// D cycles the room's own entry, right to left, left to right, back to the config's
// say, and the timeline is redrawn at once, not from cached rows.
func TestCyclingTheDirectionRedrawsTheTimeline(t *testing.T) {
	t.Parallel()
	m := directed(t, config.Direction{}, hebrew)
	window := func(m Model) string {
		rows, _ := m.layoutWindow(0, m.msgAreaRows())
		return strings.Join(rows, "\n")
	}
	before := window(m)
	if !strings.Contains(ansi.Strip(before), "dana") {
		t.Fatalf("the window draws no message:\n%s", ansi.Strip(before))
	}
	for _, want := range []struct {
		rtl, ltr bool
		mirrored bool
	}{{true, false, true}, {false, true, false}, {false, false, false}} {
		m, _ = m.cycleDirection()
		m = m.primeTimeline()
		d := m.prefs.display.Direction
		if slices.Contains(d.RTL, "!a:x") != want.rtl || slices.Contains(d.LTR, "!a:x") != want.ltr {
			t.Fatalf("after a cycle: rtl %v ltr %v, want %v %v", d.RTL, d.LTR, want.rtl, want.ltr)
		}
		if m.mirrored() != want.mirrored {
			t.Fatalf("after a cycle: mirrored %v, want %v", m.mirrored(), want.mirrored)
		}
		drawn := window(m)
		if want.mirrored && drawn == before {
			t.Error("mirrored, but the timeline is drawn as before")
		}
		if !want.mirrored && drawn != before {
			t.Error("back to left to right, but the timeline is not drawn as before")
		}
	}
}

// fullLine is words of letter filling exactly width cells.
func fullLine(letter string, width int) string {
	var words []string
	for n := 0; n < width; {
		size := min(4, width-n)
		if rest := width - n - size; rest == 1 {
			size++ // no room for a space and one more letter
		}
		words = append(words, strings.Repeat(letter, size))
		n += size + 1
	}
	return strings.Join(words, " ")
}

// A mark (edited, the star) on a last row already full goes on a row of its own: the
// body never runs into the name column, mirrored or not.
func TestAMarkNeverPushesARowPastItsColumn(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		dir    config.Direction
		letter string
	}{
		{"mirrored, Hebrew", config.Direction{RTL: []string{"!a:x"}}, "ש"},
		{"mirrored, English", config.Direction{RTL: []string{"!a:x"}}, "w"},
		{"left to right, English", config.Direction{}, "w"},
		{"left to right, Hebrew", config.Direction{}, "ש"},
	} {
		m := directed(t, c.dir, said("hi"))
		bodyW := 80 - m.bodyColumn(m.nameColWidth())
		msg := said(fullLine(c.letter, bodyW))[0]
		msg.Edited = true
		rows := m.messageRows(msg, 80, m.nameColWidth(), m.senderColorMap(), false, "")
		if len(rows) != 2 {
			t.Errorf("%s: %d rows, want the full line and the mark's own", c.name, len(rows))
		}
		for i, row := range rows {
			if w := ansi.StringWidth(row); w > 80 {
				t.Errorf("%s: row %d is %d wide, past the 80 of the timeline: %q", c.name, i, w, ansi.Strip(row))
			}
		}
		if last := ansi.Strip(rows[len(rows)-1]); !strings.Contains(last, editedMarker) {
			t.Errorf("%s: the mark is not on the last row: %q", c.name, last)
		}
		if m.mirrored() {
			if w := ansi.StringWidth(rows[1]); w != bodyW {
				t.Errorf("%s: the mark's row is %d wide, want it flushed to the body's edge at %d", c.name, w, bodyW)
			}
		}
	}
}

// What has no words of its own reads as the room does: kith's text in place of the
// sender's (a deletion, a bare file chip), a link alone, emoji alone. In a mirrored room
// it sits against the name. A caption, or words beside a link, keep their direction.
func TestAPlaceholderFollowsTheRoom(t *testing.T) {
	t.Parallel()
	msgs := said("gone", "report.pdf", "a caption in English", // a bare file's body is its name
		"https://www.linkedin.com/posts/someone-123", "🎂❤️ 100%", "see https://x.com/a",
		"↷ Forwarded\n\n", "Sent an album with 2 images:", "↷ Forwarded\n\nFYI, the deck")
	msgs[0].Redacted = true
	msgs[1].Media = &domain.Media{Type: domain.MediaFile, Name: "report.pdf"}
	msgs[2].Media = &domain.Media{Type: domain.MediaImage, Name: "cat.jpg"}
	m := directed(t, config.Direction{RTL: []string{"!a:x"}}, msgs)
	// A link alone and emoji alone have no language: the room's. Words beside a link do.
	// A bridge's header alone is not the sender's words either; words after it are.
	for i, against := range []bool{true, true, false, true, true, false, true, true, false} {
		first := ansi.Strip(rowsOf(m, msgs, i)[0])
		body := strings.TrimSuffix(first, " dana "+domain.Clock{}.Time(msgs[i].Timestamp))
		if got := strings.TrimRight(body, " ") == body; got != against {
			t.Errorf("message %d: %q, want it against the name %v", i, body, against)
		}
	}
	// Left to right, a placeholder reads from the left as ever.
	ltr := directed(t, config.Direction{}, msgs)
	if first := ansi.Strip(rowsOf(ltr, msgs, 0)[0]); !strings.Contains(first, "dana "+redactedBody) && !strings.Contains(first, "dana (deleted") {
		t.Errorf("left to right: %q, want the placeholder after the name", first)
	}
}
