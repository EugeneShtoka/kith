package daemon

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A rail built while what it is built from changed (a reload landing during a Rail call)
// is served to the call that built it, but never taken for current: the next call builds
// again. Nor does a build that started earlier replace one installed later.
func TestARailBuiltWhileItsInputsChangedIsNotCurrent(t *testing.T) {
	t.Parallel()
	c := newRailCache()
	stale := domain.ServedRail{Rows: []domain.RailRow{{Key: "tag:Old"}}}
	fresh := domain.ServedRail{Rows: []domain.RailRow{{Key: "tag:New"}}}

	_, _, began := c.current("p")
	c.bump() // the reload lands while the build reads
	if _, changed := c.install("p", began, stale); !changed {
		t.Fatal("the first rail of a profile was not told as a change")
	}
	if _, current, _ := c.current("p"); current {
		t.Fatal("a rail built before a change is taken for current")
	}

	_, _, now := c.current("p")
	served, changed := c.install("p", now, fresh)
	if !changed || served.Rows[0].Key != "tag:New" {
		t.Fatalf("the rail built after the change: %+v, changed %v", served, changed)
	}
	if got, changed := c.install("p", began, stale); changed || got.Rows[0].Key != "tag:New" {
		t.Errorf("an earlier build replaced a later one: %+v, changed %v", got, changed)
	}
	if got, current, _ := c.current("P"); !current || got.Revision != served.Revision {
		t.Errorf("the profile by another case: %+v current %v, want the later rail", got, current)
	}
}
