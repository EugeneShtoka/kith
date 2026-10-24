package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Plain messages matter most: bridged lines carry no formatting.
func TestTrackedWordIsMarkedInAPlainMessage(t *testing.T) {
	t.Parallel()

	m := dayRoom(t)
	m.prefs.display.Tracked = config.Tracked{Words: []string{"deploy"}}
	m = m.applyNaming(m.prefs.display)
	const body = "the deploy is done"
	style := m.richStyleFor(m.timeline.messages[0], body, nil, nil)
	if len(style.tracked) != 1 {
		t.Fatalf("tracked ranges = %+v, want one", style.tracked)
	}
	if got := body[style.tracked[0].start:style.tracked[0].end]; got != "deploy" {
		t.Errorf("range covers %q, want deploy", got)
	}
	row := styledRow(body, bidi.LeftToRight, style.at)
	if !strings.Contains(row, "\x1b[") {
		t.Error("the row carries no styling at all, so the mark cannot be visible")
	}
	// The row width must not change: every scroll total is computed from it.
	if got, want := ansi.StringWidth(row), ansi.StringWidth(body); got != want {
		t.Errorf("width = %d, want %d", got, want)
	}
}

func TestNoTrackedWordsNoMarks(t *testing.T) {
	t.Parallel()

	m := dayRoom(t)
	if style := m.richStyleFor(m.timeline.messages[0], "the deploy is done", nil, nil); len(style.tracked) != 0 {
		t.Errorf("tracked = %+v with an empty list", style.tracked)
	}
}

// A wrapped message narrows its marks to the row that draws them; the offsets are into
// the whole body.
func TestTrackedMarksNarrowToTheirRow(t *testing.T) {
	t.Parallel()

	m := dayRoom(t)
	m.prefs.display.Tracked = config.Tracked{Words: []string{"invoice"}}
	m = m.applyNaming(m.prefs.display)
	const body = "first line here\nthe invoice arrived"
	style := m.richStyleFor(m.timeline.messages[0], body, nil, nil)
	if len(style.tracked) != 1 {
		t.Fatalf("tracked = %+v, want one", style.tracked)
	}
	// The second line, rebased: the window drawing it sees the hit relative to itself.
	from := strings.Index(body, "the invoice")
	windowed := style.window(nil, from, len(body))
	if len(windowed.tracked) != 1 {
		t.Fatalf("windowed tracked = %+v, want the hit", windowed.tracked)
	}
	line := body[from:]
	if got := line[windowed.tracked[0].start:windowed.tracked[0].end]; got != "invoice" {
		t.Errorf("rebased range covers %q, want invoice", got)
	}
	// The row before it holds nothing.
	if before := style.window(nil, 0, from); len(before.tracked) != 0 {
		t.Errorf("first row carries %+v, want no marks", before.tracked)
	}
}

// The tracked list groups hits by word, newest first within each.
func TestTrackedListGroupsByWord(t *testing.T) {
	t.Parallel()

	stamp := func(min int) time.Time { return time.Date(2026, 9, 19, 10, min, 0, 0, time.UTC) }
	hits := []domain.SearchHit{
		{EventID: mx + "$1", Word: "invoice", Timestamp: stamp(1)},
		{EventID: mx + "$2", Word: "deploy", Timestamp: stamp(2)},
		{EventID: mx + "$3", Word: "invoice", Timestamp: stamp(3)},
		{EventID: mx + "$4", Word: "deploy", Timestamp: stamp(4)},
	}
	trackedList{}.order(hits)
	var got []string
	for _, h := range hits {
		got = append(got, h.Word+":"+string(h.EventID))
	}
	want := []string{"deploy:" + mx + "$4", "deploy:" + mx + "$2", "invoice:" + mx + "$3", "invoice:" + mx + "$1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// Each row shows its word while grouped, not when the list was opened for one word.
func TestTrackedListLabelsOnlyWhenGrouped(t *testing.T) {
	t.Parallel()

	hit := domain.SearchHit{Word: "deploy"}
	if got := (trackedList{}).label(hit); got != "deploy" {
		t.Errorf("grouped label = %q, want the word", got)
	}
	if got := (trackedList{only: "deploy"}).label(hit); got != "" {
		t.Errorf("single-word label = %q, want nothing", got)
	}
}

// The filter carries the word list, since hits depend on config the cache lacks;
// naming one word narrows the same clause.
func TestTrackedListNarrowsWithItsWords(t *testing.T) {
	t.Parallel()

	var all domain.SearchFilter
	trackedList{words: []string{"deploy", "invoice"}}.narrow(&all)
	if !all.Tracked || len(all.Words) != 2 {
		t.Errorf("filter = %+v, want both words", all)
	}
	if all.Empty() {
		t.Error("the clause alone must be a query, or the list needs terms typed first")
	}
	var one domain.SearchFilter
	trackedList{words: []string{"deploy", "invoice"}, only: "deploy"}.narrow(&one)
	if len(one.Words) != 1 || one.Words[0] != "deploy" {
		t.Errorf("named filter = %+v, want just deploy", one)
	}
}
