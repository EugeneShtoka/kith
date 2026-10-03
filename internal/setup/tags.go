package setup

import (
	"fmt"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Tags is the configured [[tag]]s, parsed, and a warning for each set of tags whose
// rules depend on each other (their references to each other match nothing).
func Tags(cfg config.Config) (domain.TagSet, []string, error) {
	tags := make([]domain.Tag, len(cfg.Tags))
	for i, t := range cfg.Tags {
		tags[i] = domain.Tag{
			Name: t.Name, Rule: t.Rule, Picked: t.Picked, Excluded: t.Excluded, Hidden: t.Hidden,
			Silent: !t.Counts(), Exclusive: t.Exclusive, SpaceExclusive: t.SpaceExclusive,
			Sticky: t.Sticky, HideWhenEmpty: t.HideWhenEmpty, First: t.First, CountInLabel: t.CountInLabel,
		}
	}
	set, warnings, err := domain.NewTagSet(tags)
	if err != nil {
		return domain.TagSet{}, nil, fmt.Errorf("config: [[tag]]: %w", err)
	}
	return set, warnings, nil
}

// tagsCheck is Tags for Validate: a cycle is a warning, not an error.
func tagsCheck(cfg config.Config) error {
	_, _, err := Tags(cfg)
	return err
}
