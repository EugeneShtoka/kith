package main

import (
	"context"
	"errors"
	"image"
	"image/color"
	"log"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Spelling and word completion in a composer, as the desktop's (spell.ts, complete.ts):
// kithd checks the draft after a pause in typing (CheckSpelling) and its marks are drawn
// under the words ([spell] underline, rare_underline), none on the word the caret is in;
// [keys.spell] open_insert walks them, one word at a time, [keys.spell]'s keys taking a
// suggestion, adding the word to the dictionary, ignoring it or skipping it. The word
// being typed is looked up (CompleteWord) and, when one completion clearly leads
// ([complete] ratio), drawn after the caret: [keys.insert] complete or complete_all
// takes it.
// shortcut: marks are of the text last checked and drawn only while it is still the
// text, not moved across an edit as the desktop moves them; port its shiftMarks if the
// marks blinking out as you type grates.
// shortcut: no autocorrect, no [spell] check_before_send, no click on a mark, and a
// suggestion taken fixes that copy only; the TUI and the desktop have them.
// shortcut: completion is the word being typed only: not the model's next words nor the
// room's phrases, not Tab's list with none leading, and [complete] scope "space" is
// asked without the space's rooms.

const (
	spellDebounce  = 200 * time.Millisecond // the TUI's and the desktop's
	assistDebounce = 120 * time.Millisecond
	assistAsked    = 6 // candidates asked for: the leader's test reads the runners-up
)

// spelling is one composer's: the text last checked and its marks (rune offsets), the
// text a check is pending for, the walk when open, and the word being completed with
// kithd's answer.
type spelling struct {
	checked, asking string
	marks           []mark
	off             bool // kithd has nothing to check with
	timer           *time.Timer
	walk            *walk
	prefix          string // lower-cased, as asked
	words           []domain.WordCandidate
	wordTimer       *time.Timer
}

// mark is a word worth marking, by rune offsets into the text checked.
type mark struct {
	word        string
	start, end  int
	suggestions []string
	rare        bool
}

// walk is the correction walk: the marks as it opened on, moved as each is put right,
// the one it is at, and the text as it left it. The composer is read-only while it is
// open (Gio types a key's letter even when a filter takes the key), and anything else
// changing the text (another room's draft) closes it.
type walk struct {
	items []mark
	at    int
	text  string
}

// marksOf is kithd's answer for text, its byte offsets made rune offsets.
func marksOf(text string, found []domain.Misspelling) []mark {
	out := make([]mark, 0, len(found))
	for _, f := range found {
		if f.Start < 0 || f.End > len(text) || f.Start > f.End {
			continue
		}
		out = append(out, mark{
			word: f.Word, start: utf8.RuneCountInString(text[:f.Start]), end: utf8.RuneCountInString(text[:f.End]),
			suggestions: f.Suggestions, rare: f.Rare,
		})
	}
	return out
}

// underlined is the marks to underline with the caret at caret: none on the word it is in
// (unfinished, not misspelled; just after its last letter is still in it), none of a
// kind whose underline is "none".
func underlined(marks []mark, caret int, s config.DeviceSpell) []mark {
	var out []mark
	for _, m := range marks {
		if underline(m, s) != config.SpellNoUnderline && (caret < m.start || caret > m.end) {
			out = append(out, m)
		}
	}
	return out
}

// underline is how m is drawn: [spell] underline, or rare_underline for a rare word.
func underline(m mark, s config.DeviceSpell) string {
	if m.rare {
		if s.RareUnderline == "" {
			return config.SpellDotted
		}
		return s.RareUnderline
	}
	if s.Underline == "" {
		return config.SpellCurly
	}
	return s.Underline
}

// wordBeingTyped is the run of letters the caret ends, when the caret is at the text's
// end and enough is typed to be worth finishing.
func wordBeingTyped(runes []rune, caret int) string {
	if caret != len(runes) {
		return ""
	}
	i := caret
	for i > 0 && unicode.IsLetter(runes[i-1]) {
		i--
	}
	if caret-i < domain.CompleteMinPrefix {
		return ""
	}
	return string(runes[i:caret])
}

// ghost is what is drawn after typed: the rest of the completion clearly leading
// (re-cased like typed, going on from it and saving enough), "" for none.
func ghost(typed string, words []domain.WordCandidate, ratio int) string {
	var offered []domain.WordCandidate
	for _, c := range words {
		w := domain.RecaseLike(typed, c.Word)
		if strings.HasPrefix(w, typed) && domain.LongEnough(typed, w) {
			offered = append(offered, domain.WordCandidate{Word: w, Score: c.Score})
		}
		if len(offered) == domain.CompleteChoices {
			break
		}
	}
	lead, ok := domain.Dominant(offered, ratio)
	if !ok {
		return ""
	}
	return lead.Word[len(typed):]
}

// spellingOf is ed's, made on first use.
func (u *ui) spellingOf(ed *widget.Editor) *spelling {
	s := u.spells[ed]
	if s == nil {
		s = &spelling{}
		u.spells[ed] = s
	}
	return s
}

// followWords asks kithd about ed's text as it now is: its spelling after a pause, the word
// being typed after a shorter one. Each answer is taken only while it is still the ask.
func (u *ui) followWords(ctx context.Context, ed *widget.Editor) *spelling {
	s := u.spellingOf(ed)
	text := ed.Text()
	if strings.TrimSpace(text) == "" {
		s.checked, s.asking, s.marks = text, "", nil // nothing to check
	}
	if !s.off && text != s.checked && text != s.asking {
		s.asking = text
		if s.timer != nil {
			s.timer.Stop()
		}
		s.timer = time.AfterFunc(spellDebounce, func() { u.check(ctx, s, text) })
	}
	caret, _ := ed.Selection()
	typed := ""
	if u.completeConf.CompleteEnabled() && u.completeConf.GhostEnabled() {
		typed = wordBeingTyped([]rune(text), caret)
	}
	if prefix := strings.ToLower(typed); prefix != s.prefix {
		s.prefix, s.words = prefix, nil
		if s.wordTimer != nil {
			s.wordTimer.Stop()
		}
		if prefix != "" {
			req := domain.CompleteRequest{
				Prefix: prefix, RoomIDs: []domain.RoomID{u.current}, Scope: u.completeConf.ScopeOrDefault(),
				Sources: u.completeConf.Sources, Limit: assistAsked,
			}
			s.wordTimer = time.AfterFunc(assistDebounce, func() { u.completeWord(ctx, s, req) })
		}
	}
	return s
}

// check asks kithd to check text, for s while text is still what it asks about.
func (u *ui) check(ctx context.Context, s *spelling, text string) {
	u.mu.Lock()
	current := s.asking == text
	u.mu.Unlock()
	if !current {
		return
	}
	found, err := u.backend.CheckSpelling(ctx, text)
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case errors.Is(err, api.ErrSpellUnavailable):
		s.off, s.marks, s.checked = true, nil, ""
	case err != nil:
		log.Printf("check spelling: %v", err) // per pause, so not news: the marks stay
		return
	case s.asking == text:
		s.checked, s.marks = text, marksOf(text, found)
	default:
		return
	}
	u.w.Invalidate()
}

