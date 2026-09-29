package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// searchKind is what the results pane is showing: a word search, or a list that is a
// search with the question already asked. One stateless empty struct per kind, so an
// added kind must answer every method, and searchState stays comparable.
type searchKind interface {
	// name is the first word of the results title.
	name() string
	// narrow puts this kind's clause on the filter; a list with no terms is still a
	// non-empty query.
	narrow(f *domain.SearchFilter)
	// summary is the header line over the results. An empty search is a failed
	// question; an empty list is a fact.
	summary(m Model, where string) string
	// showsFileName reports whether a row carries the attachment name in its own
	// column, apart from the excerpt so a Latin name cannot flip an RTL caption.
	showsFileName() bool
	// label is a short prefix on a row's excerpt, or "".
	label(hit domain.SearchHit) string
	// order arranges the hits in place; a no-op keeps the cache's newest-first.
	order(hits []domain.SearchHit)
}

// countedSummary is the shared "N things <where>, newest first" line for a list.
func countedSummary(n int, where, none, one, plural string) string {
	switch {
	case n == 0:
		return none + " " + where
	case n == 1:
		return one + " " + where
	case n >= searchLimit:
		return fmt.Sprintf("%d+ %s %s — the newest first", searchLimit, plural, where)
	default:
		return fmt.Sprintf("%d %s %s, newest first", n, plural, where)
	}
}

// wordsList is a search for terms, the only kind that needs a query.
type wordsList struct{}

func (wordsList) name() string                  { return "Search" }
func (wordsList) label(domain.SearchHit) string { return "" }
func (wordsList) order([]domain.SearchHit)      {}
func (wordsList) narrow(*domain.SearchFilter)   {}
func (wordsList) showsFileName() bool           { return false }

func (wordsList) summary(m Model, where string) string {
	switch n := len(m.search.hits); {
	case m.search.err != nil:
		return "search failed: " + m.search.err.Error()
	case strings.TrimSpace(m.search.query) == "":
		return "type to search " + where
	case n == 0:
		return fmt.Sprintf("no matches %s", where)
	case n == 1:
		return fmt.Sprintf("1 match %s", where)
	case n >= searchLimit:
		return fmt.Sprintf("%d+ matches %s — narrow the search", searchLimit, where)
	default:
		return fmt.Sprintf("%d matches %s", n, where)
	}
}

// mentionsList is every message that names you.
type mentionsList struct{}

func (mentionsList) name() string                  { return "Mentions" }
func (mentionsList) label(domain.SearchHit) string { return "" }
func (mentionsList) order([]domain.SearchHit)      {}
func (mentionsList) showsFileName() bool           { return false }

func (mentionsList) narrow(f *domain.SearchFilter) { f.Mentioned = true }

func (mentionsList) summary(m Model, where string) string {
	if m.search.err != nil {
		return "could not list mentions: " + m.search.err.Error()
	}
	if q := strings.TrimSpace(m.search.query); q != "" {
		return fmt.Sprintf("%d of your mentions %s match %s", len(m.search.hits), where, quoted(q))
	}
	return countedSummary(len(m.search.hits), where, "nobody has named you", "1 mention", "mentions")
}

// starredList is every message you bookmarked — the private counterpart of a pin.
type starredList struct{}

func (starredList) name() string                  { return "Starred" }
func (starredList) label(domain.SearchHit) string { return "" }
func (starredList) order([]domain.SearchHit)      {}
func (starredList) showsFileName() bool           { return false }

func (starredList) narrow(f *domain.SearchFilter) { f.Starred = true }

func (starredList) summary(m Model, where string) string {
	if m.search.err != nil {
		return "could not list what you starred: " + m.search.err.Error()
	}
	if q := strings.TrimSpace(m.search.query); q != "" {
		return fmt.Sprintf("%d starred %s match %s", len(m.search.hits), where, quoted(q))
	}
	return countedSummary(len(m.search.hits), where, "nothing starred", "1 starred message", "starred")
}

// trackedList is every message carrying one of your tracked words, grouped by word
// (the cache stamps each hit with the entry that caught it).
type trackedList struct {
	words []string // the configured list, carried because narrow cannot reach config
	// only is the word `/tracked <word>` named; given, the list stops grouping.
	only string
}

func (trackedList) name() string        { return "Tracked" }
func (trackedList) showsFileName() bool { return false }

// label is the entry that caught the row, shown only while grouped.
func (t trackedList) label(hit domain.SearchHit) string {
	if t.only != "" {
		return ""
	}
	return hit.Word
}

