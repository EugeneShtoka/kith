package setup

import (
	"fmt"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Tags parses the [[tag]]s, warning on each reference cycle (such references match
// nothing).
func Tags(cfg config.Config) (domain.TagSet, []string, error) {
	tags := make([]domain.Tag, len(cfg.Tags))
	for i := range cfg.Tags {
		t := &cfg.Tags[i]
		exclusive, err := tagExclusive(*t)
		if err != nil {
			return domain.TagSet{}, nil, err
		}
		tags[i] = domain.Tag{
			Name: t.Name, Rule: t.Rule, Picked: t.Picked, Excluded: t.Excluded, People: t.People, Hidden: t.Hidden,
			Silent: !t.Counts(), Mute: !t.Notifying(), Exclusive: exclusive,
			Sticky: t.Sticky, HideWhenEmpty: t.HideWhenEmpty, First: t.First, CountInLabel: t.CountInLabel,
		}
	}
	set, warnings, err := domain.NewTagSet(tags)
	if err != nil {
		return domain.TagSet{}, nil, fmt.Errorf("config: [[tag]]: %w", err)
	}
	return set, warnings, nil
}

// tagExclusive reads a tag's exclusive list.
func tagExclusive(t config.Tag) (domain.Exclusive, error) {
	var out domain.Exclusive
	for _, what := range t.Exclusive {
		switch strings.ToLower(strings.TrimSpace(what)) {
		case config.ExclusiveTags:
			out.Tags = true
		case config.ExclusiveSpaces:
			out.Spaces = true
		case config.ExclusiveOrigin:
			out.Origin = true
		default:
			return domain.Exclusive{}, fmt.Errorf("config: [[tag]] %q: exclusive: %q is not one of %s, %s, %s",
				t.Name, what, config.ExclusiveTags, config.ExclusiveSpaces, config.ExclusiveOrigin)
		}
	}
	return out, nil
}

// tagsCheck is Tags for Validate: a cycle is a warning, not an error.
func tagsCheck(cfg config.Config) error {
	_, _, err := Tags(cfg)
	return err
}

// archiveNetworks is the network each [<network>.archive] section is.
var archiveNetworks = map[string]domain.Protocol{"telegram": domain.ProtocolTelegram, "whatsapp": domain.ProtocolWhatsApp}

// Archives maps each network whose archive kith follows to an existing tag. A missing
// custom name is an error; a missing default name follows nothing.
func Archives(cfg config.Config, tags domain.TagSet) (map[domain.Protocol]string, error) {
	out := map[domain.Protocol]string{}
	for section, archive := range cfg.Archives() {
		network := archiveNetworks[section]
		name := archive.TagName()
		if _, ok := tags.Index(name); !ok {
			if !strings.EqualFold(name, config.DefaultArchiveTag) {
				return nil, fmt.Errorf("config: [%s.archive] tag: %q names no [[tag]]", section, archive.Tag)
			}
			continue
		}
		if archive.Follows() {
			out[network] = name
		}
	}
	return out, nil
}

// ArchiveTags is every existing archive tag: each [*.archive] tag and the default.
func ArchiveTags(cfg config.Config, tags domain.TagSet) []string {
	var out []string
	add := func(name string) {
		if _, ok := tags.Index(name); ok && !slices.ContainsFunc(out, func(n string) bool { return strings.EqualFold(n, name) }) {
			out = append(out, name)
		}
	}
	add(config.DefaultArchiveTag)
	for _, archive := range cfg.Archives() {
		add(archive.TagName())
	}
	return out
}

// archivesCheck is Archives for Validate.
func archivesCheck(cfg config.Config) error {
	tags, _, err := Tags(cfg)
	if err != nil {
		return nil //nolint:nilerr // tagsCheck reports it
	}
	_, err = Archives(cfg, tags)
	return err
}

// Mirrored maps each network with archive mirroring on to its existing tag.
func Mirrored(cfg config.Config, tags domain.TagSet) map[domain.Protocol]string {
	out := map[domain.Protocol]string{}
	for section, archive := range cfg.Archives() {
		if _, ok := tags.Index(archive.TagName()); ok && archive.Mirror {
			out[archiveNetworks[section]] = archive.TagName()
		}
	}
	return out
}
