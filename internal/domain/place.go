package domain

import (
	"net/url"
	"strings"
)

// An address somebody sent you, read into something this client can go to.

// PlaceKind is what an address names.
type PlaceKind int

const (
	// PlaceNone is the zero value: not an address at all.
	PlaceNone PlaceKind = iota
	// PlacePerson is somebody's MXID.
	PlacePerson
	// PlaceRoom is a room, by ID or by alias.
	PlaceRoom
	// PlaceEvent is one message: a room and an event in it.
	PlaceEvent
)

// Place is one address: what kind it is, and the parts that make it up.
type Place struct {
	Kind PlaceKind
	// User is the MXID, for a person.
	User string
	// Room is the room ID (`!id:server`) or alias (`#alias:server`).
	Room string
	// Event is the message, for a place that names one.
	Event EventID
	// Via are the servers the sender suggests asking about a room they may know and
	// yours may not.
	Via []string
}

// String is the matrix.to spelling — the one people recognize on sight, and the one
// worth showing in a picker row or a status line.
func (p Place) String() string {
	switch p.Kind {
	case PlacePerson:
		return matrixToPrefix + p.User
	case PlaceRoom:
		return matrixToPrefix + p.Room
	case PlaceEvent:
		return matrixToPrefix + p.Room + "/" + string(p.Event)
	case PlaceNone:
		return ""
	}
	return ""
}

// URI is the address written so that ParsePlace can read every part of it back, routing
// servers included.
func (p Place) URI() string {
	base := p.String()
	if base == "" {
		return ""
	}
	return base + p.viaQuery()
}

// viaQuery writes the routing servers the way splitVia reads them, in order.
func (p Place) viaQuery() string {
	if len(p.Via) == 0 {
		return ""
	}
	parts := make([]string, 0, len(p.Via))
	for _, server := range p.Via {
		parts = append(parts, "via="+url.QueryEscape(server))
	}
	return "?" + strings.Join(parts, "&")
}

const matrixToPrefix = "https://matrix.to/#/"

// ParsePlace reads one URI as an address, and reports whether it is one.
func ParsePlace(uri string) (Place, bool) {
	uri = strings.TrimSpace(uri)
	switch {
	case uri == "":
		return Place{}, false
	case hasFoldedPrefix(uri, "matrix:"):
		return parseMatrixURI(uri[len("matrix:"):])
	default:
		return parseMatrixTo(uri)
	}
}

// parseMatrixTo reads `https://matrix.to/#/<id>[/<event>][?via=…]`, in any case and
// with or without the `www.` somebody's client may have written.
func parseMatrixTo(uri string) (Place, bool) {
	rest, ok := afterMatrixToHost(uri)
	if !ok {
		return Place{}, false
	}
	rest, via := splitVia(rest)
	first, second, _ := strings.Cut(rest, "/")
	id, err := url.PathUnescape(first)
	if err != nil || id == "" {
		return Place{}, false
	}
	event := ""
	if second != "" {
		if event, err = url.PathUnescape(second); err != nil {
			return Place{}, false
		}
	}
	return placeFor(id, EventID(event), via)
}

// afterMatrixToHost strips the scheme, the host and the `#/` that every matrix.to link
// carries, and reports whether this was one at all.
func afterMatrixToHost(uri string) (string, bool) {
	for _, prefix := range []string{"https://matrix.to/#/", "https://www.matrix.to/#/",
		"http://matrix.to/#/", "http://www.matrix.to/#/", "matrix.to/#/", "www.matrix.to/#/"} {
		if hasFoldedPrefix(uri, prefix) {
			return uri[len(prefix):], true
		}
	}
	return "", false
}

// parseMatrixURI reads the `matrix:` scheme's own spelling: a type, an id, and
// optionally `/e/<event>`.
func parseMatrixURI(rest string) (Place, bool) {
	rest, via := splitVia(rest)
	kind, tail, ok := strings.Cut(rest, "/")
	if !ok || tail == "" {
		return Place{}, false
	}
	id, event, hasEvent := cutEvent(tail)
	if id == "" || (hasEvent && event == "") {
		return Place{}, false
	}
	switch kind {
	case "u":
		if hasEvent {
			return Place{}, false // a person has no event
		}
		return placeFor("@"+id, "", via)
	case "r":
		return placeFor("#"+id, eventID(event), via)
	case "roomid":
		return placeFor("!"+id, eventID(event), via)
	default:
		// `e` among them: an event with no room to find it in names nothing.
		return Place{}, false
	}
}

// cutEvent splits `<id>/e/<event>` into its two halves, reporting whether the message
// segment was there at all — so a truncated one ("…/e") is refused rather than read as
// a room.
func cutEvent(tail string) (id, event string, hasEvent bool) {
	id, rest, ok := strings.Cut(tail, "/e")
	if !ok {
		return tail, "", false
	}
	return id, strings.TrimPrefix(rest, "/"), true
}

// eventID puts the sigil back on an event the URI scheme wrote without one.
func eventID(event string) EventID {
	if event == "" {
		return ""
	}
	return EventID("$" + event)
}

// placeFor decides which kind of place an id names, by its sigil. A string with no
// sigil is not an address — it is a word that looks like one.
func placeFor(id string, event EventID, via []string) (Place, bool) {
	switch {
	case strings.HasPrefix(id, "@"):
		if event != "" {
			return Place{}, false
		}
		return Place{Kind: PlacePerson, User: id, Via: via}, true
	case strings.HasPrefix(id, "!"), strings.HasPrefix(id, "#"):
		if event != "" {
			return Place{Kind: PlaceEvent, Room: id, Event: event, Via: via}, true
		}
		return Place{Kind: PlaceRoom, Room: id, Via: via}, true
	default:
		return Place{}, false
	}
}

// splitVia takes the query off the end and returns the `via=` values in the order they
// were written: they are the servers to ask, and the sender's order is their guess at
// which is most likely to answer.
func splitVia(rest string) (string, []string) {
	head, query, ok := strings.Cut(rest, "?")
	if !ok {
		return rest, nil
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return head, nil
	}
	return head, values["via"]
}

// hasFoldedPrefix is strings.HasPrefix ignoring ASCII case, because a URI's scheme and
// host are case-insensitive and people paste them as their client wrote them.
func hasFoldedPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// PlaceLinks is every address in one message: the ones written in the words, and the
// ones only the markup knows.
func PlaceLinks(body string, hrefs []string) []Place {
	var out []Place
	seen := make(map[string]bool)
	add := func(candidate string) {
		place, ok := ParsePlace(candidate)
		if !ok {
			return
		}
		key := place.String()
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, place)
	}
	for _, word := range strings.FieldsFunc(body, isURIBoundary) {
		add(strings.Trim(word, ".,;:!?\"'()[]<>"))
	}
	for _, href := range hrefs {
		add(href)
	}
	return out
}

// isURIBoundary splits prose into the runs that could be a URI: whitespace ends one,
// and nothing else does — a matrix.to link contains slashes, colons and sigils, and
// splitting on any of those would cut an address in half.
func isURIBoundary(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
