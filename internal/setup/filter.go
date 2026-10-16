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

// The ratio moves' defaults: the share caught, the least message count, the window;
// and how many rooms one caught message must reach for sender_spread.
const (
	DefaultFilterRatio  = 0.6
	DefaultFilterFloor  = 5
	DefaultFilterWindow = 7 * 24 * time.Hour
	DefaultFilterSpread = 3
)

// Filters validates the [[filter]]s: each needs a name, a tag that exists, and words, a
// sender or a language; its languages, moves and ratio numbers must read.
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
	f := domain.Filter{
		Name: name, Tag: tag, Words: domain.Tracked{Words: words}, From: from, In: in.In,
		Ratio: in.Ratio, Floor: in.Floor, Window: DefaultFilterWindow, Spread: in.Spread,
	}
	if err := filterLanguages(&f, in); err != nil {
		return domain.Filter{}, err
	}
	// `from` or a language with no words is legitimate: everything that sender writes,
	// everything in that language.
	if len(words) == 0 && from == "" && len(f.Lang)+len(f.NotLang)+len(f.Scripts) == 0 {
		return domain.Filter{}, fmt.Errorf("filter %q: has no words, sender or language, so it would catch everything", name)
	}
	moves, err := filterMoves(name, in.Moves)
	if err != nil {
		return domain.Filter{}, err
	}
	f.Moves = moves
	return filterRatio(f, in.Window)
}

// filterLanguages reads a filter's lang, not_lang and script, refusing an entry it does
// not know rather than reading it as none.
func filterLanguages(f *domain.Filter, in config.Filter) error {
	var err error
	if f.Lang, err = domain.LanguageCodes(in.Lang); err != nil {
		return fmt.Errorf("filter %q: lang: %w", f.Name, err)
	}
	if f.NotLang, err = domain.LanguageCodes(in.NotLang); err != nil {
		return fmt.Errorf("filter %q: not_lang: %w", f.Name, err)
	}
	for _, entry := range in.Script {
		script, err := domain.ScriptNamed(entry)
		if err != nil {
			return fmt.Errorf("filter %q: script: %w", f.Name, err)
		}
		f.Scripts = append(f.Scripts, script)
	}
	return nil
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
	// One room is any catch: always, not a spread.
	if f.Spread < 0 || f.Spread == 1 {
		return domain.Filter{}, fmt.Errorf("filter %q: spread %d is not a number of rooms of 2 or more", f.Name, f.Spread)
	}
	if f.Spread == 0 {
		f.Spread = DefaultFilterSpread
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
		case config.FilterMoveSenderFirst:
			out.SenderFirst = true
		case config.FilterMoveSenderRatio:
			out.SenderRatio = true
		case config.FilterMoveSenderSpread:
			out.SenderSpread = true
		default:
			return domain.FilterMoves{}, fmt.Errorf("filter %q: moves: %q is not one of %s", filter, word, strings.Join([]string{
				config.FilterMoveAlways, config.FilterMoveFirstMessage, config.FilterMoveDirect, config.FilterMoveRatio,
				config.FilterMoveSenderFirst, config.FilterMoveSenderRatio, config.FilterMoveSenderSpread,
			}, ", "))
		}
	}
	return out, nil
}
