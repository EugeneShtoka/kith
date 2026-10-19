package signal

import (
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// placeholder is where Signal's text has a mention: the person is named beside it.
const placeholder = "￼"

// mentionsOf is a draft's text as Signal sends it: each mention of a Signal person (its
// text, "@Dana") is U+FFFC, with the person named at that place (in UTF-16 units, as
// Signal counts). A mention of anyone else stays as typed. units maps each byte offset
// of the draft's text (and one past its end) to its UTF-16 offset in what is sent.
func mentionsOf(draft domain.Draft) (body string, mentions []*signalv1.Mention, units []int) {
	body = draft.Body
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
	slices.SortFunc(found, func(x, y at) int { return x.start - y.start })
	var out strings.Builder
	units = make([]int, len(body)+1)
	u, last := 0, 0
	kept := func(to int) { // copies body up to to as it is
		for i, r := range body[last:to] {
			for j := range utf8.RuneLen(r) {
				units[last+i+j] = u
			}
			u += utf16.RuneLen(r)
		}
		out.WriteString(body[last:to])
	}
	for _, f := range found {
		kept(f.start)
		for j := range len(f.m.Name) {
			units[f.start+j] = u
		}
		out.WriteString(placeholder)
		mentions = append(mentions, &signalv1.Mention{
			Start: uint32(u), Length: 1, Aci: f.aci, Name: strings.TrimPrefix(f.m.Name, "@"), //nolint:gosec // a message's length fits
		})
		u++
		last = f.start + len(f.m.Name)
	}
	kept(len(body))
	units[len(body)] = u
	return out.String(), mentions, units
}
