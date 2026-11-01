package daemon

import (
	"strings"
	"testing"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
)

// settingGroups are the TUI's settings groups (tui.settingGroups' keys), which the
// desktop lists the settings under.
var settingGroups = map[string]bool{
	"notifications": true, "names": true, "display": true, "rooms": true, "composer": true, "media": true,
	"emoji": true, "look": true, "spelling": true, "completion": true, "assist": true, "codes": true,
	"filters": true, "agent": true, "networks": true, "advanced": true,
}

// Every property of the bridge's file is either settable one at a time or excluded,
// never both, so a property added to the config is set from a window only once someone
// decides it is; every settable one is offered in a kind the call carries, under a
// group, with what its kind needs and nothing it does not; and neither list names what
// the config does not have.
func TestEveryBridgePropertyIsClassified(t *testing.T) {
	t.Parallel()
	bridge := map[string]config.Property{}
	excludes := map[string]int{}
	for _, p := range config.Properties() {
		if p.File != config.BridgeFile {
			continue
		}
		bridge[p.Path] = p
		s, isSettable := settable[p.Path]
		var by []string
		for _, e := range excluded {
			if strings.HasPrefix(p.Path, e.prefix) {
				by = append(by, e.prefix)
				excludes[e.prefix]++
			}
		}
		switch {
		case isSettable && len(by) > 0:
			t.Errorf("%s is settable and excluded (by %v)", p.Path, by)
		case !isSettable && len(by) == 0:
			t.Errorf("%s is neither settable nor excluded: decide whether a window may set it", p.Path)
		case len(by) > 1:
			t.Errorf("%s is excluded twice (by %v)", p.Path, by)
		}
		if !isSettable {
			continue
		}
		kind := s.kind(p)
		number := kind == v1.ConfigSettingKind_CONFIG_SETTING_KIND_NUMBER
		switch {
		case kind == v1.ConfigSettingKind_CONFIG_SETTING_KIND_UNSPECIFIED:
			t.Errorf("%s is settable, but the call carries no value of its kind (%d)", p.Path, p.Kind)
		case !settingGroups[s.group] || s.label == "":
			t.Errorf("%s is settable under group %q, label %q", p.Path, s.group, s.label)
		case (len(s.choices) > 0 || s.duration) && p.Kind != config.PropertyText:
			t.Errorf("%s has choices or is a duration, but holds no text", p.Path)
		case len(s.choices) > 0 && s.duration:
			t.Errorf("%s is both a choice and a duration", p.Path)
		case (s.least != nil || s.most != nil) && !number:
			t.Errorf("%s is bounded, but is no number", p.Path)
		case s.least != nil && s.most != nil && *s.least > *s.most:
			t.Errorf("%s's bounds hold nothing", p.Path)
		}
		for _, c := range s.choices {
			if strings.TrimSpace(c.value) == "" {
				t.Errorf("%s offers an empty choice", p.Path)
			}
		}
	}
	for path := range settable {
		if _, ok := bridge[path]; !ok {
			t.Errorf("settable names %s, which is no property of the bridge's file", path)
		}
	}
	for _, e := range excluded {
		if excludes[e.prefix] == 0 {
			t.Errorf("excluded names %s, which matches no property of the bridge's file", e.prefix)
		}
		if strings.TrimSpace(e.why) == "" {
			t.Errorf("excluded names %s without saying why", e.prefix)
		}
	}
}
