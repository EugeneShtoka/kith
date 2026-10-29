package setup

import (
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// RailSpec is what a rail is built from (domain.BuildRail): the tags and [display.rail]
// as a profile has them (ProfileConfig), and the labels [[display.name]] gives rows.
func RailSpec(tags domain.TagSet, rail config.Rail, names []config.DisplayName) domain.RailSpec {
	return domain.RailSpec{
		Tags:          tags,
		Order:         rail.Order,
		Hidden:        rail.Hidden,
		HideWhenEmpty: rail.HideWhenEmpty,
		Labels:        config.Display{Names: names}.SpaceLabels(),
	}
}
