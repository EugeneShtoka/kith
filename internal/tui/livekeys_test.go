package tui

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// bareKey is a key name written into prose, where a {path} token belongs.
var bareKey = regexp.MustCompile(`(^|[\s(` + "`" + `])(tab|enter|esc|ctrl\+\w+|alt\+\w+|shift\+\w+)\b`)

// Prose that tells a key names it by binding ({search.scope}), so a rebound key is
// shown as it is: no summary or help note writes a key name, and every token names a
// real binding.
func TestProseNamesKeysByTheirBinding(t *testing.T) {
	t.Parallel()
	var prose []string
	for i := range slashCommands {
		prose = append(prose, slashCommands[i].summary)
	}
	for i := range commands {
		prose = append(prose, commands[i].summary)
	}
	for _, notes := range scopeNotes {
		prose = append(prose, notes...)
	}
	for _, text := range prose {
		outside := liveKeyToken.ReplaceAllString(text, "")
		if m := bareKey.FindString(outside); m != "" {
			t.Errorf("%q names the key %q: write its binding as {section.action}", text, strings.TrimSpace(m))
		}
		for _, token := range liveKeyToken.FindAllString(text, -1) {
			name := token[1 : len(token)-1]
			if !slices.ContainsFunc(keyActions, func(r keyAction) bool { return r.name == name }) {
				t.Errorf("%q names %s, which is no binding", text, token)
			}
		}
	}
}

// A rebound key shows in the prose as it is.
func TestProseFollowsARebinding(t *testing.T) {
	t.Parallel()
	keys := config.DefaultKeys()
	keys.Search.Scope = "w"
	m := sized(t, withRooms(t, newModel())).WithKeys(keys)
	if got := m.liveKeys("{search.scope} widens it"); got != "w widens it" {
		t.Fatalf("liveKeys = %q", got)
	}
	keys.Search.Scope = unbind
	m = sized(t, withRooms(t, newModel())).WithKeys(keys)
	if got := m.liveKeys("{search.scope} widens it"); got != "(unbound) widens it" {
		t.Fatalf("unbound: liveKeys = %q", got)
	}
}
