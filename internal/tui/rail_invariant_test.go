package tui

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
)

// staleRail returns how the memoized rail differs from one built afresh from the model's
// state, or nil; an input BuildRail reads that the memo's key does not hold shows up here.
// It never fills the memo.
func staleRail(m Model) error {
	v := m.unreadView()
	memo := v.railMemo
	if memo == nil || !memo.ok || memo.key != v.railKey() {
		return nil // the next read rebuilds it
	}
	fresh := v.buildRail()
	if err := sameBuilt(memo.built, fresh); err != nil {
		return fmt.Errorf("rail memo: %w", err)
	}
	for _, row := range fresh.rail.Rows {
		held := memo.holds[railRowRef{key: row.Key, space: row.Space}]
		if got := slices.Sorted(maps.Keys(held)); !slices.Equal(got, slices.Sorted(slices.Values(row.Rooms))) {
			return fmt.Errorf("rail memo: row %q holds %v, but its rooms are %v", row.Key, got, row.Rooms)
		}
	}
	return nil
}

// sameBuilt reports how two built rails differ: their rows and judgments, or the spec
// ShownRows reads (the tags compared as configured; a TagSet holds funcs).
func sameBuilt(memo, fresh builtRail) error {
	if !reflect.DeepEqual(memo.rail, fresh.rail) {
		return fmt.Errorf("the rail is\n%+v\nbut the state builds\n%+v", memo.rail, fresh.rail)
	}
	m, f := memo.spec, fresh.spec
	if !slices.Equal(m.Order, f.Order) || !slices.Equal(m.Hidden, f.Hidden) || !slices.Equal(m.HideWhenEmpty, f.HideWhenEmpty) ||
		!maps.Equal(m.Labels, f.Labels) || !reflect.DeepEqual(m.Tags.Tags(), f.Tags.Tags()) {
		return fmt.Errorf("built from %+v, but the state is %+v", m, f)
	}
	return nil
}
