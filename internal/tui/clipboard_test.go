package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// yanking returns a model at the message cursor over the given messages, newest last.
func yanking(t *testing.T, bodies ...domain.Message) Model {
	t.Helper()
	m := update(t, starterNew(apitest.Nop{}, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: bodies}})
	m.focus, m.compose.insertMode = paneTimeline, false
	m = m.clearStatus()
	return m
}

func msgWith(id, body string) domain.Message {
	return domain.Message{ID: domain.EventID(id), RoomID: "!a:x", Sender: "@a:x", SenderName: "Alice", Body: body}
}

// A copy is confirmed on one line: OSC 52 can fail silently, so the status is the
// only evidence.
func TestCopyTextConfirms(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "the quick brown fox"))
	m, cmd := chord(t, m, "y", "y")
	if cmd == nil {
		t.Fatal("y should produce a clipboard command")
	}
	if !strings.Contains(m.status(), "copied") || !strings.Contains(m.status(), "quick brown fox") {
		t.Errorf("status = %q, should confirm what was copied", m.status())
	}

	long := yanking(t, msgWith("$1", "first line\n\nsecond line "+strings.Repeat("x", 200)))
	long, _ = chord(t, long, "y", "y")
	if strings.Contains(long.status(), "\n") || len([]rune(long.status())) > 80 {
		t.Errorf("status must be one short line, got %q", long.status())
	}
}

func TestCopyTextNothingToCopy(t *testing.T) {
	t.Parallel()

	redacted := yanking(t, domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Redacted: true})
	redacted, cmd := chord(t, redacted, "y", "y")
	if untimed(t, cmd) != nil {
		t.Error("a deleted message should not be copied")
	}
	if !strings.Contains(redacted.status(), "deleted") {
		t.Errorf("status = %q, should say why", redacted.status())
	}

	blank := yanking(t, msgWith("$1", "   "))
	blank, cmd = chord(t, blank, "y", "y")
	if untimed(t, cmd) != nil {
		t.Error("an empty body should not be copied")
	}
	if !strings.Contains(blank.status(), "nothing to copy") {
		t.Errorf("status = %q", blank.status())
	}
}

// One link acts directly; several ask.
func TestCopyLinkOneOrAsk(t *testing.T) {
	t.Parallel()

	one := yanking(t, msgWith("$1", "see https://example.org/docs for details"))
	one, cmd := chord(t, one, "y", "u")
	if one.picker.active() {
		t.Error("a single link should be copied directly, without asking")
	}
	if cmd == nil || !strings.Contains(one.status(), "https://example.org/docs") {
		t.Errorf("status = %q", one.status())
	}

	many := yanking(t, msgWith("$1", "a https://one.org b https://two.org c https://three.org"))
	many, _ = chord(t, many, "y", "u")
	if many.picker.kind != pickerContext || many.picker.then != actCopyURL {
		t.Fatalf("three links should open the copy chooser, got %v/%v", many.picker.kind, many.picker.then)
	}
	if len(many.picker.items) != 3 {
		t.Errorf("picker has %d items, want 3", len(many.picker.items))
	}
	if many.picker.items[0].detail != "one.org" {
		t.Errorf("detail = %q, want the host", many.picker.items[0].detail)
	}
	picked, cmd := press(t, many, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("choosing a link should copy it")
	}
	if !strings.Contains(picked.status(), "https://one.org") {
		t.Errorf("status = %q", picked.status())
	}
	if picked.picker.active() {
		t.Error("choosing should close the picker")
	}
}

// The three link keys open the same list; the key alone stamps what accepting does.
func TestLinkPickerIntentFollowsTheKey(t *testing.T) {
	t.Parallel()

	body := msgWith("$1", "a https://one.org b https://two.org")
	for _, tc := range []struct {
		keys   []string
		then   action
		title  string
		status string
	}{
		{[]string{"y", "u"}, actCopyURL, "Copy", "copied"},
		{[]string{"g", "x"}, actOpenURL, "Open", "opening"},
		{[]string{"g", "X"}, actOpenURLFocus, "Go to", "opening"},
	} {
		m, _ := chord(t, yanking(t, body), tc.keys...)
		if m.picker.then != tc.then || len(m.picker.items) != 2 {
			t.Fatalf("%v: intent %v with %d items", tc.keys, m.picker.then, len(m.picker.items))
		}
		if !strings.Contains(m.picker.spec.title, tc.title) {
			t.Errorf("%v: title = %q", tc.keys, m.picker.spec.title)
		}
		done, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil || done.picker.active() || !strings.Contains(done.status(), tc.status) {
			t.Errorf("%v: accepting gave status %q, picker open %v", tc.keys, done.status(), done.picker.active())
		}
	}

	one, cmd := chord(t, yanking(t, msgWith("$1", "read https://one.org today")), "g", "X")
	if cmd == nil || !strings.Contains(one.status(), "opening") {
		t.Errorf("gX on one link: status = %q", one.status())
	}
}

