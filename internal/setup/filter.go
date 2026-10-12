package setup

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The [[filter]]s, derived in one place so the daemon (on arrival) and the client
// (previewing, rescanning) agree.

// The ratio move's defaults: the share caught, the least message count, the window.
const (
	DefaultFilterRatio  = 0.6
	DefaultFilterFloor  = 5
	DefaultFilterWindow = 7 * 24 * time.Hour
)

// Filters validates the [[filter]]s: each needs a name, a tag that exists, and words or
// a sender; its moves and ratio numbers must read.
func Filters(cfg config.Config) (domain.Filters, error) {
	out := make(domain.Filters, 0, len(cfg.Filters))
	for i := range cfg.Filters {
		f, err := filter(cfg, i)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(out, func(o domain.Filter) bool { return strings.EqualFold(o.Name, f.Name) }) {
			return nil, fmt.Errorf("filter %q is defined twice", f.Name)
		}
		out = append(out, f)
	}
	return out, nil
}

// filter reads [[filter]] i.
func filter(cfg config.Config, i int) (domain.Filter, error) {
	in := cfg.Filters[i]
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return domain.Filter{}, fmt.Errorf("filter[%d]: needs a name — it is what /why says moved a room", i)
	}
	tag := strings.TrimSpace(in.Tag)
	if !slices.ContainsFunc(cfg.Tags, func(t config.Tag) bool { return domain.SameTag(t.Name, tag) }) {
		return domain.Filter{}, fmt.Errorf("filter %q: tag %q names no [[tag]] — it is where the filter moves rooms", name, in.Tag)
	}
	var words []string
	for _, word := range in.Words {
		if trimmed := strings.TrimSpace(word); trimmed != "" {
			words = append(words, trimmed)
		}
	}
	from := strings.TrimSpace(in.From)
	// `from` with no words is legitimate: everything that sender writes.
	if len(words) == 0 && from == "" {
		return domain.Filter{}, fmt.Errorf("filter %q: has neither words nor a sender, so it would catch everything", name)
	}
	moves, err := filterMoves(name, in.Moves)
	if err != nil {
		return domain.Filter{}, err
	}
	f := domain.Filter{
		Name: name, Tag: tag, Words: domain.Tracked{Words: words}, From: from, In: in.In, Moves: moves,
		Ratio: in.Ratio, Floor: in.Floor, Window: DefaultFilterWindow,
	}
	return filterRatio(f, in.Window)
}

// filterRatio checks the ratio move's numbers and fills their defaults.
func filterRatio(f domain.Filter, window string) (domain.Filter, error) {
	if f.Ratio < 0 || f.Ratio > 1 {
		return domain.Filter{}, fmt.Errorf("filter %q: ratio %v is not a share between 0 and 1 (0.6 is three messages in five)", f.Name, f.Ratio)
	}
	if f.Ratio == 0 {
		f.Ratio = DefaultFilterRatio
	}
	if f.Floor < 0 {
		return domain.Filter{}, fmt.Errorf("filter %q: floor %d is not a number of messages", f.Name, f.Floor)
	}
	if f.Floor == 0 {
		f.Floor = DefaultFilterFloor
	}
	if spelled := strings.TrimSpace(window); spelled != "" {
		d, err := time.ParseDuration(spelled)
		if err != nil || d <= 0 {
			return domain.Filter{}, fmt.Errorf("filter %q: window %q is not a span of time (try \"168h\")", f.Name, spelled)
		}
		f.Window = d
	}
	return f, nil
}

// filterMoves reads a filter's moves; none is config.FilterMovesDefault.
func filterMoves(filter string, words []string) (domain.FilterMoves, error) {
	if len(words) == 0 {
		words = config.FilterMovesDefault
	}
	var out domain.FilterMoves
	for _, word := range words {
		switch strings.ToLower(strings.TrimSpace(word)) {
		case config.FilterMoveAlways:
			out.Always = true
		case config.FilterMoveFirstMessage:
			out.FirstMessage = true
		case config.FilterMoveDirect:
			out.Direct = true
		case config.FilterMoveRatio:
			out.Ratio = true
		default:
			return domain.FilterMoves{}, fmt.Errorf("filter %q: moves: %q is not one of %s, %s, %s, %s", filter, word,
				config.FilterMoveAlways, config.FilterMoveFirstMessage, config.FilterMoveDirect, config.FilterMoveRatio)
		}
	}
	return out, nil
}
