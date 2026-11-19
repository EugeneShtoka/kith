package domain

import (
	"regexp"
	"strings"
)

// Name rules: how a person's name is shown in one place, global → space → room, each
// rung narrowed by sender, on the scope ladder every setting uses.

// NameRule is one place's way of showing names. The narrowest rule naming a place wins
// whole; a FirstNameOnly it leaves unset follows the global choice.
type NameRule struct {
	// Match is a room ID or a home entry (NamesHome); Sender a person's ID.
	Match  string
	Sender string
	// Name is a fixed name, shown instead of any other; empty shapes the name.
	Name string
	// Strip is removed from the name wherever it matches (a bridge's suffix); nil none.
	Strip         *regexp.Regexp
	FirstNameOnly *bool
}

// NameRules is the global choice and the rules that narrow it.
type NameRules struct {
	FirstNameOnly bool
	Rules         []NameRule
}

// In is the rule in force at scope: the narrowest naming it, else the global choice.
func (r NameRules) In(scope Scope) NameRule {
	var rule NameRule
	if at, ok := Narrowest(Matches(r.Rules, func(n NameRule) Match {
		return Match{Place: n.Match, Sender: n.Sender}
	}), scope); ok {
		rule = r.Rules[at]
	}
	if rule.FirstNameOnly == nil {
		rule.FirstNameOnly = &r.FirstNameOnly
	}
	return rule
}

// Shape is name as scope's rule shows it.
func (r NameRules) Shape(scope Scope, name string) string { return r.In(scope).Shape(name) }

// FirstOnly reports whether the rule keeps first names only.
func (n NameRule) FirstOnly() bool { return n.FirstNameOnly != nil && *n.FirstNameOnly }

// Shape is name as the rule shows it: its fixed name, else name with Strip removed (all
// of it removed keeps name), cut to its first word where FirstOnly.
func (n NameRule) Shape(name string) string {
	if n.Name != "" {
		return n.Name
	}
	if n.Strip != nil {
		if stripped := strings.TrimSpace(n.Strip.ReplaceAllString(name, "")); stripped != "" {
			name = stripped
		}
	}
	if n.FirstOnly() {
		name = FirstName(name)
	}
	return name
}
