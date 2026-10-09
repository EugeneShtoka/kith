package domain

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/richtext"
)

// A conversation exports as Markdown: a heading per day; each message its time and
// sender named as kith names them, the message it replies to quoted, its words with
// their formatting and the people they mention named, its attachment and reactions;
// edits and deletions said; a thread's replies under its first message; and words
// that only look like Markdown left as words.
func TestAConversationExportsAsMarkdown(t *testing.T) {
	t.Parallel()
	day1 := time.Date(2026, 3, 5, 21, 30, 0, 0, time.Local)
	day2 := time.Date(2026, 3, 6, 9, 5, 0, 0, time.Local)
	msgs := []Message{
		{ID: "$1", Sender: "@maya:x", SenderName: "maya", Body: "dinner at 8*?", Timestamp: day1},
		{ID: "$2", Sender: "@eli:x", SenderName: "Eli", Body: "@100000000000005 yes", ReplyTo: "$1", Timestamp: day1.Add(time.Minute), Edited: true,
			Mentions: []Mention{{UserID: "whatsapp:15550100001@s.whatsapp.net", Name: "@100000000000005"}}},
		{ID: "$3", Sender: "@maya:x", SenderName: "maya", Body: "oops", Timestamp: day1.Add(2 * time.Minute), Redacted: true},
		{ID: "$4", Sender: "@eli:x", SenderName: "Eli", Body: "the plan", Timestamp: day2,
			Format: richtext.FromMarkup("the <b>plan</b>"), Media: &Media{Type: MediaFile, Name: "plan.pdf", Size: 2048}},
		{ID: "$5", Sender: "@maya:x", SenderName: "maya", Body: "looks good\nship it", ThreadRoot: "$4", Timestamp: day2.Add(time.Minute)},
		{ID: "$6", Sender: "@eli:x", SenderName: "Eli", Body: "done", ThreadRoot: "$4", ReplyTo: "$0", Timestamp: day2.Add(2 * time.Minute)},
	}
	reactions := []Reaction{
		{ID: "$r1", Target: "$4", Sender: "@maya:x", Key: "👍"},
		{ID: "$r2", Target: "$4", Sender: "@dana:x", Key: "👍"},
		{ID: "$r3", Target: "$4", Sender: "@dana:x", Key: "🎉"},
	}
	people := People{
		Alias: func(id string) string { return map[string]string{"@maya:x": "Maya Rosen"}[id] },
		Dir:   NewDirectory([]PersonName{{Source: "s", ID: PhoneID("15550100001"), Name: "Dana Levi", Rank: RankSaved}}, nil),
	}
	got := ExportMarkdown(ExportOf{Title: "Dinner [club]", Network: "Matrix", Span: "7d", At: day2.Add(time.Hour)},
		msgs, reactions, people.Name, Clock{})
	want := `# Dinner \[club\]

Matrix · exported 2026-03-06 10:05 · 6 messages · since 7d

## Thu, 05 Mar 2026

**21:30 · Maya Rosen**  
dinner at 8\*?

**21:31 · Eli** _(edited)_  
> ↳ **Maya Rosen**: dinner at 8\*?
@Dana Levi yes

**21:32 · Maya Rosen**  
_(deleted)_

## Fri, 06 Mar 2026

**09:05 · Eli**  
the **plan**
📎 file · _plan.pdf_ · 2 KB
👍 2 · 🎉 1
>
> **Thread · 2 replies**
>
> **09:06 · Maya Rosen** · 2026-03-06  
> looks good  
> ship it
>
> **09:07 · Eli** · 2026-03-06  
> > ↳ _a message not in this export_
> done
`
	if got != want {
		t.Errorf("export:\n%s\nwant:\n%s", got, want)
	}
}