// A message with no web link says so. Non-web schemes are never extracted, so they
// never reach open() at all.
func TestLinkActionsWithNoLink(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"no links in here at all",
		"file:///etc/passwd javascript:alert(1) mxc://s/m myapp://x",
	} {
		for _, keys := range [][]string{{"y", "u"}, {"g", "x"}, {"g", "X"}} {
			m, cmd := chord(t, yanking(t, msgWith("$1", body)), keys...)
			if untimed(t, cmd) != nil || m.picker.active() {
				t.Errorf("%q on %q should do nothing", strings.Join(keys, ""), body)
			}
			if !strings.Contains(m.status(), "no link") {
				t.Errorf("%q: status = %q, should say there is no link", strings.Join(keys, ""), m.status())
			}
		}
	}
}

// The scheme check is the security boundary: links come from other people's messages.
func TestOpenRefusesNonWebSchemes(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "text"))
	for _, link := range []string{
		"file:///etc/passwd", "javascript:alert(1)", "data:text/html,<script>",
		"myapp://run", "", "ftp://x.org",
	} {
		got, cmd := m.open(link)
		if cmd != nil {
			t.Errorf("open(%q) produced a command — only http and https may be opened", link)
		}
		if !strings.Contains(got.status(), "refusing") {
			t.Errorf("open(%q): status = %q, should refuse explicitly", link, got.status())
		}
	}
	got, cmd := m.open("https://example.org")
	if cmd == nil {
		t.Error("an https link should be opened")
	}
	if !strings.Contains(got.status(), "example.org") {
		t.Errorf("status = %q, should name where it is going", got.status())
	}
}

// The fallback sink runs alongside OSC 52, not instead of it.
func TestClipboardFallbackSink(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "text to copy"))
	if _, plain := chord(t, m, "y", "y"); plain == nil {
		t.Fatal("y should always produce the OSC 52 write")
	}
	m.prefs.external.clipboard = "true"
	_, both := chord(t, m, "y", "y")
	if both == nil {
		t.Fatal("with a sink configured there should still be a command")
	}
	both() // must not panic or block
}

func TestClipboardConfigReachesTheModel(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Clipboard: config.Clipboard{OpenCommand: "my-browser", Command: "wl-copy"}}
	m := yanking(t, msgWith("$1", "text")).WithConfigFile("/tmp/x.toml", cfg).keptIn()
	if m.prefs.external.open != "my-browser" || m.prefs.external.clipboard != "wl-copy" {
		t.Errorf("external.open = %q, external.clipboard = %q", m.prefs.external.open, m.prefs.external.clipboard)
	}
}

func TestCopyCode(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "Your verification code is 482910"))
	m, cmd := chord(t, m, "y", "c")
	if cmd == nil {
		t.Fatal("c should produce a clipboard command")
	}
	if !strings.Contains(m.status(), "code") || !strings.Contains(m.status(), "482910") {
		t.Errorf("status = %q, should confirm the code", m.status())
	}
	if m.picker.active() {
		t.Error("one code should be copied directly, without asking")
	}
}

// Several plausible codes ask, qualified by the keyword that vouched for each.
func TestCopyCodeAsksWhenSeveral(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "code 482910 for login, pin 7391 for the safe"))
	m, _ = chord(t, m, "y", "c")
	if m.picker.kind != pickerContext || m.picker.then != actCopyCode {
		t.Fatalf("two codes should open the code chooser, got %v/%v", m.picker.kind, m.picker.then)
	}
	if len(m.picker.items) != 2 || m.picker.items[0].label != "482910" {
		t.Fatalf("items = %+v", m.picker.items)
	}
	if !strings.Contains(m.picker.items[0].detail, "code") {
		t.Errorf("detail = %q, should say what vouched for it", m.picker.items[0].detail)
	}
	picked, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("choosing a code should copy it")
	}
	if !strings.Contains(picked.status(), "482910") || picked.picker.active() {
		t.Errorf("status = %q, picker open = %v", picked.status(), picked.picker.active())
	}
}