// completeWord asks kithd to finish req's prefix, for s while it is still the word typed.
func (u *ui) completeWord(ctx context.Context, s *spelling, req domain.CompleteRequest) {
	words, err := u.backend.CompleteWord(ctx, req)
	if err != nil {
		log.Printf("complete word: %v", err) // unasked for, so a failure is not news
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if s.prefix == req.Prefix {
		s.words = words
		u.w.Invalidate()
	}
}

// openWalk walks the composer's marks, from the first.
func (u *ui) openWalk(ed *widget.Editor) {
	s := u.spellingOf(ed)
	switch {
	case s.off:
		u.note = "Nothing to check spelling with"
	case s.checked != ed.Text():
		u.note = "Still checking the spelling"
	case len(s.marks) == 0:
		u.note = "No misspellings"
	default:
		s.walk = &walk{items: append([]mark(nil), s.marks...), text: ed.Text()}
		return
	}
	u.noteAt = time.Now()
}

// spellPaths are the walk's [keys.spell] actions.
var spellPaths = []string{
	"spell.add", "spell.ignore", "spell.skip", "spell.skip_all", "spell.ignore_all", "spell.stop",
	"spell.choose_1", "spell.choose_2", "spell.choose_3", "spell.choose_4", "spell.choose_5",
	"spell.choose_6", "spell.choose_7", "spell.choose_8", "spell.choose_9",
}

// walkKeys takes the walk's keys before ed does, and the ghost's while one is drawn.
func (u *ui) walkKeys(ctx context.Context, gtx C, ed *widget.Editor, s *spelling, ghosted string) {
	if s.walk != nil && ed.Text() != s.walk.text {
		s.walk = nil
	}
	ed.ReadOnly = s.walk != nil
	if !gtx.Focused(ed) {
		return
	}
	names := map[string]string{}
	if s.walk != nil {
		maps.Copy(names, u.spellKeys)
	} else if ghosted != "" {
		maps.Copy(names, u.ghostKeys)
	}
	var filters []event.Filter
	for name := range names {
		for _, f := range pressFilters(name, ed) {
			filters = append(filters, f)
		}
	}
	for len(filters) > 0 {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if path := names[keyName(e)]; s.walk != nil {
				u.walkStep(ctx, ed, s, path)
			} else if path != "" {
				u.takeGhost(ed, ghosted)
			}
		}
	}
}

