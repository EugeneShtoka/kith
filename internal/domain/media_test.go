package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func said() domain.DownloadPlace {
	return domain.DownloadPlace{RoomID: "!standup:x", Space: "Work", Sender: "@dana:x"}
}

// The ladder, rung by rung. It is notify.Rule's, deliberately: more constraints beat
// fewer, and among equals the narrower place wins.
func TestSpeedResolvesNarrowestFirst(t *testing.T) {
	t.Parallel()

	var rules []domain.MediaRule
	for _, step := range []struct {
		add  domain.MediaRule
		want float64
		why  string
	}{
		{domain.MediaRule{}, 1, "nothing named this place, so the setting above stands"},
		{domain.MediaRule{Match: "Work", Speed: new(1.25)}, 1.25, "the space it is in"},
		{domain.MediaRule{Sender: "@dana:x", Speed: new(1.5)}, 1.5, "her anywhere beats the space"},
		{domain.MediaRule{Match: "!standup:x", Speed: new(1.75)}, 1.75, "this room beats her anywhere"},
		{domain.MediaRule{Match: "Work", Sender: "@dana:x", Speed: new(float64(2))}, 2, "her in the space beats the room"},
		{domain.MediaRule{Match: "!standup:x", Sender: "@dana:x", Speed: new(2.5)}, 2.5, "her in this room wins outright"},
	} {
		if step.add != (domain.MediaRule{}) {
			rules = append(rules, step.add)
		}
		if got := domain.ResolveMedia(true, true, 1, rules, said()).Speed; got != step.want {
			t.Errorf("speed = %v, want %v — %s", got, step.want, step.why)
		}
	}
}

// A rule naming someone else does not apply, however narrowly it names the place.
func TestSpeedIgnoresSomebodyElsesRule(t *testing.T) {
	t.Parallel()

	rules := []domain.MediaRule{
		{Match: "!standup:x", Sender: "@someone:x", Speed: new(float64(3))},
		{Match: "Work", Speed: new(1.25)},
	}
	if got := domain.ResolveMedia(true, true, 1, rules, said()).Speed; got != 1.25 {
		t.Errorf("speed = %v, want the space's 1.25 — the room rule names another person", got)
	}
}

// A rule states only what it changes: a speed rule must not quietly turn caching off
// by being the rule that won.
func TestASpeedRuleChangesOnlyTheSpeed(t *testing.T) {
	t.Parallel()

	rules := []domain.MediaRule{{Match: "!standup:x", Speed: new(float64(2))}}
	got := domain.ResolveMedia(true, true, 1, rules, said())
	if !got.Auto || !got.Cache {
		t.Errorf("ResolveMedia = %+v, want auto and cache inherited", got)
	}
}

// Where two rules are equally specific the last one wins.
func TestTheLastEquallySpecificRuleWins(t *testing.T) {
	t.Parallel()

	rules := []domain.MediaRule{
		{Match: "!standup:x", Speed: new(1.5)},
		{Match: "!standup:x", Speed: new(float64(2))},
	}
	if got := domain.ResolveMedia(true, true, 1, rules, said()).Speed; got != 2 {
		t.Errorf("speed = %v, want the later rule's 2", got)
	}
}

// A rule constrained by nothing is not a rule here.
func TestARuleNamingNothingDoesNotApply(t *testing.T) {
	t.Parallel()

	rules := []domain.MediaRule{{Speed: new(float64(4))}}
	if got := domain.ResolveMedia(true, true, 1, rules, said()).Speed; got != 1 {
		t.Errorf("speed = %v, want the setting above to stand", got)
	}
}

// The same ladder decides where a file is saved, because it is the same question about
// the same places — a person's attachments can have their own folder.
func TestDownloadRulesUseTheSameLadder(t *testing.T) {
	t.Parallel()

	rules := []domain.DownloadRule{
		{Match: "Work", Dir: "/work"},
		{Sender: "@dana:x", Dir: "/dana"},
		{Match: "!standup:x", Dir: "/standup"},
	}
	place := said()
	place.Name = "note.ogg"
	if got := domain.ResolveDownload("/dl", "", rules, place); got.Dir != "/standup" {
		t.Errorf("dir = %q, want the room to beat her-anywhere and the space", got.Dir)
	}
	rules = append(rules, domain.DownloadRule{Match: "!standup:x", Sender: "@dana:x", Dir: "/dana-standup"})
	if got := domain.ResolveDownload("/dl", "", rules, place); got.Dir != "/dana-standup" {
		t.Errorf("dir = %q, want her-in-this-room to win outright", got.Dir)
	}
}

// Rules match the MXID, never the displayed name: a display name is something anyone
// can change to anything, which makes it a poor thing to key a setting on.
func TestRulesMatchTheMXIDNotTheName(t *testing.T) {
	t.Parallel()

	place := said()
	place.Person = "Dana"
	rules := []domain.MediaRule{{Sender: "Dana", Speed: new(float64(3))}}
	if got := domain.ResolveMedia(true, true, 1, rules, place).Speed; got != 1 {
		t.Errorf("speed = %v, want a display name not to match a sender rule", got)
	}
}
