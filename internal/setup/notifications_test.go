package setup_test

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The defaults produce one rule: the account-wide one, at mention.
func TestNotificationRulesDefaults(t *testing.T) {
	t.Parallel()

	rules, err := setup.NotificationRules(config.Notifications{Enabled: true})
	if err != nil {
		t.Fatalf("NotificationRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want just the account-wide rule", rules)
	}
	if rules[0].Match != "" || rules[0].Sender != "" {
		t.Errorf("rule = %+v, want it scoped to nothing, which is what account-wide means", rules[0])
	}
	if rules[0].Show == nil || *rules[0].Show != notify.LevelMention {
		t.Errorf("Show = %v, want the shipped mention level", rules[0].Show)
	}
	if rules[0].When != nil {
		t.Error("no quiet hours configured should mean no schedule")
	}
}

// A rule leaves unset fields unset, so it changes one thing without restating the rest.
func TestRulesLeaveUnsetFieldsAlone(t *testing.T) {
	t.Parallel()

	rules, err := setup.NotificationRules(config.Notifications{
		Enabled: true,
		Rules: []config.Rule{
			{Name: "Standup", Match: "!standup:x", Sender: "@alice:x", Show: "all"},
			{Match: "Work", Sound: "/s/boss.wav"},
			{Match: "Night", Show: "none", When: "22:00-08:00", Ring: "none"},
		},
	})
	if err != nil {
		t.Fatalf("NotificationRules: %v", err)
	}
	if len(rules) != 4 {
		t.Fatalf("rules = %+v, want the global rule plus three", rules)
	}

	first := rules[1]
	if first.Name != "Standup" || first.Match != "!standup:x" || first.Sender != "@alice:x" {
		t.Errorf("rule = %+v, want the name and scope carried through", first)
	}
	if first.Show == nil || *first.Show != notify.LevelAll {
		t.Errorf("Show = %v, want all", first.Show)
	}
	if first.Ring != nil || first.When != nil || first.Sound != "" {
		t.Errorf("rule = %+v, want everything it did not set left unset", first)
	}

	second := rules[2]
	if second.Sound != "/s/boss.wav" {
		t.Errorf("rule = %+v, want the sound", second)
	}
	if second.Show != nil {
		t.Error("a rule that set no level should leave the level alone")
	}

	third := rules[3]
	if third.When == nil || third.When.String() != "22:00-08:00" {
		t.Errorf("When = %v, want the configured window", third.When)
	}
	if third.Ring == nil || *third.Ring != notify.LevelNone {
		t.Errorf("Ring = %v, want none", third.Ring)
	}
}

// A misspelling is refused, naming the rule and the field.
func TestNotificationRulesRejectBadSpellings(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		rule config.Rule
		want string
	}{
		"show":   {config.Rule{Match: "x", Show: "everything"}, "show"},
		"ring":   {config.Rule{Match: "x", Ring: "loud"}, "ring"},
		"when":   {config.Rule{Match: "x", When: "22:00 to 08:00"}, "when"},
		"thread": {config.Rule{Match: "x", Thread: "mine"}, "thread"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := setup.NotificationRules(config.Notifications{Rules: []config.Rule{tc.rule}})
			if err == nil {
				t.Fatalf("NotificationRules(%+v) should have errored", tc.rule)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "rule[0]") {
				t.Errorf("error %q should name the rule and the field %q", err, tc.want)
			}
		})
	}
	if err := setup.Validate(config.Config{
		Notifications: config.Notifications{Rules: []config.Rule{{Match: "x", Show: "everything"}}},
	}); err == nil {
		t.Error("Validate should carry the same refusal, so main can stop before a login")
	}
}

// Notifications off, or on with no sink, compose to a Nop.
func TestNotifierComposesFromConfig(t *testing.T) {
	t.Parallel()

	if got := setup.NotifierReporting(config.Notifications{Desktop: true}, nil); got != (notify.Nop{}) {
		t.Errorf("notifier = %T, want a Nop while notifications are off", got)
	}
	if got := setup.NotifierReporting(config.Notifications{Enabled: true}, nil); got != (notify.Nop{}) {
		t.Errorf("notifier = %T, want a Nop with no sinks configured", got)
	}
	if got := setup.NotifierReporting(config.Notifications{Enabled: true, Desktop: true}, nil); got == (notify.Nop{}) {
		t.Error("a configured desktop sink should produce something that delivers")
	}
}

// Unset templates take the built-in format.
func TestTemplatesDefault(t *testing.T) {
	t.Parallel()

	tpl := setup.Templates(config.Notifications{})
	if tpl != (notify.Templates{Title: config.DefaultTitleTemplate, Body: config.DefaultBodyTemplate}) {
		t.Errorf("templates = %+v, want the built-in format", tpl)
	}
	custom := setup.Templates(config.Notifications{Title: "{room}", Body: "{sender}: {body}"})
	if custom.Title != "{room}" || custom.Body != "{sender}: {body}" {
		t.Errorf("templates = %+v, want the configured ones", custom)
	}
}

// Configured rules layer on the built-in account-wide rule rather than replacing it,
// and an unscoped configured rule sets the account-wide level.
func TestAnUnscopedRuleSetsTheAccountWideLevel(t *testing.T) {
	t.Parallel()

	rules, err := setup.NotificationRules(config.Notifications{
		Enabled: true,
		Rules:   []config.Rule{{Name: "everything", Show: "all"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || *rules[0].Show != notify.LevelMention {
		t.Fatalf("rules = %+v, want the built-in rule first, then the configured one", rules)
	}
	got := notify.Resolve(rules, notify.Scope{}, time.Now())
	out := got.Decide(notify.Event{})
	if got.Show != notify.LevelAll || !out.Notify || out.Silent || out.By.Name != "everything" {
		t.Errorf("resolved %+v / %+v, want every message shown with sound, by the configured rule", got, out)
	}
}