// takeGhost puts the ghost after the caret, at the text's end.
func (u *ui) takeGhost(ed *widget.Editor, ghosted string) {
	text := ed.Text() + ghosted
	ed.SetText(text)
	n := utf8.RuneCountInString(text)
	ed.SetCaret(n, n)
}

// walkStep is a [keys.spell] action in the walk.
func (u *ui) walkStep(ctx context.Context, ed *widget.Editor, s *spelling, path string) {
	w := s.walk
	m := w.items[w.at]
	switch path {
	case "spell.stop", "spell.skip_all":
		s.walk = nil
		return
	case "spell.add", "spell.ignore":
		u.learn(ctx, s, []string{m.word}, path == "spell.add")
		w.items = withoutLater(w.items, w.at, m.word)
	case "spell.ignore_all":
		var words []string
		for _, later := range w.items[w.at:] {
			words = append(words, later.word)
		}
		u.learn(ctx, s, words, false)
		s.walk = nil
		return
	case "spell.skip":
		w.items = withoutLater(w.items, w.at, m.word)
	default: // spell.choose_N
		n := int(path[len(path)-1] - '1')
		if n >= len(m.suggestions) {
			return
		}
		runes := []rune(ed.Text())
		if m.end > len(runes) || string(runes[m.start:m.end]) != m.word {
			break // the word was changed by hand: left as it is
		}
		put := m.suggestions[n]
		u.put(ed, runes, m.start, m.end, put)
		delta := utf8.RuneCountInString(put) - (m.end - m.start)
		for i := w.at + 1; i < len(w.items); i++ {
			w.items[i].start += delta
			w.items[i].end += delta
		}
		w.text = ed.Text()
	}
	if w.at++; w.at >= len(w.items) {
		s.walk = nil
	}
}

// withoutLater is items less the copies of word after at, so one answer covers them.
func withoutLater(items []mark, at int, word string) []mark {
	out := items[: at+1 : at+1]
	for _, m := range items[at+1:] {
		if m.word != word {
			out = append(out, m)
		}
	}
	return out
}

