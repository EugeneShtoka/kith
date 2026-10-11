package setup

import (
	"fmt"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// `[spam]` rules, derived in one place so the daemon (on arrival) and the client
// (previewing a filter) agree.

// DefaultSpamWindow is the ratio rule's default counting window.
const DefaultSpamWindow = 7 * 24 * time.Hour

// DefaultSpamFloor is the minimum message count before the ratio rule applies.
const DefaultSpamFloor = 5

// SpamRules validates `[spam]` into domain rules.
func SpamRules(cfg config.Spam) (domain.SpamRules, error) {
	out := domain.SpamRules{
		FirstMessage: cfg.FirstMessageRule(),
		Direct:       cfg.DirectRule(),
		Ratio:        cfg.Ratio,
		Floor:        cfg.Floor,
		Window:       DefaultSpamWindow,
	}
	filters, err := spamFilters(cfg.Filters)
	if err != nil {
		return domain.SpamRules{}, err
	}
	out.Filters = filters
	if out.Ratio < 0 || out.Ratio > 1 {
		return domain.SpamRules{}, fmt.Errorf(
			"spam.ratio: %v is not a share between 0 and 1 (0.6 is three messages in five)", out.Ratio)
	}
	if out.Floor < 0 {
		return domain.SpamRules{}, fmt.Errorf("spam.floor: %d is not a number of messages", out.Floor)
	}
	if out.Floor == 0 {
		out.Floor = DefaultSpamFloor
	}
	if spelled := strings.TrimSpace(cfg.Window); spelled != "" {
		window, perr := time.ParseDuration(spelled)
		if perr != nil {
			return domain.SpamRules{}, fmt.Errorf("spam.window: %q is not a duration (try \"168h\")", spelled)
		}
		if window <= 0 {
			return domain.SpamRules{}, fmt.Errorf("spam.window: %q must be positive", spelled)
		}
		out.Window = window
	}
	return out, nil
}

// spamFilters checks each filter can do what its name claims.
func spamFilters(filters []config.SpamFilter) ([]domain.SpamFilter, error) {
	out := make([]domain.SpamFilter, 0, len(filters))
	for i := range filters {
		name := strings.TrimSpace(filters[i].Name)
		if name == "" {
			return nil, fmt.Errorf("spam.filter[%d]: needs a name — it is what the answer "+
				"to \"why is this room in Spam\" says", i)
		}
		words := make([]string, 0, len(filters[i].Words))
		for _, word := range filters[i].Words {
			if trimmed := strings.TrimSpace(word); trimmed != "" {
				words = append(words, trimmed)
			}
		}
		from := strings.TrimSpace(filters[i].From)
		// `from` with no words is legitimate: everything that sender writes.
		if len(words) == 0 && from == "" {
			return nil, fmt.Errorf("spam.filter[%q]: has neither words nor a sender, "+
				"so it would catch everything", name)
		}
		out = append(out, domain.SpamFilter{
			Name:  name,
			Words: domain.Tracked{Words: words},
			From:  from,
		})
	}
	return out, nil
}

// SpamPlaces converts the two hand-written lists.
func SpamPlaces(cfg config.Spam) domain.Spam {
	return domain.Spam{Entries: cfg.Rooms, Except: cfg.Except}
}
