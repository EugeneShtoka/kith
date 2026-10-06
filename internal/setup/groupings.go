package setup

import (
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ApplyGroupings is cfg with each grouping copied into its tag as chosen: a tag made
// for a grouping no tag is named after, and, among owner's rooms only, the grouping's
// rooms put in (MergeIn, TakeNetworks) and the rest taken out (TakeNetworks), each
// written as filing a room by hand writes it (TagSet.Filed). rooms are every room's
// facts; the tags are judged on them as places.
func ApplyGroupings(cfg config.Config, owner domain.RoomOwner, groupings []domain.Grouping,
	choices map[string]domain.GroupingChoice, rooms []domain.RoomFacts,
) (config.Config, error) {
	out := cfg.Clone()
	for _, g := range groupings {
		choice := choices[g.Name]
		if choice == domain.KeepTags {
			continue
		}
		if configTag(out, g.Name) < 0 {
			out.Tags = append(out.Tags, config.Tag{Name: g.Name})
		}
		for _, r := range rooms {
			if !owner.Owns(domain.RoomID(r.ID)) {
				continue
			}
			in := containsID(g.Rooms, r.ID)
			if !in && choice != domain.TakeNetworks {
				continue
			}
			tags, _, err := Tags(out)
			if err != nil {
				return config.Config{}, err
			}
			i, _ := tags.Index(g.Name)
			if tags.HasAt(i, r, domain.RoomState{}) == in {
				continue
			}
			at := configTag(out, g.Name)
			out.Tags[at].Picked, out.Tags[at].Excluded = tags.Filed(i, r, domain.RoomState{}, in, rooms)
		}
	}
	return out, nil
}

// configTag is the index of the [[tag]] named name (case-insensitive), -1 for none.
func configTag(cfg config.Config, name string) int {
	for i, t := range cfg.Tags {
		if strings.EqualFold(strings.TrimSpace(t.Name), strings.TrimSpace(name)) {
			return i
		}
	}
	return -1
}

func containsID(rooms []domain.RoomID, id string) bool {
	for _, r := range rooms {
		if string(r) == id {
			return true
		}
	}
	return false
}
