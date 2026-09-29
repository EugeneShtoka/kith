package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A search prompt with a room's membership loaded, so `from:` has people to offer.
func completingSearch(t *testing.T) Model {
	t.Helper()
	m, _ := searching(t, false)
	m = update(t, m, membersMsg{roomID: "!a:x", members: []domain.Member{
		{UserID: "@dana:x", DisplayName: "Dana"},
		{UserID: "@dan:x", DisplayName: "Dan Other"},
		{UserID: "@bob:x", DisplayName: "Bob"},
	}})
	return m
}

func typePrompt(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		next, cmd := press(t, m, keyText(string(r)))
		m = next
		if cmd != nil {
			m = update(t, m, cmd())
		}
	}
	return m
}

// `from:` opens the people list the way `@` does in the composer.
func TestFromOpensTheCompletionPopup(t *testing.T) {
	m := completingSearch(t)
	m = typePrompt(t, m, "from:")
	if !m.completion.active {
		t.Fatal("from: did not open the popup")
	}
	if m.completion.trigger != "from:" {
		t.Errorf("trigger = %q, want from:", m.completion.trigger)
	}
	if len(m.completion.candidates) != 3 {
		t.Errorf("candidates = %d, want the room's three people", len(m.completion.candidates))
	}
	m = typePrompt(t, m, "dan")
	if len(m.completion.candidates) != 2 {
		t.Errorf("after 'dan': %d candidates, want Dana and Dan Other", len(m.completion.candidates))
	}
}

// Accepting inserts the MXID after `from:` (names are ambiguous).
func TestAcceptingFromInsertsTheMXID(t *testing.T) {
	m := completingSearch(t)
	m = typePrompt(t, m, "invoice from:dana")
	if !m.completion.active {
		t.Fatal("popup closed before accepting")
	}
	next, _ := m.acceptCompletion()
	m = next

	if got := m.editorFor(fieldPrompt).text; got != "invoice from:@dana:x" {
		t.Errorf("prompt = %q, want the filter kept and the MXID after it", got)
	}
	if m.completion.active {
		t.Error("the popup should close on accept")
	}
	// A search filter is not a mention: nobody is being notified.
	if len(m.compose.drafted) != 0 {
		t.Errorf("completing a filter drafted a mention: %v", m.compose.drafted)
	}
}

// The emoji trigger must not fire on the colon in `from:` — longest trigger wins.
func TestFromDoesNotOpenTheEmojiList(t *testing.T) {
	m := completingSearch(t)
	m = typePrompt(t, m, "from:")
	if m.completion.trigger == ":" {
		t.Error("the colon in from: opened the emoji list")
	}
}

// The search keeps running underneath the popup.
func TestSearchRunsWhileCompleting(t *testing.T) {
	m := completingSearch(t)
	before := len(m.search.query)
	m = typePrompt(t, m, "from:dan")
	if len(m.search.query) <= before {
		t.Error("the query did not track what was typed while the popup was open")
	}
	if !strings.Contains(m.search.query, "from:dan") {
		t.Errorf("search query = %q", m.search.query)
	}
}

// Popup keys are its own only while it is open; enter must still submit.
func TestPromptKeysSurviveTheCompletionPopup(t *testing.T) {
	m := completingSearch(t)
	m = typePrompt(t, m, "deploy")
	if m.completion.active {
		t.Fatal("plain text should not open a popup here")
	}
	after, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if after.prompt.active() {
		t.Error("enter did not submit the search")
	}
}

// esc and enter work at every stage (the popup once swallowed both while closed).
func TestEscAndEnterAlwaysReachTheSearchPrompt(t *testing.T) {
	for _, typed := range []string{"", "deploy", "invoice from:dan"} {
		t.Run("after "+typed, func(t *testing.T) {
			m := completingSearch(t)
			m = typePrompt(t, m, typed)

			// With the popup open, enter accepts first — at most two presses.
			submitted, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if submitted.prompt.active() {
				if !m.completion.active {
					t.Fatal("enter did not submit the search")
				}
				submitted, _ = press(t, submitted, tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if submitted.prompt.active() {
				t.Error("enter did not submit the search")
			}
			if !submitted.search.active {
				t.Error("submitting closed the results as well as the prompt")
			}

			// With the popup open, esc dismisses it first.
			back := m
			for range 3 {
				next, _ := press(t, back, tea.KeyPressMsg{Code: tea.KeyEsc})
				back = next
				if !back.search.active && !back.prompt.active() {
					break
				}
			}
			if back.search.active || back.prompt.active() {
				t.Error("esc never closed the search")
			}
		})
	}
}

// The popup is drawn over the results pane, where the search is.
func TestFromPopupIsVisibleOverTheResults(t *testing.T) {
	m := completingSearch(t)
	m = typePrompt(t, m, "from:dan")
	if !m.completion.active {
		t.Fatal("popup closed before it could be drawn")
	}
	view := stripStyles(m.View().Content)
	for _, want := range []string{"Dana", "Dan Other"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view does not show candidate %q", want)
		}
	}
	// The results are still there underneath.
	if !strings.Contains(view, "Search") {
		t.Error("the popup replaced the results pane instead of sitting in it")
	}
}

// `from:` offers the scope's senders; the open room's members fill in until they
// arrive.
func TestFromOffersTheScopesSendersNotJustTheRoom(t *testing.T) {
	m := completingSearch(t)
	m = update(t, m, searchSendersMsg{scope: m.search.scope, senders: []domain.Member{
		{UserID: "@elsewhere:x", DisplayName: "Elsa Elsewhere"},
	}})

	// Somebody who has posted in the scope but is in none of this room's membership.
	offered := typePrompt(t, m, "from:els")
	if !offered.completion.active {
		t.Fatal("a sender from the wider scope was not offered")
	}
	if got := offered.completion.candidates[0].text; got != "@elsewhere:x" {
		t.Errorf("first candidate = %q, want the scope's sender", got)
	}

	// The open room's members are still reachable.
	local := typePrompt(t, m, "from:bob")
	if !local.completion.active {
		t.Fatal("the open room's members stopped being offered")
	}
	if got := local.completion.candidates[0].text; got != "@bob:x" {
		t.Errorf("first candidate = %q, want the room's member", got)
	}
}

// An answer for a scope cycled past is dropped.
func TestStaleSendersAreDropped(t *testing.T) {
	m := completingSearch(t)
	stale := m.search.scope.next()
	m = update(t, m, searchSendersMsg{scope: stale, senders: []domain.Member{
		{UserID: "@elsewhere:x", DisplayName: "Elsa Elsewhere"},
	}})
	if len(m.search.people) != 0 {
		t.Errorf("kept %+v for a scope that is no longer current", m.search.people)
	}
}

// Widening the scope re-asks who it reaches.
func TestCyclingScopeReasksForSenders(t *testing.T) {
	m, backend := searching(t, false)
	before := len(backend.senderScopes)
	next, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	_ = deliver(t, next, cmd)
	if len(backend.senderScopes) <= before {
		t.Error("cycling the scope did not re-ask who it covers")
	}
}
