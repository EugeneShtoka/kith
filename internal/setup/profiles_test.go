package setup_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// A config with no [[profile]] has one, default, holding every room; written ones keep
// their order.
func TestThereIsAlwaysAProfile(t *testing.T) {
	t.Parallel()
	got, err := setup.Profiles(valid())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != domain.DefaultProfile || !got[0].Rooms.Admits(domain.RoomFacts{ID: mx + "!any:x"}) {
		t.Errorf("Profiles = %+v, want default alone, holding every room", got)
	}
	cfg := valid()
	cfg.Profiles = []config.Profile{{Name: "work", Include: []string{"space:Work"}}, {Name: " phone "}}
	got, err = setup.Profiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "work" || got[1].Name != "phone" {
		t.Errorf("Profiles = %+v, want work then phone", got)
	}
}

// A profile names rooms, so a tag whose rule reads a room's state cannot name them,
// however it is reached; a name is unique in any case, and not empty.
func TestAProfileIsRefusedWhatItCannotMean(t *testing.T) {
	t.Parallel()
	tags := []config.Tag{
		{Name: "Pinned"},
		{Name: "Unread", Rule: []string{"unread"}},
		{Name: "Quiet", Rule: []string{"not mention"}},
		{Name: "Busy", Rule: []string{"dm", "tag:Unread"}},
		{Name: "Loop", Rule: []string{"tag:Loop2"}},
		{Name: "Loop2", Rule: []string{"tag:Loop", "draft"}},
		{Name: "Family", Rule: []string{"dm", "not tag:Pinned"}},
	}
	tests := map[string]struct {
		profiles []config.Profile
		refusal  string // "" when accepted
	}{
		"a state tag":             {[]config.Profile{{Name: "p", Include: []string{"tag:Unread"}}}, `"tag:Unread" reads whether`},
		"a negated state word":    {[]config.Profile{{Name: "p", Include: []string{"tag:Quiet"}}}, `"tag:Quiet" reads whether`},
		"through another tag":     {[]config.Profile{{Name: "p", Exclude: []string{"tag:Busy"}}}, `"tag:Busy" reads whether`},
		"in a cycle":              {[]config.Profile{{Name: "p", Include: []string{"tag:Loop2"}}}, `"tag:Loop2" reads whether`},
		"only through a cycle":    {[]config.Profile{{Name: "p", Include: []string{"tag:Loop"}}}, ""},
		"as a row":                {[]config.Profile{{Name: "p", Include: []string{"place:unread"}}}, `"place:unread" reads whether`},
		"a place tag":             {[]config.Profile{{Name: "p", Include: []string{"tag:Family", "place:Pinned"}}}, ""},
		"a space named like one":  {[]config.Profile{{Name: "p", Include: []string{"space:Unread"}}}, ""},
		"a missing tag":           {[]config.Profile{{Name: "p", Include: []string{"tag:Nope"}}}, `"tag:Nope" names no [[tag]]`},
		"an empty name":           {[]config.Profile{{Name: " "}}, "profile 1: name is empty"},
		"a name twice":            {[]config.Profile{{Name: "Work"}, {Name: "work "}}, `profile "work" is defined twice`},
		"a bare Matrix room":      {[]config.Profile{{Name: "p", Include: []string{"!abc:example.org"}}}, `profile "p": include`},
		"entries in the profiles": {[]config.Profile{{Name: "p"}, {Name: "q", Exclude: []string{"tag:Unread"}}}, `profile "q": exclude`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := valid()
			cfg.Tags = tags
			cfg.Profiles = tc.profiles
			err := setup.Validate(cfg)
			switch {
			case tc.refusal == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
				t.Errorf("Validate = %v, want a refusal holding %q", err, tc.refusal)
			}
		})
	}
}
