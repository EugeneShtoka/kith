package domain_test

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Of two values of one verdict, a release wins over a move whenever made; otherwise the
// later wins, so a clearing made after a release ends it; the same verdict made twice is
// the same, and of two made in one millisecond every bridge picks alike.
func TestAVerdictsValueThatWins(t *testing.T) {
	t.Parallel()
	at := time.UnixMilli(1_000_000)
	move := func(ms int64, rule domain.MoveRule) domain.TagVerdict {
		return domain.TagVerdict{Tag: "spam", Rule: rule, Filter: "f", At: at.Add(time.Duration(ms) * time.Millisecond)}
	}
	release := func(ms int64) domain.TagVerdict {
		return domain.TagVerdict{Tag: "spam", Released: true, At: at.Add(time.Duration(ms) * time.Millisecond)}
	}
	cleared := func(ms int64) domain.TagVerdict {
		return domain.TagVerdict{Tag: "spam", At: at.Add(time.Duration(ms) * time.Millisecond)}
	}
	for _, c := range []struct {
		name string
		win  domain.TagVerdict
		lose domain.TagVerdict
	}{
		{"a release over a later move", release(0), move(5, domain.MoveAlways)},
		{"a later move over a move", move(5, domain.MoveAlways), move(0, domain.MoveMostly)},
		{"a later clearing over a release", cleared(5), release(0)},
		{"a later release over a clearing", release(5), cleared(0)},
		{"a later clearing over a move", cleared(5), move(0, domain.MoveAlways)},
		{"in one millisecond, the higher rule", move(0, domain.MoveMostly), move(0, domain.MoveAlways)},
		{"in one millisecond, a release over a clearing", release(0), cleared(0)},
	} {
		if !c.win.Beats(c.lose) || c.lose.Beats(c.win) {
			t.Errorf("%s: %+v should win over %+v, alone", c.name, c.win, c.lose)
		}
	}
	if a, b := move(0, domain.MoveAlways), move(9, domain.MoveAlways); !a.Same(b) {
		t.Error("one move made twice is not the same")
	}
	if a, b := release(0), (domain.TagVerdict{Tag: "spam", Released: true, Rule: domain.MoveAlways, At: at}); !a.Same(b) || a.Beats(b) || b.Beats(a) {
		t.Error("a release whatever rule it carries is not one release")
	}
}

// A retag moves what each room and person holds under the old name as Retagged does
// (a release wins over the new name's move), and clears the old name at the retag, or
// just after an entry made later by a clock ahead.
func TestARetagOfTheMeshsVerdicts(t *testing.T) {
	t.Parallel()
	at := time.UnixMilli(1_000_000)
	ahead := at.Add(time.Hour)
	mv := domain.MeshVerdicts{
		Tags: []domain.TagVerdict{
			{Room: "tg:1", Tag: "spam", Rule: domain.MoveAlways, At: at.Add(-time.Minute)},
			{Room: "tg:2", Tag: "spam", Released: true, At: ahead},
			{Room: "tg:2", Tag: "junk", Rule: domain.MoveAlways, At: at.Add(-time.Minute)},
			{Room: "tg:3", Tag: "work", Rule: domain.MoveAlways, At: at},
		},
		People: []domain.PersonVerdict{{Person: "tg:555", Tag: "spam", Rule: domain.MoveSenderFirst, At: at.Add(-time.Minute)}},
	}
	got := mv.Retag("spam", "Junk", at)
	want := []domain.TagVerdict{
		{Room: "tg:1", Tag: "spam", At: at},
		{Room: "tg:1", Tag: "junk", Rule: domain.MoveAlways, At: at.Add(-time.Minute)},
		{Room: "tg:2", Tag: "spam", At: ahead.Add(time.Millisecond)},
		{Room: "tg:2", Tag: "junk", Released: true, At: ahead},
	}
	if len(got.Tags) != len(want) {
		t.Fatalf("Retag = %+v, want %+v", got.Tags, want)
	}
	for i := range want {
		if g := got.Tags[i]; g.Room != want[i].Room || g.Tag != want[i].Tag || !g.Same(want[i]) || !g.At.Equal(want[i].At) {
			t.Errorf("Retag[%d] = %+v, want %+v", i, g, want[i])
		}
	}
	if p := got.People; len(p) != 2 || p[0].In() || p[1].Tag != "junk" || !p[1].In() {
		t.Errorf("Retag's people = %+v, want spam cleared and junk marked", p)
	}
	if deleted := mv.Retag("spam", "", at); len(deleted.Tags) != 2 || deleted.Tags[0].In() || deleted.Tags[1].Released {
		t.Errorf("a deletion = %+v, want spam cleared alone", deleted.Tags)
	}
}
