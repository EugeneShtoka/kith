package main

import (
	"context"
	"image"
	"slices"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Naming, as the TUI's `a` and the desktop's: [keys.rooms] name gives the room under the
// cursor a name of your own ([[display.name]], kithd writes it; an empty one takes it
// away), and [keys.timeline] name makes the selected message's sender one of your people
// ([[display.identity]], "same person as"): one already made, picked under the field,
// keeps their color and picture; a name typed makes a new one. Both ask in a card over
// the window: Enter takes, the arrows pick, Esc or a click outside closes.

// promptChoices is how many of the choices the card lists at once.
const promptChoices = 8

// prompt is the card while it is up: its question, the words typed, the names it offers
// (narrowed by what is typed) with the one under the arrows (-1: the words typed), and
// what it does with the words taken; the keys go back to back once it closes.
type prompt struct {
	title, hint string
	back        pane
	field       widget.Editor
	choices     []string
	at          int
	btns        []widget.Clickable
	take        func(words string)
	focused     bool
	close, card widget.Clickable
}

// newPrompt asks title, starting from words.
func (u *ui) newPrompt(title, hint, words string, take func(string)) *prompt {
	p := &prompt{title: title, hint: hint, back: u.pane, at: -1, take: take}
	p.field.SingleLine, p.field.Submit = true, true
	p.field.SetText(words)
	n := len([]rune(words))
	p.field.SetCaret(n, n)
	return p
}

// offered is the choices holding what is typed, at most promptChoices.
func (p *prompt) offered() []string {
	typed := strings.ToLower(strings.TrimSpace(p.field.Text()))
	var out []string
	for _, c := range p.choices {
		if strings.Contains(strings.ToLower(c), typed) {
			out = append(out, c)
		}
		if len(out) == promptChoices {
			break
		}
	}
	return out
}

// taken is what Enter takes: the choice under the arrows, else the words typed.
func (p *prompt) taken() string {
	if offered := p.offered(); p.at >= 0 && p.at < len(offered) {
		return offered[p.at]
	}
	return strings.TrimSpace(p.field.Text())
}

// roomName is room as the rail names it: the name you gave it ([[display.name]]), else
// its own.
func (u *ui) roomName(room domain.Room) string {
	if served := u.rail.served[room.ID]; served.Name != "" {
		return served.Name
	}
	return room.DisplayName()
}

// nameRoom asks for a name of your own for room, starting from the one it shows.
func (u *ui) nameRoom(ctx context.Context, room domain.Room) {
	shown, own := u.roomName(room), room.DisplayName()
	u.prompting = u.newPrompt("Name "+shown, "A name of your own; empty takes it away", shown, func(name string) {
		go func() {
			switch err := u.backend.SetRoomName(ctx, room.ID, name); {
			case err != nil:
				u.say(shown + " was not named: " + err.Error())
			case name == "":
				u.say(own + " has its own name again")
			default:
				u.say(own + " is " + name + " for you")
			}
		}()
	})
}

// samePerson asks which of your people account (shown as name) is, offering those
// already made once they are read.
func (u *ui) samePerson(ctx context.Context, account, name string) {
	p := u.newPrompt(name+" is the same person as", "Their name, or one of your people below", name, nil)
	var people []config.Identity
	p.take = func(alias string) {
		if alias == "" {
			u.note = "Give them a name"
			return
		}
		var color, picture string
		if i := slices.IndexFunc(people, func(id config.Identity) bool { return id.Alias == alias }); i >= 0 {
			color, picture = people[i].Color, people[i].Picture // theirs, unchanged
		}
		go func() {
			if err := u.backend.SetIdentity(ctx, account, alias, color, picture); err != nil {
				u.say("Not saved: " + err.Error())
				return
			}
			u.say(name + " is " + alias)
		}()
	}
	u.prompting = p
	go func() {
		got, err := u.backend.Identities(ctx)
		if err != nil {
			u.say("Could not read your people: " + err.Error())
			return
		}
		u.mu.Lock()
		people = got
		for _, id := range got {
			p.choices = append(p.choices, id.Alias)
			if slices.Contains(id.IDs, account) {
				p.field.SetText(id.Alias) // theirs already
				n := len([]rune(id.Alias))
				p.field.SetCaret(n, n)
			}
		}
		u.mu.Unlock()
		u.w.Invalidate()
	}()
}

// arrows moves the choice under the arrows by the field's Up and Down; true on Esc.
func (p *prompt) arrows(gtx C, offered int) (closed bool) {
	step := map[key.Name]int{key.NameUpArrow: -1, key.NameDownArrow: 1}
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &p.field, Name: key.NameEscape},
			key.Filter{Focus: &p.field, Name: key.NameUpArrow},
			key.Filter{Focus: &p.field, Name: key.NameDownArrow},
		)
		if !ok {
			return false
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if e.Name == key.NameEscape {
				return true
			}
			p.at = max(-1, min(offered-1, p.at+step[e.Name]))
		}
	}
}

