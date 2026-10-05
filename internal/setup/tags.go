package setup

import (
	"fmt"
	"strings"

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

// archiveNetworks is the network each [<network>.archive] section is.
var archiveNetworks = map[string]domain.Protocol{"telegram": domain.ProtocolTelegram, "whatsapp": domain.ProtocolWhatsApp}

// Archives is, for each network whose archive kith follows, the tag that archive is:
// one that exists. Another name that does not exist is an error; the default name,
// when no tag has it (deleted), follows nothing.
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

// archivesCheck is Archives for Validate.
func archivesCheck(cfg config.Config) error {
	tags, _, err := Tags(cfg)
	if err != nil {
		return nil //nolint:nilerr // tagsCheck reports it
	}
	_, err = Archives(cfg, tags)
	return err
}

// Mirrored is, for each network whose archive kith's filing changes too (mirror on),
// the tag that files into it: one that exists.
func Mirrored(cfg config.Config, tags domain.TagSet) map[domain.Protocol]string {
	out := map[domain.Protocol]string{}
	for section, archive := range cfg.Archives() {
		if _, ok := tags.Index(archive.TagName()); ok && archive.Mirror {
			out[archiveNetworks[section]] = archive.TagName()
		}
	}
	return out
}
