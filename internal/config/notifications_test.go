package config

import "testing"

// A device plays the most specific matching rule it gives a sound, passing over a rule
// it gives none, else its default.
func TestADeviceSoundsTheMostSpecificRuleItNames(t *testing.T) {
	t.Parallel()
	matched := []string{"boss", "work", "all rooms"}
	for _, c := range []struct {
		name   string
		sounds map[string]string
		want   string
	}{
		{"the most specific", map[string]string{"boss": "/b", "work": "/w"}, "/b"},
		{"a rule it names no sound for passed over", map[string]string{"work": "/w", "all rooms": "/a"}, "/w"},
		{"an empty sound passed over", map[string]string{"boss": "", "work": "/w"}, "/w"},
		{"none named: the default", map[string]string{"other": "/o"}, "/d"},
	} {
		n := DeviceNotifications{Sound: "/d", Sounds: c.sounds}
		if got := n.SoundFor(matched); got != c.want {
			t.Errorf("%s: SoundFor = %q, want %q", c.name, got, c.want)
		}
	}
	if got := (DeviceNotifications{}).SoundFor(matched); got != "" {
		t.Errorf("no sounds at all: %q, want silence", got)
	}
}

// A name a rule already has is numbered, past every number taken.
func TestARuleNameIsMadeUnique(t *testing.T) {
	t.Parallel()
	rules := []Rule{{Name: "dm"}, {Name: "dm 2"}, {Name: "x"}}
	if got := UniqueRuleName("dm", rules); got != "dm 3" {
		t.Errorf("UniqueRuleName = %q, want dm 3", got)
	}
	if got := UniqueRuleName("y", rules); got != "y" {
		t.Errorf("UniqueRuleName = %q, want y", got)
	}
}
