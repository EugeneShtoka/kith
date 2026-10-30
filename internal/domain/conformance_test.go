package domain

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/richtext"
)

// The conformance tables in testdata/ say what the desktop's ports of this package's
// rules must answer; this test holds the tables to the Go they port, so the two cannot
// drift apart unseen.

// confRow is a message as testdata/timeline.json writes it: the fields ordering and
// merging read, times in Unix milliseconds, an absent field its zero.
type confRow struct {
	ID             string `json:"id"`
	TS             int64  `json:"ts,omitempty"`
	Seq            int64  `json:"seq,omitempty"`
	Sender         string `json:"sender,omitempty"`
	SenderName     string `json:"senderName,omitempty"`
	Body           string `json:"body,omitempty"`
	Format         string `json:"format,omitempty"` // the formatting's text, no spans
	Edited         bool   `json:"edited,omitempty"`
	EditedAt       int64  `json:"editedAt,omitempty"`
	RevisionID     string `json:"revisionId,omitempty"`
	Reverted       bool   `json:"reverted,omitempty"`
	Redacted       bool   `json:"redacted,omitempty"`
	RedactedBy     string `json:"redactedBy,omitempty"`
	RedactedReason string `json:"redactedReason,omitempty"`
	RedactedAt     int64  `json:"redactedAt,omitempty"`
	ReplyTo        string `json:"replyTo,omitempty"`
	ThreadRoot     string `json:"threadRoot,omitempty"`
	Mentioned      bool   `json:"mentioned,omitempty"`
	Placeholder    bool   `json:"placeholder,omitempty"`
	Undecrypted    bool   `json:"undecrypted,omitempty"`
	Media          string `json:"media,omitempty"` // an image attachment's name
	Poll           string `json:"poll,omitempty"`  // the poll's question
}

func confTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func confMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (r confRow) message() Message {
	m := Message{
		ID: EventID(r.ID), Timestamp: confTime(r.TS), Seq: r.Seq, Sender: r.Sender, SenderName: r.SenderName,
		Body: r.Body, Format: richtext.Drawn(r.Format, nil), Edited: r.Edited, EditedAt: confTime(r.EditedAt),
		RevisionID: EventID(r.RevisionID), Reverted: r.Reverted, Redacted: r.Redacted, RedactedBy: r.RedactedBy,
		RedactedReason: r.RedactedReason, RedactedAt: confTime(r.RedactedAt), ReplyTo: EventID(r.ReplyTo),
		ThreadRoot: EventID(r.ThreadRoot), Mentioned: r.Mentioned, Placeholder: r.Placeholder,
		Undecrypted: r.Undecrypted,
	}
	if r.Media != "" {
		m.Media = &Media{Type: "image", Name: r.Media}
	}
	if r.Poll != "" {
		m.Poll = &Poll{Question: r.Poll}
	}
	return m
}

func confRowOf(m Message) confRow {
	r := confRow{
		ID: string(m.ID), TS: confMillis(m.Timestamp), Seq: m.Seq, Sender: m.Sender, SenderName: m.SenderName,
		Body: m.Body, Format: m.Format.Text(), Edited: m.Edited, EditedAt: confMillis(m.EditedAt),
		RevisionID: string(m.RevisionID), Reverted: m.Reverted, Redacted: m.Redacted, RedactedBy: m.RedactedBy,
		RedactedReason: m.RedactedReason, RedactedAt: confMillis(m.RedactedAt), ReplyTo: string(m.ReplyTo),
		ThreadRoot: string(m.ThreadRoot), Mentioned: m.Mentioned, Placeholder: m.Placeholder,
		Undecrypted: m.Undecrypted,
	}
	if m.Media != nil {
		r.Media = m.Media.Name
	}
	if m.Poll != nil {
		r.Poll = m.Poll.Question
	}
	return r
}

func messagesOf(rows []confRow) []Message {
	out := make([]Message, len(rows))
	for i := range rows {
		out[i] = rows[i].message()
	}
	return out
}

