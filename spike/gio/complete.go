package main

import (
	"cmp"
	"context"
	"image"
	"log"
	"regexp"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/emoji"
)

// Completion in a composer, as the desktop's: `:` and two letters of a name offer emoji,
// `@` and part of a name the room's people; a `:name:` closed by hand becomes its emoji.
// A person chosen goes into the text by name, and the mention travels with the draft
// (kithd sends those whose name is still in the words).

var (
	shortcodeTyped  = regexp.MustCompile(`(?i)(^|[\s(])(:[a-z0-9_+-]{2,})$`)
	shortcodeClosed = regexp.MustCompile(`(?i)(^|[\s(]):([a-z0-9_+-]+):$`)
	mentionTyped    = regexp.MustCompile(`(^|\s)@([^\s@]*)$`)
)

const (
	completionSize = 8   // how many candidates the popup offers
	memberLimit    = 500 // how many people a mention is chosen among, as the TUI's
)

// candidate is one offer: what it writes over the typed word, and the mention it adds.
type candidate struct {
	label, put string
	mention    *domain.Mention
}

// completion is a composer's popup: what is typed and where it starts (runes), the
// offers, and the one Enter takes.
type completion struct {
	start, caret int
	query        string
	offers       []candidate
	at           int
}

// completing is what the text before caret is completing, nil for nothing.
func (u *ui) completing(ctx context.Context, before []rune, caret int) *completion {
	text := string(before)
	if m := mentionTyped.FindStringSubmatch(text); m != nil {
		query := m[2]
		u.loadMembers(ctx)
		var offers []candidate
		q := strings.ToLower(query)
		for _, p := range u.timeline.members {
			name := cmp.Or(p.DisplayName, p.UserID)
			if strings.Contains(strings.ToLower(name), q) || strings.Contains(strings.ToLower(p.UserID), q) {
				offers = append(offers, candidate{label: name, put: "@" + name + " ", mention: &domain.Mention{UserID: p.UserID, Name: name}})
			}
			if len(offers) == completionSize {
				break
			}
		}
		return &completion{start: caret - len([]rune(query)) - 1, caret: caret, query: "@" + query, offers: offers}
	}
	if m := shortcodeTyped.FindStringSubmatch(text); m != nil {
		typed := m[2]
		var offers []candidate
		for _, glyph := range searchEmoji(strings.ToLower(typed[1:]), completionSize) {
			offers = append(offers, candidate{label: glyph, put: glyph})
		}
		return &completion{start: caret - len([]rune(typed)), caret: caret, query: typed, offers: offers}
	}
	return nil
}

// loadMembers asks kithd once per room for the people a mention may name.
func (u *ui) loadMembers(ctx context.Context) {
	t := &u.timeline
	if t.membersAsked {
		return
	}
	t.membersAsked = true
	room := t.room
	go func() {
		members, err := u.backend.MentionCandidates(ctx, room, memberLimit)
		if err != nil {
			log.Printf("mention candidates: %v", err)
		}
		u.mu.Lock()
		if u.timeline.room == room {
			u.timeline.members = members
		}
		u.mu.Unlock()
		u.w.Invalidate()
	}()
}

// complete runs before ed lays out, so it takes the popup's keys first: up and down
// choose, Tab or Enter take, Esc closes. It also turns a `:name:` just closed into its
// emoji. shortcut: the popup's keys are [keys.completion]'s defaults, not read from it.
func (u *ui) complete(ctx context.Context, gtx C, ed *widget.Editor) *completion {
	if !gtx.Focused(ed) {
		return nil
	}
	caret, _ := ed.Selection()
	runes := []rune(ed.Text())
	if caret > len(runes) {
		return nil
	}
	if m := shortcodeClosed.FindStringSubmatch(string(runes[:caret])); m != nil {
		if glyph, ok := emoji.ByName(strings.ToLower(m[2])); ok {
			start := caret - len([]rune(m[2])) - 2
			u.put(ed, runes, start, caret, glyph)
			return nil
		}
	}
	c := u.completing(ctx, runes[:caret], caret)
	if c == nil || len(c.offers) == 0 || u.dismissed == c.query {
		return nil
	}
	if u.offer != nil && u.offer.query == c.query {
		c.at = min(u.offer.at, len(c.offers)-1)
	}
	u.offer = c
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: ed, Name: key.NameUpArrow}, key.Filter{Focus: ed, Name: key.NameDownArrow},
			key.Filter{Focus: ed, Name: "P", Required: key.ModCtrl}, key.Filter{Focus: ed, Name: "N", Required: key.ModCtrl},
			key.Filter{Focus: ed, Name: key.NameTab}, key.Filter{Focus: ed, Name: key.NameReturn},
			key.Filter{Focus: ed, Name: key.NameEnter}, key.Filter{Focus: ed, Name: key.NameEscape},
		)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch e.Name {
		case key.NameUpArrow, "P":
			c.at = (c.at + len(c.offers) - 1) % len(c.offers)
		case key.NameDownArrow, "N":
			c.at = (c.at + 1) % len(c.offers)
		case key.NameEscape:
			u.dismissed = c.query
			return nil
		default:
			o := c.offers[c.at]
			u.put(ed, runes, c.start, c.caret, o.put)
			if o.mention != nil {
				u.mentions[ed] = append(u.mentions[ed], *o.mention)
			}
			u.offer = nil
			return nil
		}
	}
	return c
}

// put writes s over runes[from:to] of ed's text, the caret after it.
func (u *ui) put(ed *widget.Editor, runes []rune, from, to int, s string) {
	text := string(runes[:from]) + s + string(runes[to:])
	ed.SetText(text)
	at := from + len([]rune(s))
	ed.SetCaret(at, at)
}

// layoutCompletion is the popup's offers in a row over the composer, the chosen one
// marked.
func (u *ui) layoutCompletion(gtx C, c *completion) D {
	if c == nil {
		return D{}
	}
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		cells := make([]layout.FlexChild, 0, len(c.offers))
		for i, o := range c.offers {
			cells = append(cells, layout.Rigid(func(gtx C) D {
				m := op.Record(gtx.Ops)
				dims := layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
					l := material.Body1(u.th, o.label)
					l.MaxLines = 1
					return l.Layout(gtx)
				})
				call := m.Stop()
				if i == c.at {
					paint.FillShape(gtx.Ops, u.pal.selection, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(6)).Op(gtx.Ops))
				}
				call.Add(gtx.Ops)
				return dims
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, cells...)
	})
}
