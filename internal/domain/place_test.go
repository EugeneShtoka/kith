package domain_test

import (
	"reflect"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Both spellings (matrix.to and matrix:) of every kind of place.
func TestParsePlaceReadsBothSpellings(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		uri  string
		want domain.Place
	}{
		"matrix.to, a person": {
			uri:  "https://matrix.to/#/@alice:example.org",
			want: domain.Place{Kind: domain.PlacePerson, User: "@alice:example.org"},
		},
		"matrix.to, a person with the sigil encoded": {
			uri:  "https://matrix.to/#/%40alice%3Aexample.org",
			want: domain.Place{Kind: domain.PlacePerson, User: "@alice:example.org"},
		},
		"matrix.to, a room by id": {
			uri:  "https://matrix.to/#/!abc:example.org",
			want: domain.Place{Kind: domain.PlaceRoom, Room: "!abc:example.org"},
		},
		"matrix.to, a room by alias": {
			uri:  "https://matrix.to/#/%23kith:example.org",
			want: domain.Place{Kind: domain.PlaceRoom, Room: "#kith:example.org"},
		},
		"matrix.to, a message in a room": {
			uri:  "https://matrix.to/#/!abc:example.org/$event123",
			want: domain.Place{Kind: domain.PlaceEvent, Room: "!abc:example.org", Event: "$event123"},
		},
		"matrix.to, via servers": {
			uri: "https://matrix.to/#/!abc:example.org/$e?via=one.org&via=two.org",
			want: domain.Place{
				Kind: domain.PlaceEvent, Room: "!abc:example.org", Event: "$e",
				Via: []string{"one.org", "two.org"},
			},
		},
		"matrix.to, www and uppercase host": {
			uri:  "HTTPS://WWW.MATRIX.TO/#/@bob:example.org",
			want: domain.Place{Kind: domain.PlacePerson, User: "@bob:example.org"},
		},
		"matrix: a person": {
			uri:  "matrix:u/alice:example.org",
			want: domain.Place{Kind: domain.PlacePerson, User: "@alice:example.org"},
		},
		"matrix: a room by alias": {
			uri:  "matrix:r/kith:example.org",
			want: domain.Place{Kind: domain.PlaceRoom, Room: "#kith:example.org"},
		},
		"matrix: a room by id": {
			uri:  "matrix:roomid/abc:example.org",
			want: domain.Place{Kind: domain.PlaceRoom, Room: "!abc:example.org"},
		},
		"matrix: a message in a room": {
			uri:  "matrix:roomid/abc:example.org/e/event123",
			want: domain.Place{Kind: domain.PlaceEvent, Room: "!abc:example.org", Event: "$event123"},
		},
		"matrix: via servers": {
			uri: "matrix:r/kith:example.org?via=one.org",
			want: domain.Place{
				Kind: domain.PlaceRoom, Room: "#kith:example.org", Via: []string{"one.org"},
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := domain.ParsePlace(tc.uri)
			if !ok {
				t.Fatalf("ParsePlace(%q) said it was not a place", tc.uri)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParsePlace(%q) =\n  %+v\nwant\n  %+v", tc.uri, got, tc.want)
			}
		})
	}
}

// Not places: anything not understood is refused rather than approximated.
func TestParsePlaceRefusesWhatIsNotOne(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"https://example.org/room/!abc:example.org", // somebody else's site
		"https://matrix.to/",                        // the homepage
		"https://matrix.to/#/",                      // an empty fragment
		"https://matrix.to/#/alice:example.org",     // no sigil: not an address
		"matrix:",                                   // nothing at all
		"matrix:u",                                  // a type with no id
		"matrix:roomid/abc:example.org/e",           // a message segment, truncated
		"matrix:x/abc:example.org",                  // a type nobody defined
		"matrix:e/event123",                         // an event with no room to find it in
		"mailto:alice@example.org",
		"just a sentence",
		"",
	} {
		if got, ok := domain.ParsePlace(uri); ok {
			t.Errorf("ParsePlace(%q) = %+v, want not a place", uri, got)
		}
	}
}

