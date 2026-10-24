package domain

import (
	"net/url"
	"strings"
)

// Matrix addresses (matrix.to links and matrix: URIs) parsed into places.

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
	// Via are the routing servers the link suggests for reaching the room.
	Via []string
}

// String is the matrix.to form, without routing servers.
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

// URI is the matrix.to form with routing servers, readable back by ParsePlace.
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

// parseMatrixTo reads `https://matrix.to/#/<id>[/<event>][?via=…]`, case-insensitively,
// with or without `www.`.
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

// afterMatrixToHost strips a matrix.to link's scheme, host and `#/`.
func afterMatrixToHost(uri string) (string, bool) {
	for _, prefix := range []string{"https://matrix.to/#/", "https://www.matrix.to/#/",
		"http://matrix.to/#/", "http://www.matrix.to/#/", "matrix.to/#/", "www.matrix.to/#/"} {
		if hasFoldedPrefix(uri, prefix) {
			return uri[len(prefix):], true
		}
	}
	return "", false
}

// parseMatrixURI reads `matrix:<type>/<id>[/e/<event>]`.
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
		// Including `e`: an event without a room names nothing.
		return Place{}, false
	}
}

// cutEvent splits `<id>/e/<event>`; hasEvent lets a truncated "…/e" be refused rather
// than read as a room.
func cutEvent(tail string) (id, event string, hasEvent bool) {
	id, rest, ok := strings.Cut(tail, "/e")
	if !ok {
		return tail, "", false
	}
	return id, strings.TrimPrefix(rest, "/"), true
}

// eventID restores the "$" sigil that matrix: URIs omit.
func eventID(event string) EventID {
	if event == "" {
		return ""
	}
	return EventID("$" + event)
}

// placeFor classifies id by its sigil; without one it is not an address.
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

// splitVia cuts off the query and returns the `via=` values in order (the sender's
// order is their guess at which server answers best).
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

// hasFoldedPrefix is strings.HasPrefix ignoring case; a URI's scheme and host are
// case-insensitive.
func hasFoldedPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// isURIBoundary splits only on whitespace: a link contains slashes, colons and sigils.
func isURIBoundary(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