// order is the grouping: by word, newest first within each. A sort rather than heading
// rows, so the cursor stays a position in the hits.
func (trackedList) order(hits []domain.SearchHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Word != hits[j].Word {
			return hits[i].Word < hits[j].Word
		}
		return hits[i].Timestamp.After(hits[j].Timestamp)
	})
}

func (t trackedList) narrow(f *domain.SearchFilter) {
	f.Tracked = true
	if t.only != "" {
		f.Words = []string{t.only}
		return
	}
	f.Words = t.words
}

// summary counts the hits and, when grouped, how many words they came from.
func (t trackedList) summary(m Model, where string) string {
	if m.search.err != nil {
		return "could not list tracked words: " + m.search.err.Error()
	}
	if t.only != "" {
		return fmt.Sprintf("%d for %s %s, newest first", len(m.search.hits), quoted(t.only), where)
	}
	if len(t.words) == 0 {
		return "nothing is tracked yet — add words under [tracked] in the config"
	}
	switch n := len(m.search.hits); {
	case n == 0:
		return "none of your tracked words have come up " + where
	case n == 1:
		return "1 mention of a tracked word " + where
	case n >= searchLimit:
		return fmt.Sprintf("%d+ across %d words %s — the newest first", searchLimit, t.distinct(m), where)
	default:
		return fmt.Sprintf("%d across %d words %s, newest first", n, t.distinct(m), where)
	}
}

// distinct is how many different entries the hits came from.
func (trackedList) distinct(m Model) int {
	seen := map[string]bool{}
	for i := range m.search.hits {
		seen[m.search.hits[i].Word] = true
	}
	return len(seen)
}

// filesList is every message carrying an attachment.
type filesList struct{}

func (filesList) name() string                  { return "Files" }
func (filesList) label(domain.SearchHit) string { return "" }
func (filesList) order([]domain.SearchHit)      {}
func (filesList) showsFileName() bool           { return true }

func (filesList) narrow(f *domain.SearchFilter) { f.HasFile = true }

func (filesList) summary(m Model, where string) string {
	if m.search.err != nil {
		return "could not list files: " + m.search.err.Error()
	}
	if q := strings.TrimSpace(m.search.query); q != "" {
		return fmt.Sprintf("%d files %s match %s", len(m.search.hits), where, quoted(q))
	}
	return countedSummary(len(m.search.hits), where, "no files", "1 file", "files")
}

// caughtList previews what the spam filters would catch: the tracked list's query over
// the filters' words, through the same matcher the daemon uses (domain.Tracked).
type caughtList struct {
	words   []string          // every filter's words, flattened
	filters map[string]string // word → the filter that owns it, for the row label
	// senders counts sender-only filters, which a word query cannot show; the summary
	// says so rather than silently covering less than the rules do.
	senders int
}

func (caughtList) name() string             { return "Caught" }
func (caughtList) showsFileName() bool      { return false }
func (caughtList) order([]domain.SearchHit) {}

// label names the filter that caught the row.
func (c caughtList) label(hit domain.SearchHit) string {
	if name, ok := c.filters[hit.Word]; ok {
		return name
	}
	return hit.Word
}

func (c caughtList) narrow(f *domain.SearchFilter) {
	f.Tracked = true
	f.Words = c.words
}

func (c caughtList) summary(m Model, where string) string {
	if m.search.err != nil {
		return "could not preview the filters: " + m.search.err.Error()
	}
	if len(c.words) == 0 && c.senders == 0 {
		return "no spam filters yet — add one under [[spam.filter]] in the config"
	}
	if len(c.words) == 0 {
		return c.bySender() + " — nothing here is matched by words, so there is nothing to preview"
	}
	if q := strings.TrimSpace(m.search.query); q != "" {
		return fmt.Sprintf("%d caught %s match %s", len(m.search.hits), where, quoted(q))
	}
	var caught string
	switch n := len(m.search.hits); {
	case n == 0:
		caught = "your filters catch nothing " + where
	case n == 1:
		caught = "1 message caught " + where + " — this is what the filters see"
	case n >= searchLimit:
		caught = fmt.Sprintf("%d+ caught %s — the newest first", searchLimit, where)
	default:
		caught = fmt.Sprintf("%d caught %s, newest first", n, where)
	}
	if c.senders > 0 {
		caught += " · " + c.bySender() + ", not shown"
	}
	return caught
}

// bySender names what the preview leaves out, in the plural it needs.
func (c caughtList) bySender() string {
	if c.senders == 1 {
		return "1 filter catches by sender"
	}
	return fmt.Sprintf("%d filters catch by sender", c.senders)
}