// The code key is advertised only on a message that has a code.
func TestCodeHintFollowsTheMessage(t *testing.T) {
	t.Parallel()

	withCode := yanking(t, msgWith("$1", "Your code is 482910"))
	if !strings.Contains(withCode.hints(), "copy code") {
		t.Errorf("legend = %q, should advertise the code key", withCode.hints())
	}
	without := yanking(t, msgWith("$1", "see you at the standup"))
	if strings.Contains(without.hints(), "copy code") {
		t.Errorf("legend = %q, should not advertise it", without.hints())
	}
}

// While composing, the yank letters are text.
func TestYankKeysTypeWhileComposing(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "see https://example.org and code 482910"))
	m.compose.insertMode = true
	for _, r := range "yuUc" {
		m, _ = press(t, m, keyText(string(r)))
	}
	if m.compose.input != "yuUc" {
		t.Errorf("input = %q — the yank keys must type while composing", m.compose.input)
	}
	if m.picker.active() {
		t.Error("no picker should open from the composer")
	}
}

// "No code" names the configured rules it looked with, and copies nothing.
func TestNoCodeStatusNamesWhatItLookedFor(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.prefs.codes.rules = domain.CodeRules{MinLength: 6, MaxLength: 6, Digits: true, RequireDigit: true}
	m = m.setMessages([]domain.Message{{ID: "$1", RoomID: "!a:x", Body: "nothing code-shaped here"}})
	m.focus, m.compose.insertMode = paneTimeline, false

	next, cmd := chord(t, m, "y", "c")
	if untimed(t, cmd) != nil {
		t.Error("nothing should reach the clipboard")
	}
	for _, want := range []string{"no code", "6 digits", "[codes]"} {
		if !strings.Contains(next.status(), want) {
			t.Errorf("status = %q, want it to contain %q", next.status(), want)
		}
	}
}

// ctrl+x cuts the whole draft, stays in the composer, and ctrl+z brings it back.
func TestCutTakesTheDraftToTheClipboard(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "hello"))
	m, _ = press(t, m, keyText("i"))
	for _, r := range "a whole thought" {
		m, _ = press(t, m, keyText(string(r)))
	}
	if m.compose.input != "a whole thought" {
		t.Fatalf("composer holds %q", m.compose.input)
	}

	cut, cmd := press(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if cut.compose.input != "" {
		t.Errorf("composer still holds %q after ctrl+x", cut.compose.input)
	}
	if cmd == nil {
		t.Error("nothing was sent to the clipboard")
	}
	if !strings.Contains(cut.status(), "a whole thought") {
		t.Errorf("status = %q, want it to name what was cut", cut.status())
	}
	if !cut.compose.insertMode {
		t.Error("cutting dropped out of the composer")
	}

	back, _ := press(t, cut, tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if back.compose.input != "a whole thought" {
		t.Errorf("ctrl+z gave %q, want the draft back", back.compose.input)
	}
}

// An empty field must not overwrite the clipboard with nothing.
func TestCutWithNothingToCut(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "hello"))
	m, _ = press(t, m, keyText("i"))
	cut, cmd := press(t, m.clearStatus(), tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if untimed(t, cmd) != nil {
		t.Error("an empty draft was sent to the clipboard")
	}
	if !strings.Contains(cut.status(), "nothing to cut") {
		t.Errorf("status = %q, want it to say there was nothing to cut", cut.status())
	}
}

// [clipboard] copy_downloads puts a saved file's path on the clipboard too; a failed
// save copies nothing.
func TestSavingCanCopyThePath(t *testing.T) {
	t.Parallel()

	m := yanking(t, msgWith("$1", "here it is"))
	off, cmd := m.handleDownloaded(downloadedMsg{path: "/home/eugene/Downloads/plan.pdf"})
	if cmd != nil {
		t.Error("the default copied something to the clipboard")
	}
	if !strings.Contains(off.status(), "/home/eugene/Downloads/plan.pdf") {
		t.Errorf("status = %q, want the path reported", off.status())
	}

	m.prefs.external.copyDownloads = true
	on, cmd := m.handleDownloaded(downloadedMsg{path: "/home/eugene/Downloads/plan.pdf"})
	if cmd == nil {
		t.Fatal("nothing was sent to the clipboard with copy_downloads on")
	}
	if !strings.Contains(on.status(), "plan.pdf") || !strings.Contains(on.status(), "copied") {
		t.Errorf("status = %q, want one line saying where it went and that it was copied", on.status())
	}

	failed, cmd := m.handleDownloaded(downloadedMsg{err: errors.New("no space left on device")})
	if cmd != nil {
		t.Error("a failed save wrote to the clipboard")
	}
	if !strings.Contains(failed.status(), "could not save") {
		t.Errorf("status = %q", failed.status())
	}
}