// Addresses come from the body and from hrefs (a pill shows "Alice" but links the MXID).
func TestPlaceLinksReadsTheWordsAndTheTargets(t *testing.T) {
	t.Parallel()

	body := "see https://matrix.to/#/!room:example.org and matrix:u/bob:example.org, plus https://example.org/nope"
	hrefs := []string{
		"https://matrix.to/#/@alice:example.org", // the pill
		"https://example.org/docs",               // an ordinary named link
		"https://matrix.to/#/!room:example.org",  // the same room again
	}
	got := domain.PlaceLinks(body, hrefs)

	want := []domain.Place{
		{Kind: domain.PlaceRoom, Room: "!room:example.org"},
		{Kind: domain.PlacePerson, User: "@bob:example.org"},
		{Kind: domain.PlacePerson, User: "@alice:example.org"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PlaceLinks() =\n  %+v\nwant\n  %+v", got, want)
	}
}

// A message with no addresses yields none.
func TestPlaceLinksFindsNothingInOrdinaryProse(t *testing.T) {
	t.Parallel()

	if got := domain.PlaceLinks("lunch at one? https://example.org/menu", nil); len(got) != 0 {
		t.Errorf("PlaceLinks() = %+v, want none", got)
	}
}

// String and ParsePlace round-trip.
func TestPlaceStringIsTheSpellingPeopleRecognize(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"https://matrix.to/#/@alice:example.org",
		"https://matrix.to/#/!abc:example.org",
		"https://matrix.to/#/!abc:example.org/$event123",
	} {
		place, ok := domain.ParsePlace(uri)
		if !ok {
			t.Fatalf("ParsePlace(%q) said it was not a place", uri)
		}
		if got := place.String(); got != uri {
			t.Errorf("String() = %q, want %q", got, uri)
		}
	}
}

// A mention's URI parses back, so pills are followable (kith handles the scheme itself).
func TestMentionURIRoundTrips(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		mention domain.Mention
		want    string
	}{
		{"a person", domain.Mention{UserID: "@dana:example.org"}, "matrix:u/dana:example.org"},
		{"a room by id", domain.Mention{RoomID: "!abc:example.org"}, "matrix:roomid/abc:example.org"},
		{"a room by alias", domain.Mention{RoomID: "#standup:example.org"}, "matrix:r/standup:example.org"},
		{"nothing nameable", domain.Mention{Name: "Dana"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.mention.URI()
			if got != tc.want {
				t.Fatalf("URI = %q, want %q", got, tc.want)
			}
			if got == "" {
				return
			}
			place, ok := domain.ParsePlace(got)
			if !ok {
				t.Fatalf("ParsePlace(%q) refused the URI this client writes", got)
			}
			if target := tc.mention.Target(); place.User != target && place.Room != target {
				t.Errorf("parsed back to %+v, want it to name %s", place, target)
			}
		})
	}
}

// URI keeps the routing servers.
func TestAPlaceRoundTripsThroughItsURI(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"https://matrix.to/#/!room:example.org?via=one.org",
		"https://matrix.to/#/#book-club:example.org?via=one.org&via=two.org",
		"https://matrix.to/#/!room:example.org/$event?via=one.org",
		"https://matrix.to/#/@alice:example.org",
	} {
		place, ok := domain.ParsePlace(uri)
		if !ok {
			t.Fatalf("domain.ParsePlace(%q) did not read it", uri)
		}
		back, ok := domain.ParsePlace(place.URI())
		if !ok {
			t.Fatalf("URI() = %q, which does not parse", place.URI())
		}
		if !reflect.DeepEqual(place, back) {
			t.Errorf("%q round-tripped to %+v, want %+v (via %q)", uri, back, place, place.URI())
		}
	}

	// String omits via, so links differing only in routes deduplicate in PlaceLinks.
	with, _ := domain.ParsePlace("https://matrix.to/#/!room:example.org?via=one.org")
	without, _ := domain.ParsePlace("https://matrix.to/#/!room:example.org")
	if with.String() != without.String() {
		t.Errorf("String() = %q and %q; the destination is the same", with.String(), without.String())
	}
}
