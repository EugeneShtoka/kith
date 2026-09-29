package domain_test

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestSortRooms(t *testing.T) {
	t.Parallel()

	rooms := []domain.Room{
		{ID: "!c:x", Name: "banana"},
		{ID: "!a:x", Name: "Apple"},
		{ID: "!b:x", Name: ""}, // display name "!b:x" sorts before named rooms
	}
	domain.SortRooms(rooms)

	want := []string{"!b:x", "Apple", "banana"}
	for i, w := range want {
		if got := rooms[i].DisplayName(); got != w {
			t.Errorf("rooms[%d] = %q, want %q", i, got, w)
		}
	}
}

func TestSortSpaces(t *testing.T) {
	t.Parallel()

	spaces := []domain.Space{
		{ID: "!c:x", Name: "Work"},
		{ID: "!a:x", Name: "friends"},
		{ID: "!b:x"}, // display name "!b:x" sorts first
	}
	domain.SortSpaces(spaces)

	want := []string{"!b:x", "friends", "Work"}
	for i, w := range want {
		if got := spaces[i].DisplayName(); got != w {
			t.Errorf("spaces[%d] = %q, want %q", i, got, w)
		}
	}
}

func TestFormatHeroes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		shown []string
		extra int
		want  string
	}{
		"none":        {nil, 0, ""},
		"one":         {[]string{"Alice"}, 0, "Alice"},
		"two":         {[]string{"Alice", "Bob"}, 0, "Alice and Bob"},
		"three":       {[]string{"Alice", "Bob", "Cy"}, 0, "Alice, Bob and Cy"},
		"one other":   {[]string{"Alice"}, 1, "Alice and 1 other"},
		"many others": {[]string{"Alice", "Bob"}, 3, "Alice, Bob and 3 others"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := domain.FormatHeroes(tc.shown, tc.extra); got != tc.want {
				t.Errorf("FormatHeroes(%v, %d) = %q, want %q", tc.shown, tc.extra, got, tc.want)
			}
		})
	}
}

func TestMergeMessages(t *testing.T) {
	t.Parallel()

	at := func(sec int) time.Time { return time.Unix(int64(sec), 0) }
	cached := []domain.Message{
		{ID: "$2", Body: "second", Timestamp: at(2)},
		{ID: "$1", Body: "first", Timestamp: at(1)},
	}
	live := []domain.Message{
		{ID: "$3", Body: "third", Timestamp: at(3)},
		{ID: "$2", Body: "second", Timestamp: at(2)}, // duplicate of cached
	}

	merged := domain.MergeMessages(cached, live)
	if len(merged) != 3 {
		t.Fatalf("merged len = %d, want 3 (deduped)", len(merged))
	}
	wantOrder := []string{"first", "second", "third"}
	for i, w := range wantOrder {
		if merged[i].Body != w {
			t.Errorf("merged[%d] = %q, want %q (chronological)", i, merged[i].Body, w)
		}
	}
}

func TestMergeMessagesPrefersNamed(t *testing.T) {
	t.Parallel()

	at := func(sec int) time.Time { return time.Unix(int64(sec), 0) }
	cached := []domain.Message{{ID: "$1", Body: "hi", Timestamp: at(1)}}                     // no name
	fresh := []domain.Message{{ID: "$1", Body: "hi", SenderName: "Alice", Timestamp: at(1)}} // resolved

	// A freshly named copy upgrades an unnamed duplicate...
	if got := domain.MergeMessages(cached, fresh); got[0].SenderName != "Alice" {
		t.Errorf("merge did not upgrade to the named copy: %+v", got)
	}
	// ...and an unnamed copy never downgrades a named one, regardless of order.
	if got := domain.MergeMessages(fresh, cached); got[0].SenderName != "Alice" {
		t.Errorf("merge downgraded a named copy: %+v", got)
	}
}

func TestMergeMessagesRedaction(t *testing.T) {
	t.Parallel()

	at := func(sec int) time.Time { return time.Unix(int64(sec), 0) }
	real := []domain.Message{{ID: "$1", Sender: "@a:x", SenderName: "Alice", Body: "secret", Timestamp: at(5)}}
	// A live redaction arrives as a synthetic copy carrying the ID, the flag, and
	// whatever is left of the body — which is nothing unless the cache was told to keep
	// it ([display.deleted] keep).
	redaction := []domain.Message{{ID: "$1", Redacted: true, RedactedBy: "@mod:x", RedactedReason: "spam"}}

	// Folded on: the row survives — sender, name, timestamp — and the words do not.
	got := domain.MergeMessages(real, redaction)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (deduped)", len(got))
	}
	if m := got[0]; !m.Redacted || m.Sender != "@a:x" || m.SenderName != "Alice" ||
		!m.Timestamp.Equal(at(5)) {
		t.Errorf("redaction clobbered the real message: %+v", m)
	}
	if m := got[0]; m.Body != "" {
		t.Errorf("the deleted body survived the redaction: %q", m.Body)
	}
	if m := got[0]; m.RedactedBy != "@mod:x" || m.RedactedReason != "spam" {
		t.Errorf("who deleted it and why was lost: %+v", m)
	}

	// The race is the half that matters. A redaction can fold in before the page
	// carrying the message arrives — the server strips the content, but a buffer in
	// flight does not — so a redacted message must never regain a body.
	if m := domain.MergeMessages(redaction, real)[0]; !m.Redacted ||
		!m.Timestamp.Equal(at(5)) {
		t.Errorf("out-of-order redaction lost data: %+v", m)
	}
	if m := domain.MergeMessages(redaction, real)[0]; m.Body != "" {
		t.Errorf("an out-of-order copy put the deleted words back: %q", m.Body)
	}

	// Kept, the words travel with the redaction and stay.
	keeping := []domain.Message{{ID: "$1", Redacted: true, Body: "secret"}}
	if m := domain.MergeMessages(real, keeping)[0]; m.Body != "secret" {
		t.Errorf("keeping a deleted message lost it anyway: %+v", m)
	}
}