func readTable(t *testing.T, name string, into any) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// A page merges by MergeMessages; a stream frame replaces the row with its ID, as the
// TUI's handleIncoming does: the row shown taken out, the frame merged in.
func TestTheTimelineTableIsWhatGoOrdersAndMerges(t *testing.T) {
	t.Parallel()
	var table struct {
		Cases []struct {
			Name  string    `json:"name"`
			Kind  string    `json:"kind"`
			Shown []confRow `json:"shown"`
			Rows  []confRow `json:"rows"`
			Want  []confRow `json:"want"`
		} `json:"cases"`
	}
	readTable(t, "timeline.json", &table)
	if len(table.Cases) == 0 {
		t.Fatal("timeline.json has no cases")
	}
	for _, c := range table.Cases {
		shown, rows := messagesOf(c.Shown), messagesOf(c.Rows)
		var got []Message
		switch c.Kind {
		case "merge":
			got = MergeMessages(shown, rows)
		case "frame":
			if len(rows) != 1 {
				t.Fatalf("%s: a frame is one row", c.Name)
			}
			others := slices.DeleteFunc(shown, func(m Message) bool { return m.ID == rows[0].ID })
			got = MergeMessages(others, rows)
		default:
			t.Fatalf("%s: no kind %q", c.Name, c.Kind)
		}
		gotRows := make([]confRow, len(got))
		for i, m := range got {
			gotRows[i] = confRowOf(m)
		}
		if !reflect.DeepEqual(gotRows, c.Want) {
			t.Errorf("%s: got %+v, the table says %+v", c.Name, gotRows, c.Want)
		}
	}
}

// AggregateReactions with no one counted as me, as both timelines tally them.
func TestTheReactionsTableIsWhatGoTallies(t *testing.T) {
	t.Parallel()
	type tally struct {
		Key   string `json:"key"`
		Count int    `json:"count"`
	}
	var table struct {
		Cases []struct {
			Name      string `json:"name"`
			Reactions []struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			} `json:"reactions"`
			Want []tally `json:"want"`
		} `json:"cases"`
	}
	readTable(t, "reactions.json", &table)
	if len(table.Cases) == 0 {
		t.Fatal("reactions.json has no cases")
	}
	for _, c := range table.Cases {
		rs := make([]Reaction, len(c.Reactions))
		for i, r := range c.Reactions {
			rs[i] = Reaction{ID: EventID(r.ID), Key: r.Key, Target: "$a"}
		}
		got := []tally{}
		for _, x := range AggregateReactions(rs, "") {
			got = append(got, tally{Key: x.Key, Count: x.Count})
		}
		if !reflect.DeepEqual(got, c.Want) {
			t.Errorf("%s: got %v, the table says %v", c.Name, got, c.Want)
		}
	}
}

// ResolveRead with the room's homes unknown, as the desktop resolves it until it is told
// them: a receipt is announced only when every home the room might have says so. Each
// case lists the homes worth trying (one matching each home rule, and none).
func TestTheReadTableIsWhatGoResolves(t *testing.T) {
	t.Parallel()
	var table struct {
		Cases []struct {
			Name  string `json:"name"`
			Send  bool   `json:"send"`
			Rules []struct {
				Match  string `json:"match"`
				Sender string `json:"sender"`
				Send   *bool  `json:"send"`
				Delay  *int   `json:"delay"`
			} `json:"rules"`
			Room   string   `json:"room"`
			Sender string   `json:"sender"`
			Homes  []string `json:"homes"`
			Want   bool     `json:"want"`
		} `json:"cases"`
	}
	readTable(t, "read.json", &table)
	if len(table.Cases) == 0 {
		t.Fatal("read.json has no cases")
	}
	for _, c := range table.Cases {
		rules := make([]ReadRule, len(c.Rules))
		for i, r := range c.Rules {
			rules[i] = ReadRule{Match: r.Match, Sender: r.Sender, Send: r.Send}
			if r.Delay != nil {
				d := time.Duration(*r.Delay) * time.Second
				rules[i].Delay = &d
			}
		}
		homes := c.Homes
		if len(homes) == 0 {
			homes = []string{""}
		}
		announced := true
		for _, home := range homes {
			place := DownloadPlace{RoomID: c.Room, Space: home, Sender: c.Sender}
			announced = announced && ResolveRead(c.Send, -time.Second, rules, place).Send
		}
		if announced != c.Want {
			t.Errorf("%s: announced = %v over homes %q, the table says %v", c.Name, announced, homes, c.Want)
		}
	}
}

