package domain

import (
	"testing"
	"time"
)

var searchNow = time.Date(2026, 9, 1, 14, 30, 0, 0, time.UTC)

func TestParseSearchLiftsFilters(t *testing.T) {
	got := ParseSearch("invoice from:@dana:example.org since:2026-08-01 paid", searchNow)
	if got.Terms != "invoice paid" {
		t.Errorf("terms = %q, want the filters lifted out", got.Terms)
	}
	if got.Sender != "@dana:example.org" {
		t.Errorf("sender = %q", got.Sender)
	}
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !got.Since.Equal(want) {
		t.Errorf("since = %v, want %v", got.Since, want)
	}
}

// A name is what you remember; the MXID is what the completion popup offers.
func TestParseSearchSenderForms(t *testing.T) {
	for in, want := range map[string]string{
		"from:dana":              "dana",
		"from:@dana":             "dana",
		"from:@dana:example.org": "@dana:example.org",
	} {
		if got := ParseSearch(in, searchNow).Sender; got != want {
			t.Errorf("%q -> sender %q, want %q", in, got, want)
		}
	}
}

func TestParseSearchDates(t *testing.T) {
	cases := map[string]time.Time{
		"since:today":     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		"since:yesterday": time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		"since:7d":        searchNow.AddDate(0, 0, -7),
		"since:2w":        searchNow.AddDate(0, 0, -14),
		"since:3m":        searchNow.AddDate(0, -3, 0),
		"since:1y":        searchNow.AddDate(-1, 0, 0),
	}
	for in, want := range cases {
		if got := ParseSearch(in, searchNow).Since; !got.Equal(want) {
			t.Errorf("%q -> %v, want %v", in, got, want)
		}
	}
}

// A bound written as a date means a day, and the day you named belongs in the
// results you asked for.
func TestUntilIncludesTheWholeDay(t *testing.T) {
	got := ParseSearch("until:2026-08-31", searchNow).Until
	want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("until = %v, want the start of the next day (%v)", got, want)
	}
}

// Someone typing `since:yest` is mid-word.
func TestUnparseableFiltersStayTerms(t *testing.T) {
	for _, in := range []string{"since:yest", "until:nonsense", "since:", "colon:in:middle"} {
		got := ParseSearch(in, searchNow)
		if !got.Since.IsZero() || !got.Until.IsZero() {
			t.Errorf("%q was parsed as a date: %+v", in, got)
		}
		if got.Terms != in {
			t.Errorf("%q -> terms %q, want it left alone", in, got.Terms)
		}
	}
}

// An unknown prefix is not a filter and must reach the matcher untouched — a
// message really can contain "http://example.org".
func TestUnknownPrefixesAreTerms(t *testing.T) {
	got := ParseSearch("see http://example.org", searchNow)
	if got.Terms != "see http://example.org" {
		t.Errorf("terms = %q", got.Terms)
	}
}

func TestSearchFilterEmpty(t *testing.T) {
	if !ParseSearch("   ", searchNow).Empty() {
		t.Error("whitespace should be an empty filter")
	}
	// A filter with no terms is still a question: everything Dana said.
	if ParseSearch("from:dana", searchNow).Empty() {
		t.Error("a sender-only filter is not empty")
	}
}
