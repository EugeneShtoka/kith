package tui

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func pickerItems(pairs ...[2]string) []pickerItem {
	out := make([]pickerItem, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, pickerItem{label: p[0], value: p[1], match: p[0]})
	}
	return out
}

func withPicker(t *testing.T, spec pickerSpec, items []pickerItem) Model {
	t.Helper()
	m := sized(t, withRooms(t, newModel()))
	m.focus = paneTimeline
	m.picker = newPickerWith(pickerEmoji, spec, items)
	return m
}

func listSpec(modal bool) pickerSpec { return pickerSpec{title: "Test", modal: modal} }
func gridSpec(modal bool) pickerSpec { return pickerSpec{title: "Test", modal: modal, grid: true} }

var pickerPeopleFixture = pickerItems(
	[2]string{"Alice Cohen", "@alice:x"},
	[2]string{"Bob Stone", "@bob:x"},
	[2]string{"Carol Diaz", "@carol:x"},
	[2]string{"Alistair Bo", "@ali:x"},
)

// In navigate mode a letter moves; in filter mode the same letter types.
func TestPickerModalKeysSwitchMeaning(t *testing.T) {
	t.Parallel()

	m := withPicker(t, listSpec(true), pickerPeopleFixture)
	m, _ = press(t, m, keyText("j"))
	if m.picker.cursor != 1 || m.picker.filter != "" {
		t.Errorf("navigate: cursor=%d filter=%q, want a move and no text", m.picker.cursor, m.picker.filter)
	}
	m, _ = press(t, m, keyText("i"))
	if m.picker.mode != pickerFilter {
		t.Fatal("i should enter filter mode")
	}
	m, _ = press(t, m, keyText("j"))
	if m.picker.filter != "j" {
		t.Errorf("filter: filter=%q, want the letter typed", m.picker.filter)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.picker.mode != pickerNavigate || m.picker.filter != "" {
		t.Errorf("esc: mode=%v filter=%q, want navigate and cleared", m.picker.mode, m.picker.filter)
	}
	if !m.picker.active() {
		t.Error("the first esc should not close a modal picker")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.picker.active() {
		t.Error("the second esc should close it")
	}
}

// A non-modal picker filters at once; esc clears the query first, then closes.
func TestPickerNonModalEscapeClearsThenCloses(t *testing.T) {
	t.Parallel()

	m := withPicker(t, listSpec(false), pickerPeopleFixture)
	all := len(m.picker.items)
	m, _ = press(t, m, keyText("a"))
	if m.picker.filter != "a" {
		t.Fatalf("filter = %q, want typing to narrow at once", m.picker.filter)
	}

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.picker.active() {
		t.Fatal("the first esc closed the list instead of clearing the query")
	}
	if m.picker.filter != "" {
		t.Errorf("filter = %q, want it emptied", m.picker.filter)
	}
	if len(m.picker.items) != all {
		t.Errorf("%d rows after clearing, want the whole list of %d back", len(m.picker.items), all)
	}

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.picker.active() {
		t.Error("esc on an empty query did not close the list")
	}
}

// Filtering is prefix-before-substring over the item's match text.
func TestPickerFiltering(t *testing.T) {
	t.Parallel()

	m := withPicker(t, listSpec(false), pickerPeopleFixture)
	for _, r := range "ali" {
		m, _ = press(t, m, keyText(string(r)))
	}
	got := []string{}
	for _, item := range m.picker.items {
		got = append(got, item.label)
	}
	if len(got) != 2 || got[0] != "Alice Cohen" || got[1] != "Alistair Bo" {
		t.Errorf("filtered to %v, want the two prefix matches", got)
	}

	m2 := withPicker(t, listSpec(false), pickerItems(
		[2]string{"Xavier Ali", "@x:x"},
		[2]string{"Alice", "@a:x"},
	))
	for _, r := range "ali" {
		m2, _ = press(t, m2, keyText(string(r)))
	}
	if len(m2.picker.items) != 2 || m2.picker.items[0].label != "Alice" {
		t.Errorf("prefix should sort before substring, got %v", m2.picker.items)
	}

	for range 3 {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if len(m.picker.items) != len(pickerPeopleFixture) {
		t.Errorf("clearing the filter left %d items, want all %d", len(m.picker.items), len(pickerPeopleFixture))
	}
}

// A filter that shrinks the list must not leave the cursor past the end.
func TestPickerCursorSurvivesFiltering(t *testing.T) {
	t.Parallel()

	m := withPicker(t, listSpec(false), pickerPeopleFixture)
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnd}) // last item
	if m.picker.cursor != len(pickerPeopleFixture)-1 {
		t.Fatalf("cursor = %d", m.picker.cursor)
	}
	for _, r := range "bob" {
		m, _ = press(t, m, keyText(string(r)))
	}
	if m.picker.cursor >= len(m.picker.items) {
		t.Errorf("cursor %d is past the %d filtered items", m.picker.cursor, len(m.picker.items))
	}
	if _, ok := m.picker.selected(); !ok {
		t.Error("something should still be selected after filtering")
	}
}

// Nothing matching is survivable, says so, and accepting does nothing.
func TestPickerEmptyResult(t *testing.T) {
	t.Parallel()

	m := withPicker(t, listSpec(false), pickerPeopleFixture)
	for _, r := range "zzz" {
		m, _ = press(t, m, keyText(string(r)))
	}
	if _, ok := m.picker.selected(); ok {
		t.Error("nothing should be selected when nothing matches")
	}
	if !strings.Contains(stripStyles(strings.Join(m.pickerLines(60, 6), "\n")), "nothing matches") {
		t.Error("an empty result should say so")
	}
	before := m.prefs.display
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliver(t, m, cmd)
	if !reflect.DeepEqual(m.prefs.display, before) {
		t.Error("accepting nothing should change nothing")
	}
	if m.picker.active() {
		t.Error("accepting nothing should close the picker rather than trapping you")
	}
}

// Keys the picker does not claim must not fall through to the pane underneath.
func TestPickerCapturesKeys(t *testing.T) {
	t.Parallel()

	m := withPicker(t, listSpec(true), pickerPeopleFixture)
	m.compose.input = "draft"
	before := m.focus
	for _, key := range []tea.KeyPressMsg{
		keyText("x"), keyText("r"), keyText("?"), keyText("q"),
	} {
		next, _ := press(t, m, key)
		if next.compose.input != "draft" {
			t.Errorf("%q reached the composer: input = %q", key.String(), next.compose.input)
		}
		if next.focus != before || next.reader.showing(readerHelp) {
			t.Errorf("%q leaked past the picker", key.String())
		}
	}
}

// The grid pads the pane and counts items; the list shows labels with details and
// draws a logical RTL label in visual order.
func TestPickerRenderers(t *testing.T) {
	t.Parallel()

	grid := withPicker(t, gridSpec(false), newModel().emojiPickerItems(newModel().emojiRows(curated.all)))
	grid.width, grid.height = 96, 14
	lines := grid.pickerLines(60, 8)
	if len(lines) != 8 {
		t.Errorf("grid rendered %d lines, want the pane padded to 8", len(lines))
	}
	if !strings.Contains(stripStyles(lines[0]), "of "+strconv.Itoa(len(curated.all))) {
		t.Errorf("header should count the items, got %q", stripStyles(lines[0]))
	}

	list := withPicker(t, listSpec(false), []pickerItem{
		{label: "Alice Cohen", detail: "@alice:x", value: "@alice:x"},
		{label: "יבגני שטוקה", detail: "@rtl:x", value: "@rtl:x"},
	})
	body := stripStyles(strings.Join(list.pickerLines(60, 6), "\n"))
	for _, want := range []string{"Alice Cohen", "@alice:x"} {
		if !strings.Contains(body, want) {
			t.Errorf("the list should show %q, got:\n%s", want, body)
		}
	}
	if !strings.Contains(body, displayName("יבגני שטוקה")) || strings.Contains(body, "יבגני שטוקה") {
		t.Errorf("the row should draw the logical label in visual order, got:\n%s", body)
	}
}

// Producers hand over logical text: the label is the isolated name, matched as typed.
func TestPeopleItemsReorderTheNameAndFilterOnTheTyped(t *testing.T) {
	t.Parallel()

	const logical = "יבגני שטוקה"
	m := newModel()
	m.timeline.members = []domain.Member{{UserID: "@rtl:x", DisplayName: logical}}
	items := m.peopleItems()
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].label != isolate(logical) {
		t.Errorf("label = %q, want the logical name, isolated — the picker draws it", items[0].label)
	}
	if !strings.Contains(items[0].matchText(), logical) {
		t.Errorf("match = %q, want the logical name so typing finds it", items[0].matchText())
	}
}