func TestMergeMessagesEdit(t *testing.T) {
	t.Parallel()

	at := func(sec int) time.Time { return time.Unix(int64(sec), 0) }
	orig := domain.Message{ID: "$1", Sender: "@a:x", SenderName: "Alice", Body: "old", Timestamp: at(5)}
	// An edit carries the target ID, the new body, Edited, and its own later ts.
	edit := domain.Message{ID: "$1", Sender: "@a:x", Body: "new", Timestamp: at(9), Edited: true}

	// Edit after the original: new body wins, Edited set, original ts kept in place.
	got := domain.MergeMessages([]domain.Message{orig}, []domain.Message{edit})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (folded)", len(got))
	}
	if m := got[0]; !m.Edited || m.Body != "new" || m.SenderName != "Alice" || !m.Timestamp.Equal(at(5)) {
		t.Errorf("edit fold wrong: %+v", m)
	}
	// Reverse order (edit seen first): identical — earliest ts + replacement body.
	if m := domain.MergeMessages([]domain.Message{edit}, []domain.Message{orig})[0]; !m.Edited ||
		m.Body != "new" || !m.Timestamp.Equal(at(5)) {
		t.Errorf("out-of-order edit wrong: %+v", m)
	}
	// A plain re-fetch of the original must not revert an already-edited body.
	if m := domain.MergeMessages(got, []domain.Message{orig})[0]; m.Body != "new" || !m.Edited {
		t.Errorf("plain duplicate reverted an edit: %+v", m)
	}
}

func TestAggregateReactions(t *testing.T) {
	t.Parallel()

	rs := []domain.Reaction{
		{ID: "$1", Target: "$m", Sender: "@a:x", Key: "👍"},
		{ID: "$2", Target: "$m", Sender: "@b:x", Key: "👍"},
		{ID: "$3", Target: "$m", Sender: "@a:x", Key: "❤️"},
		{ID: "$2", Target: "$m", Sender: "@b:x", Key: "👍"}, // duplicate event id — counted once
		{ID: "$4", Target: "$m", Sender: "@me:x", Key: "👍"},
	}
	got := domain.AggregateReactions(rs, "@me:x")
	if len(got) != 2 {
		t.Fatalf("tallies = %+v, want 2 keys", got)
	}
	// Ordered by first appearance: 👍 (count 3, includes me), then ❤️ (count 1).
	if got[0].Key != "👍" || got[0].Count != 3 || !got[0].Mine {
		t.Errorf("tally[0] = %+v, want 👍 count 3 mine", got[0])
	}
	if got[1].Key != "❤️" || got[1].Count != 1 || got[1].Mine {
		t.Errorf("tally[1] = %+v, want ❤️ count 1 not-mine", got[1])
	}
	if got := domain.AggregateReactions(nil, ""); len(got) != 0 {
		t.Errorf("empty input = %+v, want none", got)
	}
}

func TestMergeMessagesKeepsEmptyIDs(t *testing.T) {
	t.Parallel()

	// Messages without an event ID must not be collapsed into one.
	merged := domain.MergeMessages(
		[]domain.Message{{Body: "a"}},
		[]domain.Message{{Body: "b"}},
	)
	if len(merged) != 2 {
		t.Errorf("merged len = %d, want 2 (empty IDs never collide)", len(merged))
	}
}

// The message stream carries three kinds of thing: new messages, edits folded onto
// their target, and redaction markers. Only the first is a new event.
func TestMessageIsUpdate(t *testing.T) {
	t.Parallel()

	if (domain.Message{ID: "$1", Body: "hi"}).IsUpdate() {
		t.Error("a plain message is not a revision")
	}
	if !(domain.Message{ID: "$1", Redacted: true}).IsUpdate() {
		t.Error("a redaction marker revises a message already delivered")
	}
	if !(domain.Message{ID: "$1", Body: "fixed", Edited: true}).IsUpdate() {
		t.Error("an edit revises a message already delivered")
	}
}