// ParseSearch over the table's queries, its times read in the zone now is in (here UTC;
// the desktop's, its own).
func TestTheSearchTableIsWhatGoParses(t *testing.T) {
	t.Parallel()
	var table struct {
		Now   string `json:"now"`
		Cases []struct {
			Input       string `json:"input"`
			Terms       string `json:"terms"`
			Sender      string `json:"sender"`
			SenderField string `json:"senderField"`
			Since       string `json:"since"`
			Until       string `json:"until"`
			Empty       *bool  `json:"empty"`
		} `json:"cases"`
	}
	readTable(t, "search.json", &table)
	if len(table.Cases) == 0 {
		t.Fatal("search.json has no cases")
	}
	const wall = "2006-01-02T15:04:05"
	now, err := time.ParseInLocation(wall, table.Now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	shown := func(when time.Time) string {
		if when.IsZero() {
			return ""
		}
		return when.Format(wall)
	}
	for _, c := range table.Cases {
		got := ParseSearch(c.Input, now)
		if got.Terms != c.Terms || got.Sender != c.Sender || got.SenderField != c.SenderField ||
			shown(got.Since) != c.Since || shown(got.Until) != c.Until {
			t.Errorf("%q: got terms %q, sender %q, field %q, since %q, until %q; the table says %+v",
				c.Input, got.Terms, got.Sender, got.SenderField, shown(got.Since), shown(got.Until), c)
		}
		if c.Empty != nil && got.Empty() != *c.Empty {
			t.Errorf("%q: empty = %v, the table says %v", c.Input, got.Empty(), *c.Empty)
		}
	}
}

// CollapseThreads and ThreadMessages over the table's rooms.
func TestTheThreadTableIsWhatGoGroups(t *testing.T) {
	t.Parallel()
	type summary struct {
		Root             string `json:"root"`
		Count            int    `json:"count"`
		Latest           string `json:"latest"`
		LatestSender     string `json:"latestSender,omitempty"`
		LatestSenderName string `json:"latestSenderName,omitempty"`
		Anchor           string `json:"anchor"`
		RootLoaded       bool   `json:"rootLoaded"`
	}
	var table struct {
		Cases []struct {
			Name     string              `json:"name"`
			Rows     []confRow           `json:"rows"`
			Main     []string            `json:"main"`
			Threads  []summary           `json:"threads"`
			ThreadOf map[string][]string `json:"threadOf"`
		} `json:"cases"`
	}
	readTable(t, "threads.json", &table)
	if len(table.Cases) == 0 {
		t.Fatal("threads.json has no cases")
	}
	ids := func(msgs []Message) []string {
		out := []string{}
		for i := range msgs {
			out = append(out, string(msgs[i].ID))
		}
		return out
	}
	for _, c := range table.Cases {
		msgs := messagesOf(c.Rows)
		main, threads := CollapseThreads(msgs)
		got := []summary{}
		for _, th := range threads {
			got = append(got, summary{
				Root: string(th.Root), Count: th.Count, Latest: string(th.Latest), LatestSender: th.LatestSender,
				LatestSenderName: th.LatestSenderName, Anchor: string(th.Anchor), RootLoaded: th.RootLoaded,
			})
		}
		if !slices.Equal(ids(main), c.Main) || !reflect.DeepEqual(got, c.Threads) {
			t.Errorf("%s: main %v, threads %+v; the table says %v, %+v", c.Name, ids(main), got, c.Main, c.Threads)
		}
		for root, want := range c.ThreadOf {
			if got := ids(ThreadMessages(msgs, EventID(root))); !slices.Equal(got, want) {
				t.Errorf("%s: thread %q holds %v, the table says %v", c.Name, root, got, want)
			}
		}
	}
}
