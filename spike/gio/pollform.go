package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// The composer's form for a poll (the desktop's AskPoll), opened by its Poll button: a
// question, its answers (two to start, Add an answer for more), whether voters may choose
// several, and Create poll. Enter asks it once it is a poll, else moves to the next field;
// Esc or Cancel closes it.

// pollForm is the form while it is open. Its fields are pointers, so adding one moves
// none another has the focus of.
type pollForm struct {
	question            widget.Editor
	answers             []*widget.Editor
	several             widget.Bool
	add, cancel, create widget.Clickable
	busy                bool
}

func newPollForm() *pollForm {
	f := &pollForm{answers: []*widget.Editor{{}, {}}}
	for _, ed := range f.fields() {
		ed.SingleLine, ed.Submit = true, true
	}
	return f
}

// fields are the form's editors, the question first.
func (f *pollForm) fields() []*widget.Editor {
	return append([]*widget.Editor{&f.question}, f.answers...)
}

// asked is the poll the form writes: a question and at least two answers, a blank answer
// a row not used (polls.ts asked); ok false while it asks none.
func asked(question string, answers []string) (string, []string, bool) {
	var options []string
	for _, a := range answers {
		if a = strings.TrimSpace(a); a != "" {
			options = append(options, a)
		}
	}
	question = strings.TrimSpace(question)
	return question, options, question != "" && len(options) >= 2
}

// togglePoll opens the form, or closes it.
func (u *ui) togglePoll() {
	if u.asking != nil {
		u.asking, u.focus = nil, &u.composer
		return
	}
	u.asking = newPollForm()
	u.focus = &u.asking.question
}

// layoutPollForm draws the form over the composer, and takes its keys and clicks.
func (u *ui) layoutPollForm(ctx context.Context, gtx C) D {
	f := u.asking
	if f == nil {
		return D{}
	}
	answers := make([]string, len(f.answers))
	for i, ed := range f.answers {
		answers[i] = ed.Text()
	}
	question, options, ok := asked(f.question.Text(), answers)
	ask := func() {
		if !ok || f.busy {
			return
		}
		f.busy = true
		room, multiple := u.current, f.several.Value
		go func() {
			_, err := u.backend.CreatePoll(ctx, room, question, options, multiple)
			u.mu.Lock()
			f.busy = false
			if err != nil {
				u.note, u.noteAt = "Poll not asked: "+err.Error(), time.Now()
			} else if u.asking == f {
				u.asking, u.focus = nil, &u.composer
			}
			u.mu.Unlock()
			u.w.Invalidate()
		}()
	}
	if u.pollKeys(gtx, f, ok, ask) {
		return D{}
	}
	switch {
	case f.cancel.Clicked(gtx):
		u.togglePoll()
		return D{}
	case f.add.Clicked(gtx):
		ed := &widget.Editor{SingleLine: true, Submit: true}
		f.answers = append(f.answers, ed)
		u.focus = ed
	case f.create.Clicked(gtx):
		ask()
	}
	rows := []layout.FlexChild{layout.Rigid(func(gtx C) D { return u.pollField(gtx, &f.question, "A question") })}
	for i, ed := range f.answers {
		rows = append(rows, layout.Rigid(func(gtx C) D { return u.pollField(gtx, ed, "Answer "+strconv.Itoa(i+1)) }))
	}
	rows = append(rows,
		layout.Rigid(material.CheckBox(u.th, &f.several, "Voters may choose several answers").Layout),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return u.button(gtx, &f.add, "Add an answer", true, false) }),
				layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx C) D { return u.button(gtx, &f.cancel, "Cancel", true, false) }),
				layout.Flexed(1, layout.Spacer{}.Layout),
				layout.Rigid(func(gtx C) D { return u.button(gtx, &f.create, "Create poll", ok && !f.busy, false) }),
			)
		}))
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
}

// pollKeys takes the form's keys: Esc closes it (true), Enter asks the poll once it is
// one (ok), else moves to the next field.
func (u *ui) pollKeys(gtx C, f *pollForm, ok bool, ask func()) (closed bool) {
	fields := f.fields()
	for i, ed := range fields {
		for {
			ev, more := gtx.Event(key.Filter{Focus: ed, Name: key.NameEscape})
			if !more {
				break
			}
			if e, press := ev.(key.Event); press && e.State == key.Press {
				u.togglePoll()
				return true
			}
		}
		for {
			ev, more := ed.Update(gtx)
			if !more {
				break
			}
			if _, submit := ev.(widget.SubmitEvent); !submit {
				continue
			}
			if ok {
				ask()
			} else if i+1 < len(fields) {
				u.focus = fields[i+1]
			}
		}
	}
	return false
}

// pollField is one of the form's fields, in a box.
func (u *ui) pollField(gtx C, ed *widget.Editor, hint string) D {
	return layout.Inset{Bottom: unit.Dp(6)}.Layout(gtx, func(gtx C) D {
		return u.field(gtx, func(gtx C) D { return u.editor(ed, hint).Layout(gtx) })
	})
}
