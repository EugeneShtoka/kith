package signal

import (
	"slices"
	"strings"
	"unicode/utf16"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// placeholder is where Signal's text has a mention: the person is named beside it.
const placeholder = "￼"

// mentionsOf is a draft's text as Signal sends it: each mention of a Signal person (its
// text, "@Dana") is U+FFFC, with the person named at that place (in UTF-16 units, as
// Signal counts). A mention of anyone else stays as typed.
func mentionsOf(draft domain.Draft) (string, []*signalv1.Mention) {
	body := draft.Body
	type at struct {
		start int // in bytes of body
		m     domain.Mention
		aci   string
	}
	var found []at
	taken := func(i, n int) bool {
		return slices.ContainsFunc(found, func(f at) bool { return i < f.start+len(f.m.Name) && f.start < i+n })
	}
	for _, m := range draft.LiveMentions() {
		id := domain.ParseID(m.UserID)
		if id.Network != domain.ProtocolSignal || id.Account != "" || !isACI(id.Native) || m.Name == "" {
			continue
		}
		for from := 0; ; {
			i := strings.Index(body[from:], m.Name)
			if i < 0 {
				break
			}
			if i += from; !taken(i, len(m.Name)) {
				found = append(found, at{start: i, m: m, aci: id.Native})
				break
			}
			from = i + len(m.Name)
		}
	}
	if len(found) == 0 {
		return body, nil
	}
	slices.SortFunc(found, func(x, y at) int { return x.start - y.start })
	var out strings.Builder
	var mentions []*signalv1.Mention
	last := 0
	for _, f := range found {
		out.WriteString(body[last:f.start])
		start := len(utf16.Encode([]rune(out.String())))
		out.WriteString(placeholder)
		mentions = append(mentions, &signalv1.Mention{
			Start: uint32(start), Length: 1, Aci: f.aci, Name: strings.TrimPrefix(f.m.Name, "@"), //nolint:gosec // a message's length fits
		})
		last = f.start + len(f.m.Name)
	}
	out.WriteString(body[last:])
	return out.String(), mentions
}