// learn tells kithd words are right, forever (the dictionary) or for this session, and
// drops their marks.
func (u *ui) learn(ctx context.Context, s *spelling, words []string, forever bool) {
	s.marks = slices.DeleteFunc(slices.Clone(s.marks), func(m mark) bool { return slices.Contains(words, m.word) })
	go func() {
		for _, word := range words {
			if err := u.backend.LearnWord(ctx, word, forever); err != nil {
				u.say("Not learned: " + err.Error())
				return
			}
		}
	}()
}

// layoutWords draws ed with its marks under the words and the ghost after the caret.
func (u *ui) layoutWords(gtx C, ed *widget.Editor, s *spelling, ghosted, hint string) D {
	dims := u.editor(ed, hint).Layout(gtx)
	caret, _ := ed.Selection()
	if s.walk != nil {
		m := s.walk.items[s.walk.at]
		for _, r := range ed.Regions(m.start, m.end, nil) {
			paint.FillShape(gtx.Ops, withAlpha(u.pal.selection, 0x99), clip.Rect(r.Bounds).Op())
		}
	} else if s.checked == ed.Text() {
		for _, m := range underlined(s.marks, caret, u.spellConf) {
			col := u.pal.mention
			if m.rare {
				col = u.pal.muted
			}
			for _, r := range ed.Regions(m.start, m.end, nil) {
				u.underline(gtx, r, underline(m, u.spellConf), col)
			}
		}
	}
	if ghosted != "" {
		at := ed.CaretCoords()
		l := material.Body1(u.th, ghosted)
		l.Color, l.MaxLines = u.pal.muted, 1
		m := op.Record(gtx.Ops)
		gd := l.Layout(gtx)
		call := m.Stop()
		// CaretCoords is the caret's baseline; the label's is Baseline above its bottom.
		defer op.Offset(image.Pt(int(at.X), int(at.Y)-(gd.Size.Y-gd.Baseline))).Push(gtx.Ops).Pop()
		call.Add(gtx.Ops)
	}
	return dims
}

// underline draws style under region r: curly, a line, or dotted.
func (u *ui) underline(gtx C, r widget.Region, style string, col color.NRGBA) {
	base := r.Bounds.Max.Y - r.Baseline
	th := max(1, gtx.Dp(1))
	y := min(base+gtx.Dp(2), r.Bounds.Max.Y-th)
	switch style {
	case config.SpellLine:
		paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(r.Bounds.Min.X, y), Max: image.Pt(r.Bounds.Max.X, y+th)}.Op())
	case config.SpellDotted:
		for x := r.Bounds.Min.X; x+th <= r.Bounds.Max.X; x += 3 * th {
			paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(x, y), Max: image.Pt(x+th, y+th)}.Op())
		}
	default: // curly
		step := float32(gtx.Dp(2))
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(f32.Pt(float32(r.Bounds.Min.X), float32(y)))
		for x, up := float32(r.Bounds.Min.X)+step, true; x <= float32(r.Bounds.Max.X); x, up = x+step, !up {
			dy := float32(y)
			if up {
				dy -= step / 2
			}
			p.LineTo(f32.Pt(x, dy))
		}
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: float32(th)}.Op())
	}
}

func withAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

// layoutWalk is the walk over the composer: the word, its suggestions by number, and
// what else may be done with it.
func (u *ui) layoutWalk(gtx C, s *spelling) D {
	if s == nil || s.walk == nil {
		return D{}
	}
	m := s.walk.items[s.walk.at]
	parts := []string{m.word + ":"}
	for i, sug := range m.suggestions[:min(len(m.suggestions), 9)] {
		parts = append(parts, string(rune('1'+i))+" "+sug)
	}
	if len(m.suggestions) == 0 {
		parts = append(parts, "no suggestions")
	}
	help := material.Caption(u.th, "a add · i ignore · s skip · I ignore all · esc stop")
	help.Color = u.pal.muted
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Flexed(1, material.Body1(u.th, strings.Join(parts, "   ")).Layout),
			layout.Rigid(help.Layout),
		)
	})
}
