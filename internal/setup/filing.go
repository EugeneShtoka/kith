package setup

import (
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// FileTags is cfg with a room filed into or out of tags as filings say: each tag's
// picked and excluded lists rewritten by domain.TagSet.Filed, judged by tags (the
// profile's view of cfg's) on the room's facts and its state in each tag. others are
// the other rooms' facts: an entry naming the room and others too keeps the others.
// A tag holds what its rule says plus its picked rooms, less its excluded ones, so these
// two lists are all filing writes.
func FileTags(cfg config.Config, tags domain.TagSet, facts domain.RoomFacts, state func(tag int) domain.RoomState,
	filings []domain.TagFiling, others []domain.RoomFacts,
) config.Config {
	out := cfg.Clone()
	for _, f := range filings {
		name := tags.At(f.Tag).Name
		for j := range out.Tags {
			if strings.EqualFold(strings.TrimSpace(out.Tags[j].Name), name) {
				out.Tags[j].Picked, out.Tags[j].Excluded = tags.Filed(f.Tag, facts, state(f.Tag), f.In, others)
			}
		}
	}
	return out
}