// layoutPrompt draws the card, and takes its keys: the field's own, Enter, the arrows
// and Esc.
func (u *ui) layoutPrompt(gtx C) {
	p := u.prompting
	if !p.focused {
		p.focused, u.focus = true, &p.field
	}
	shut := func() {
		u.prompting = nil
		u.toPane(p.back)
	}
	offered := p.offered()
	if p.arrows(gtx, len(offered)) {
		shut()
		return
	}
	for {
		ev, ok := p.field.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.SubmitEvent:
			shut()
			p.take(p.taken())
			return
		case widget.ChangeEvent:
			p.at = -1
		}
	}
	if p.close.Clicked(gtx) {
		shut()
		return
	}
	if len(p.btns) < len(offered) {
		p.btns = make([]widget.Clickable, len(offered))
	}
	for i := range offered {
		if p.btns[i].Clicked(gtx) {
			shut()
			p.take(offered[i])
			return
		}
	}
	u.layoutCard(gtx, &p.close, &p.card, func(gtx C) D {
		rows := []layout.FlexChild{
			layout.Rigid(material.H6(u.th, p.title).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx C) D { return u.field(gtx, u.editor(&p.field, p.hint).Layout) }),
		}
		for i, choice := range offered {
			rows = append(rows, layout.Rigid(func(gtx C) D {
				return u.cardRow(gtx, &p.btns[i], i == p.at, material.Body1(u.th, choice).Layout)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
}

// layoutCard draws body as a card over the shaded window; a click outside it is close's.
func (u *ui) layoutCard(gtx C, closer, card *widget.Clickable, body layout.Widget) {
	shade := u.pal.background
	shade.A = 0x99
	paint.FillShape(gtx.Ops, shade, clip.Rect{Max: gtx.Constraints.Max}.Op())
	closer.Layout(gtx, func(gtx C) D { return D{Size: gtx.Constraints.Max} })
	layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = min(gtx.Dp(480), gtx.Constraints.Max.X)
		gtx.Constraints.Max.X = gtx.Constraints.Min.X
		m := op.Record(gtx.Ops)
		dims := layout.UniformInset(unit.Dp(12)).Layout(gtx, body)
		call := m.Stop()
		box := image.Rectangle{Max: dims.Size}
		paint.FillShape(gtx.Ops, u.pal.surface, clip.UniformRRect(box, gtx.Dp(8)).Op(gtx.Ops))
		u.outline(gtx, box)
		card.Layout(gtx, func(gtx C) D { return D{Size: dims.Size} })
		call.Add(gtx.Ops)
		return dims
	})
}

// cardRow is one of a card's rows as a button, on the selection while the arrows are on it.
func (u *ui) cardRow(gtx C, btn *widget.Clickable, at bool, w layout.Widget) D {
	return btn.Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		dims := layout.UniformInset(unit.Dp(6)).Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w(gtx)
		})
		call := m.Stop()
		if at {
			paint.FillShape(gtx.Ops, u.pal.selection, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(6)).Op(gtx.Ops))
		}
		call.Add(gtx.Ops)
		return dims
	})
}