// The picker's cursor arithmetic, without a Model.
func TestPickerMoveIsItsOwnArithmetic(t *testing.T) {
	t.Parallel()

	list := func(n int) picker {
		p := picker{kind: pickerJump}
		for i := range n {
			p.items = append(p.items, pickerItem{label: string(rune('a' + i)), value: string(rune('a' + i))})
		}
		p.all = p.items
		return p
	}

	for _, tc := range []struct {
		name    string
		act     action
		perRow  int
		count   int
		from    int
		want    int
		handled bool
	}{
		{"down one", actDown, 1, 1, 0, 1, true},
		{"down a count", actDown, 1, 3, 0, 3, true},
		{"up clamps at the top", actUp, 1, 5, 2, 0, true},
		{"a grid row is perRow items", actDown, 4, 1, 0, 4, true},
		{"a count with the end key means that row", actSelectNewest, 1, 4, 0, 3, true},
		{"the end key alone means the end", actSelectNewest, 1, 1, 0, 9, true},
		{"an unbound action is not ours", actHelp, 1, 1, 3, 3, false},
		{"a page is the rows on screen", actPageDown, 1, 1, 0, 3, true},
		{"a grid page is rows of perRow", actPageDown, 2, 1, 0, 6, true},
		{"half a page", actHalfPageDown, 1, 1, 0, 1, true},
		{"a page up clamps at the top", actPageUp, 1, 1, 2, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := list(10)
			p.cursor = tc.from
			got, handled := p.move(tc.act, tc.perRow, tc.count, 3)
			if handled != tc.handled {
				t.Fatalf("handled = %v, want %v", handled, tc.handled)
			}
			if got.cursor != tc.want {
				t.Errorf("cursor = %d, want %d", got.cursor, tc.want)
			}
		})
	}
}