// Caption is the words someone typed with an attachment, told apart from a body
// that is only the file name repeated (MSC2530).
func TestMessageCaption(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		msg  domain.Message
		want string
	}{
		"words under a photo are a caption": {
			domain.Message{Body: "look at this", Media: &domain.Media{Type: domain.MediaImage, Name: "IMG_1.jpg"}},
			"look at this",
		},
		"a body repeating the file name is not": {
			domain.Message{Body: "IMG_1.jpg", Media: &domain.Media{Type: domain.MediaImage, Name: "IMG_1.jpg"}},
			"",
		},
		"an attachment with no body at all": {
			domain.Message{Media: &domain.Media{Type: domain.MediaImage, Name: "IMG_1.jpg"}},
			"",
		},
		"a message with no attachment has no caption, only a body": {
			domain.Message{Body: "hello"},
			"",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.msg.Caption(); got != tc.want {
				t.Errorf("Caption() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Summary is what to say when there is no room to render a message properly: the
// body when there is one, and otherwise what the attachment is.
func TestMessageSummary(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		msg  domain.Message
		want string
	}{
		"a body speaks for itself": {domain.Message{Body: "hello"}, "hello"},
		"nothing to say":           {domain.Message{}, ""},
		"a named attachment": {
			domain.Message{Media: &domain.Media{Type: domain.MediaFile, Name: "invoice.pdf"}},
			"[file: invoice.pdf]",
		},
		"an unnamed attachment": {
			domain.Message{Media: &domain.Media{Type: domain.MediaImage}},
			"[image]",
		},
		"a caption beats the attachment": {
			domain.Message{Body: "look", Media: &domain.Media{Type: domain.MediaImage, Name: "cat.png"}},
			"look",
		},
		// The body repeats the file name (no `filename` field, or a sender that writes
		// both), so there is nothing anyone typed and the chip's wording wins.
		"a body that is only the file name is not a caption": {
			domain.Message{Body: "cat.png", Media: &domain.Media{Type: domain.MediaImage, Name: "cat.png"}},
			"[image: cat.png]",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.msg.Summary(); got != tc.want {
				t.Errorf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A covered message stays covered in the one place this client speaks without being
// asked to.
func TestANotificationCoversWhatTheSenderCovered(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		msg  domain.Message
		want string
	}{
		"a spoiler is replaced": {
			msg: domain.Message{
				Body: "the killer is ||the butler||",
				HTML: `the killer is <span data-mx-spoiler>the butler</span>`,
			},
			want: "the killer is ++spoiler++",
		},
		"a labeled spoiler is replaced the same way": {
			msg: domain.Message{
				Body: "||he dies||",
				HTML: `<span data-mx-spoiler="plot">he dies</span>`,
			},
			want: "++spoiler++",
		},
		"emphasis inside a spoiler does not leak the words": {
			msg: domain.Message{
				Body: "it was ||really him||",
				HTML: `it was <span data-mx-spoiler>really <strong>him</strong></span>`,
			},
			want: "it was ++spoiler++",
		},
		"two spoilers, both covered": {
			msg: domain.Message{
				Body: "||one|| and ||two||",
				HTML: `<span data-mx-spoiler>one</span> and <span data-mx-spoiler>two</span>`,
			},
			want: "++spoiler++ and ++spoiler++",
		},
		"a formatted message with nothing covered says what it always said": {
			msg: domain.Message{
				Body: "**shipped** it",
				HTML: "<b>shipped</b> it",
			},
			want: "**shipped** it",
		},
		"a plain message is untouched": {
			msg:  domain.Message{Body: "on my way"},
			want: "on my way",
		},
		"an attachment is still described, not read": {
			msg: domain.Message{
				Body:  "holiday.jpg",
				Media: &domain.Media{Type: domain.MediaImage, Name: "holiday.jpg"},
			},
			want: "[image: holiday.jpg]",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.msg.NotifyBody(); got != tc.want {
				t.Errorf("NotifyBody() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The timeline draws HTML over Body, so an edit replaces both: a kept HTML would show
// the old words beside the "edited" label.
func TestMergeMessagesEditReplacesTheFormatting(t *testing.T) {
	t.Parallel()

	orig := domain.Message{ID: "$1", Body: "old", HTML: "<b>old</b>", Timestamp: time.Unix(5, 0)}
	cases := []struct {
		name string
		edit domain.Message
		html string
	}{
		{"a formatted edit", domain.Message{ID: "$1", Body: "new", HTML: "<i>new</i>", Edited: true}, "<i>new</i>"},
		{"a plain edit", domain.Message{ID: "$1", Body: "new", Edited: true}, ""},
	}
	for _, c := range cases {
		got := domain.MergeMessages([]domain.Message{orig}, []domain.Message{c.edit})
		if len(got) != 1 || got[0].Body != "new" || got[0].HTML != c.html {
			t.Errorf("%s: %+v, want body new and html %q", c.name, got, c.html)
		}
		// And the original arriving after the edit changes neither.
		again := domain.MergeMessages(got, []domain.Message{orig})
		if again[0].Body != "new" || again[0].HTML != c.html {
			t.Errorf("%s, then the original: %+v", c.name, again[0])
		}
	}
}
