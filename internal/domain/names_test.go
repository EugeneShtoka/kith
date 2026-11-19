package domain_test

import (
	"regexp"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestNameRulesShapeByTheNarrowestRule(t *testing.T) {
	t.Parallel()

	on, off := true, false
	rules := domain.NameRules{Rules: []domain.NameRule{
		{Match: "tag:Work", FirstNameOnly: &on},
		{Match: "!wa:example.org", Strip: regexp.MustCompile(`\s*\(WA\)$`)},
		{Match: "!plain:example.org", FirstNameOnly: &off},
		{Match: "tag:Work", Sender: "@sveta:example.org", Name: "Sveta"},
	}}
	work := func(room, sender string) domain.Scope {
		return domain.Scope{RoomID: room, Space: "tag:Work", Sender: sender}
	}
	cases := []struct {
		name  string
		rules domain.NameRules
		scope domain.Scope
		in    string
		want  string
	}{
		{"no rule names the room: the name as it is", rules, domain.Scope{RoomID: "!x:example.org", Space: "Friends"}, "Dana Levi", "Dana Levi"},
		{"the global choice where no rule names the room", domain.NameRules{FirstNameOnly: true}, domain.Scope{RoomID: "!x:example.org"}, "Dana Levi", "Dana"},
		{"the space's rule", rules, work("!x:example.org", "@dana:example.org"), "Dana Levi", "Dana"},
		{"a room's rule wins whole over the space's", rules, work("!wa:example.org", "@dana:example.org"), "Dana Levi (WA)", "Dana Levi"},
		{"a room's rule turns the space's first names off", rules, work("!plain:example.org", "@dana:example.org"), "Dana Levi", "Dana Levi"},
		{"one person's fixed name in the space", rules, work("!x:example.org", "@sveta:example.org"), "neke", "Sveta"},
		{"a strip that removes the whole name keeps it", domain.NameRules{Rules: []domain.NameRule{{Match: "!x:example.org", Strip: regexp.MustCompile(`.*`)}}}, domain.Scope{RoomID: "!x:example.org"}, "Dana", "Dana"},
	}
	for _, c := range cases {
		if got := c.rules.Shape(c.scope, c.in); got != c.want {
			t.Errorf("%s: Shape(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