// A list picker has no header until a filter is typed; the rows already name the
// selection.
func TestAListPickerHasNoHeaderUntilYouType(t *testing.T) {
	t.Parallel()

	m, _ := filing(t)
	m, _ = press(t, m, keyText("S"))
	if got := m.pickerHeader(); got != "" {
		t.Errorf("header = %q, want nothing — the rows already say it", got)
	}

	lines := m.pickerLines(60, 8)
	if len(lines) == 0 || !strings.Contains(lines[0], "[") || !strings.Contains(lines[0], cursor(true)) {
		t.Errorf("first line = %q, want the cursor already on the first checkbox row", lines[0])
	}

	m, _ = press(t, m, keyText("i")) // into filter mode
	m, _ = press(t, m, keyText("W"))
	if got := m.pickerHeader(); !strings.Contains(got, "filter: W") {
		t.Errorf("header = %q, want it to show what was typed", got)
	}
}

// A grid keeps its header: the shortcode has nowhere else to live.
func TestTheEmojiGridKeepsItsHeader(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.picker = newPickerWith(pickerEmoji, pickerSpec{title: "Emoji", grid: true}, []pickerItem{
		{label: "😀", detail: ":grinning:", value: "😀"},
	})
	if got := m.pickerHeader(); !strings.Contains(got, ":grinning:") {
		t.Errorf("header = %q, want the shortcode the cell cannot show", got)
	}
}
