package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func said() domain.DownloadPlace {
	return domain.DownloadPlace{RoomID: "!standup:x", Space: "Work", Sender: "@dana:x"}
}

// The ladder, rung by rung (shared with notify.Rule): more constraints beat fewer,
// then the narrower place.
func TestSpeedResolvesNarrowestFirst(t *testing.T) {
	t.Parallel()

	var rules []domain.MediaRule
	for _, step := range []struct {
		add  domain.MediaRule
		want float64
		why  string
	}{
		{domain.MediaRule{}, 1, "nothing named this place, so the setting above stands"},
		{domain.MediaRule{Match: "space:Work", Speed: new(1.25)}, 1.25, "the space it is in"},
		{domain.MediaRule{Sender: "@dana:x", Speed: new(1.5)}, 1.5, "her anywhere beats the space"},
		{domain.MediaRule{Match: "!standup:x", Speed: new(1.75)}, 1.75, "this room beats her anywhere"},
		{domain.MediaRule{Match: "space:Work", Sender: "@dana:x", Speed: new(float64(2))}, 2, "her in the space beats the room"},
		{domain.MediaRule{Match: "!standup:x", Sender: "@dana:x", Speed: new(2.5)}, 2.5, "her in this room wins outright"},
	} {
		if step.add != (domain.MediaRule{}) {
			rules = append(rules, step.add)
		}
		if got := domain.ResolveMedia(domain.MediaPolicy{Auto: true, Cache: true, Speed: 1}, rules, said()).Speed; got != step.want {
			t.Errorf("speed = %v, want %v — %s", got, step.want, step.why)
		}
	}
}

// A rule naming someone else does not apply, however narrowly it names the place.
func TestSpeedIgnoresSomebodyElsesRule(t *testing.T) {
	t.Parallel()

	rules := []domain.MediaRule{
		{Match: "!standup:x", Sender: "@someone:x", Speed: new(float64(3))},
		{Match: "space:Work", Speed: new(1.25)},
	}
	if got := domain.ResolveMedia(domain.MediaPolicy{Auto: true, Cache: true, Speed: 1}, rules, said()).Speed; got != 1.25 {
		t.Errorf("speed = %v, want the space's 1.25 — the room rule names another person", got)
	}
}

// A rule changes only the fields it sets.
func TestASpeedRuleChangesOnlyTheSpeed(t *testing.T) {
	t.Parallel()

	rules := []domain.MediaRule{{Match: "!standup:x", Speed: new(float64(2))}}
	got := domain.ResolveMedia(domain.MediaPolicy{Auto: true, Cache: true, Speed: 1}, rules, said())
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
	if got := domain.ResolveMedia(domain.MediaPolicy{Auto: true, Cache: true, Speed: 1}, rules, said()).Speed; got != 2 {
		t.Errorf("speed = %v, want the later rule's 2", got)
	}
}

// A rule constrained by nothing matches nothing.
func TestARuleNamingNothingDoesNotApply(t *testing.T) {
	t.Parallel()

	rules := []domain.MediaRule{{Speed: new(float64(4))}}
	if got := domain.ResolveMedia(domain.MediaPolicy{Auto: true, Cache: true, Speed: 1}, rules, said()).Speed; got != 1 {
		t.Errorf("speed = %v, want the setting above to stand", got)
	}
}

// The same ladder decides where a file is saved.
func TestDownloadRulesUseTheSameLadder(t *testing.T) {
	t.Parallel()

	rules := []domain.DownloadRule{
		{Match: "space:Work", Dir: "/work"},
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

// Rules match the MXID, never the display name, which anyone can change.
func TestRulesMatchTheMXIDNotTheName(t *testing.T) {
	t.Parallel()

	place := said()
	place.Person = "Dana"
	rules := []domain.MediaRule{{Sender: "Dana", Speed: new(float64(3))}}
	if got := domain.ResolveMedia(domain.MediaPolicy{Auto: true, Cache: true, Speed: 1}, rules, place).Speed; got != 1 {
		t.Errorf("speed = %v, want a display name not to match a sender rule", got)
	}
}

// A sent attachment's neutral name says what it is and keeps its extension, lower
// case, so it still opens; nothing of its own name travels with it.
func TestSentName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"IMG_2041 Haifa.JPG":     "photo.jpg",
		"scan.jpeg":              "photo.jpeg",
		"holiday.mkv":            "video.mkv",
		"talk.MPEG":              "video.mpeg",
		"song by Dana.mp3":       "music.mp3",
		"contract v3.docx":       "document.docx",
		"report.pdf":             "document.pdf",
		"backup.7z":              "archive.7z",
		"photos.zip":             "archive.zip",
		"old.rar":                "archive.rar",
		"site-2026.tar.gz":       "archive.tar.gz",
		"model.stl":              "file.stl",
		"README":                 "file",
		".bashrc":                "file",
		"/home/dana/x/plan.pdf":  "document.pdf",
		`C:\Users\dana\cv.docx`:  "document.docx",
		"no.ext.dots.unknownext": "file.unknownext",
	} {
		if got := domain.SentName(in); got != want {
			t.Errorf("SentName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Anonymizing holds unless the narrowest rule for the place turns it off.
func TestAnonymizeResolvesNarrowestFirst(t *testing.T) {
	t.Parallel()
	off, on := false, true
	place := domain.DownloadPlace{RoomID: "!standup:server", Space: "Work"}
	defaults := domain.MediaPolicy{Anonymize: true}
	for _, tc := range []struct {
		rules []domain.MediaRule
		want  bool
		why   string
	}{
		{nil, true, "nothing says otherwise"},
		{[]domain.MediaRule{{Match: "space:Work", Anonymize: &off}}, false, "the space turns it off"},
		{[]domain.MediaRule{{Match: "space:Work", Anonymize: &off}, {Match: "!standup:server", Anonymize: &on}}, true, "the room, narrower, turns it back on"},
		{[]domain.MediaRule{{Match: "space:Work", Speed: new(float64)}}, true, "a rule about something else leaves it"},
	} {
		if got := domain.ResolveMedia(defaults, tc.rules, place).Anonymize; got != tc.want {
			t.Errorf("%s: anonymize = %v, want %v", tc.why, got, tc.want)
		}
	}
}
