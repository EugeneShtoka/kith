package tui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func formatted(body, html string) domain.Message {
	return domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@her:x", Body: body, HTML: html}
}

// A formatted message draws its rendered text, not the Markdown source.
func TestAFormattedMessageDrawsTheRenderedText(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	msg := formatted("**test** is not bold", "<strong>test</strong> is not bold")

	body, _, marks := m.messageBody(msg, nil)
	if want := "test is not bold"; body != want {
		t.Fatalf("drew %q, want %q — the asterisks are the source, not the message", body, want)
	}
	if len(marks) != 1 || body[marks[0].Start:marks[0].End] != "test" {
		t.Fatalf("marks = %+v, want one over \"test\"", marks)
	}
	if !marks[0].Bold {
		t.Error("the mark is not bold")
	}
}

// The escapes land on that word only, and stripping them leaves every character.
func TestTheEmphasisLandsOnTheRightWord(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	msg := formatted("**test** is not bold", "<strong>test</strong> is not bold")
	rows := m.messageRows(msg, 80, 12, nil, false, "")
	row := strings.Join(rows, "\n")

	if !strings.Contains(row, "\x1b[") {
		t.Fatalf("no styling in a formatted message:\n  %q", row)
	}
	if plain := stripStyles(row); !strings.Contains(plain, "test is not bold") {
		t.Errorf("the row lost its words:\n  %q", plain)
	}
	if !strings.Contains(row, "test\x1b[") {
		t.Errorf("the emphasis does not end where the word does:\n  %q", row)
	}
}

// Unformatted messages, formatting that draws no words, and the client's own text
// (redactions, emotes, attachments) are drawn from the plain body, unstyled.
func TestUnstyledBodies(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	for name, tc := range map[string]struct {
		msg  domain.Message
		want string
	}{
		"plain":      {domain.Message{ID: "$1", RoomID: "!a:x", Body: "just words"}, "just words"},
		"no words":   {formatted("the real words", "<strong> </strong>"), "the real words"},
		"redacted":   {domain.Message{ID: "$1", RoomID: "!a:x", Body: "gone", HTML: "<strong>gone</strong>", Redacted: true}, ""},
		"an emote":   {domain.Message{ID: "$2", RoomID: "!a:x", Body: "waves", HTML: "<strong>waves</strong>", Emote: true}, ""},
		"attachment": {domain.Message{ID: "$3", RoomID: "!a:x", Body: "pic.png", HTML: "<strong>pic.png</strong>", Media: &domain.Media{Type: domain.MediaImage, Name: "pic.png"}}, ""},
	} {
		body, _, marks := m.messageBody(tc.msg, nil)
		if marks != nil {
			t.Errorf("%s: marks = %+v, want none", name, marks)
		}
		if tc.want != "" && body != tc.want {
			t.Errorf("%s: drew %q, want %q", name, body, tc.want)
		}
	}
}

// A bold phrase broken across lines is bold on each.
func TestEmphasisSurvivesAWrap(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	body := "one two three four five six seven eight nine ten eleven twelve"
	msg := formatted(body, "<strong>"+body+"</strong>")
	rows := m.messageRows(msg, 40, 8, nil, false, "")

	styled := 0
	for _, row := range rows {
		if strings.Contains(row, "\x1b[") {
			styled++
		}
	}
	if len(rows) < 2 {
		t.Fatalf("the body did not wrap: %d row(s)", len(rows))
	}
	if styled != len(rows) {
		t.Errorf("%d of %d rows carry the emphasis, want all of them", styled, len(rows))
	}
}

// segmentStarts locates repeated lines in turn, and gives up on a line not in text.
func TestSegmentStartsFindsEachLineInTurn(t *testing.T) {
	t.Parallel()

	text := "hello world\nhello again\nhello world"
	segments := strings.Split(text, "\n")
	starts, ok := segmentStarts(text, segments)
	if !ok {
		t.Fatal("the lines could not be located in the text they came from")
	}
	for i, at := range starts {
		if got := text[at : at+len(segments[i])]; got != segments[i] {
			t.Errorf("line %d was located at %d, which holds %q not %q", i, at, got, segments[i])
		}
	}
	if starts[0] == starts[2] {
		t.Error("the repeated line was located in the same place twice")
	}
	if _, ok := segmentStarts("the text", []string{"not in it"}); ok {
		t.Error("a line that is not in the text was located anyway")
	}
}

// Every OSC 8 target must be printable ASCII: a terminal that rejects one prints it,
// overflowing the row the layout measured.
func TestAHyperlinkNeverCarriesWhatATerminalMightPrint(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m, _ = m.selectRoom(m.filteredRooms()[0])

	for _, body := range []string{
		// From a Slack bridge: `<url|label>` with an emoji in the label.
		"> **<https://us-east-2.console.aws.amazon.com/cloudwatch/home?region=us-east-2" +
			"#alarm:name=tipmaster-prd-cpu|✅️ CloudWatch Alarm | tipmaster-prd-cpu>**",
		"see https://example.org/фотографии for the pictures",
	} {
		rows := m.messageRows(domain.Message{
			ID: "$probe", RoomID: m.openRoom, Sender: "@bot:x", SenderName: "Bot",
			Body: body, Timestamp: time.Now(),
		}, 120, 14, map[string]color.Color{}, false, "")

		for i, row := range rows {
			for _, uri := range hyperlinkTargets(row) {
				if !domain.SafeHyperlink(uri) {
					t.Errorf("row %d carries an OSC 8 target a terminal may print: %q", i, uri)
				}
			}
		}
	}
}

// hyperlinkTargets is every OSC 8 URI in a rendered row.
func hyperlinkTargets(row string) []string {
	var out []string
	for rest := row; ; {
		open := strings.Index(rest, "\x1b]8;;")
		if open < 0 {
			return out
		}
		rest = rest[open+len("\x1b]8;;"):]
		end := strings.IndexAny(rest, "\a\x1b")
		if end < 0 {
			return out
		}
		if uri := rest[:end]; uri != "" {
			out = append(out, uri)
		}
		rest = rest[end:]
	}
}
