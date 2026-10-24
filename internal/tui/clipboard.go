package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Copying uses OSC 52 (tea.SetClipboard), which sets the clipboard of the terminal
// you are looking at, not the host (tmux, remote). tmux needs `set -g set-clipboard on`;
// `[clipboard] command` is the fallback and gets the text on stdin, never argv.

type externalCommands struct {
	// clipboard is the fallback when OSC 52 is blocked; open opens links.
	clipboard string
	open      string
	// focus raises the browser after opening a link; empty asks the desktop.
	focus string
	// raise brings this window forward when a link is handed over; empty does nothing.
	raise string
	// copyDownloads also copies a saved attachment's path ([clipboard] copy_downloads).
	copyDownloads bool
}

// copyMessageText copies the selected message's body. For an attachment that is the
// caption, or the file name when there is none (MSC2530).
func (m Model) copyMessageText() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	switch {
	case msg.Redacted:
		m = m.say("that message was deleted — nothing to copy")
		return m, nil
	case strings.TrimSpace(msg.Body) == "":
		m = m.say("nothing to copy")
		return m, nil
	}
	return m.copy(msg.Body, describeCopy("message", msg.Body))
}

// codeItems turns codes into picker rows, qualified by the keyword that vouched for
// each so similar numbers can be told apart.
func codeItems(codes []domain.Code) []pickerItem {
	items := make([]pickerItem, 0, len(codes))
	for _, code := range codes {
		detail := "no keyword"
		if code.Label != "" {
			detail = "near \"" + code.Label + "\""
		}
		items = append(items, pickerItem{
			label:  isolate(code.Value),
			detail: detail,
			value:  code.Value,
			match:  code.Value,
		})
	}
	return items
}

// hasCode reports whether the legend advertises the code key for the selected
// message. A hint, not a highlight: the RTL renderer cannot highlight inside reordered
// text. Uses the unprompted rules and scope; the key itself always works.
func (m Model) hasCode() bool {
	msg, ok := m.selectedMessage()
	return ok && m.codesHere(msg.RoomID) && len(domain.Codes(msg.Body, m.prefs.codes.rules)) > 0
}

// linkItems turns links into picker rows, qualified by host.
func linkItems(links []string) []pickerItem {
	items := make([]pickerItem, 0, len(links))
	for _, link := range links {
		items = append(items, pickerItem{
			label:  isolate(link),
			detail: hostOf(link),
			value:  link,
			match:  link,
		})
	}
	return items
}

func hostOf(link string) string {
	rest := strings.TrimPrefix(strings.TrimPrefix(link, "https://"), "http://")
	host, _, _ := strings.Cut(rest, "/")
	return host
}

// copy puts text on the clipboard and says so: OSC 52 can fail silently.
func (m Model) copy(text, said string) (Model, tea.Cmd) {
	m = m.say(said)
	cmds := []tea.Cmd{tea.SetClipboard(text)}
	if m.prefs.external.clipboard != "" {
		cmds = append(cmds, m.clipboardSinkCmd(text))
	}
	return m, tea.Batch(cmds...)
}

// cutDraft moves the whole draft to the clipboard as one undo step. Terminals handle
// ctrl+c/ctrl+v themselves; cut they cannot.
func (m Model) cutDraft() (Model, tea.Cmd) {
	ed := m.editorFor(fieldComposer)
	if ed.text == "" {
		return m.say("nothing to cut"), nil
	}
	text := ed.text
	m = m.store(fieldComposer, ed.cleared())
	// Mentions go with the words, or they would attach to what is typed next.
	m.compose.drafted = nil
	next, cmd := m.copy(text, describeCopy("the draft", text))
	// Clear the composer's own copy too, or the next enter sends the cut text.
	typed, typing := next.composerTyped("")
	return typed, tea.Batch(cmd, typing)
}

// open hands a link to the desktop's URL handler. Links come from senders, so only
// http/https, passed as one argv element, never through a shell.
func (m Model) open(link string) (Model, tea.Cmd) {
	if !domain.OpenableLink(link) {
		m = m.say("refusing to open that — only http and https links")
		return m, nil
	}
	m = m.say("opening " + hostOf(link) + "…")
	return m, m.openLinkCmd(link)
}

// openFocused opens a link and raises the browser window.
func (m Model) openFocused(link string) (Model, tea.Cmd) {
	next, cmd := m.open(link)
	if cmd == nil {
		return next, nil // refused, and it has already said why
	}
	return next, tea.Batch(cmd, next.focusBrowserCmd())
}

// describeCopy is the status line for a copy, with a one-line preview of the value.
func describeCopy(what, value string) string {
	flat := strings.Join(strings.Fields(value), " ")
	return fmt.Sprintf("copied %s: %s", what, isolate(truncateLogical(flat, 48)))
}
