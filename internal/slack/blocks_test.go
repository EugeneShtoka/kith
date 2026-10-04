package slack

import (
	"encoding/json"
	"strings"
	"testing"

	slackgo "github.com/slack-go/slack"
)

// decoded is a message as Slack sends it, through slack-go's own decoding.
func decoded(t *testing.T, raw string) slackgo.Msg {
	t.Helper()
	var m slackgo.Msg
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// An app's post of blocks alone reads: its header, its section and fields, its
// context line — the words, formatted as mrkdwn would be.
func TestAnAppsBlocksRead(t *testing.T) {
	t.Parallel()
	m := decoded(t, `{"type":"message","subtype":"bot_message","bot_id":"B1","username":"AlertManager","ts":"5.0","text":"",
	 "blocks":[
	  {"type":"header","text":{"type":"plain_text","text":"FIRING: TargetDown"}},
	  {"type":"section","text":{"type":"mrkdwn","text":"*backend* is down <https://grafana.example/d/1|dashboard>"},
	   "fields":[{"type":"mrkdwn","text":"*Severity:* warning"},{"type":"plain_text","text":"ns <production>"}]},
	  {"type":"context","elements":[{"type":"mrkdwn","text":"9:08 PM"}]},
	  {"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Silence"}}]}
	 ]}`)
	got, ok := incoming("C1", &m, acmeNames())
	if !ok {
		t.Fatal("an app's post of blocks alone was dropped")
	}
	for _, want := range []string{"FIRING: TargetDown", "backend is down dashboard", "Severity: warning", "ns <production>", "9:08 PM"} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("body %q lacks %q", got.Body, want)
		}
	}
	if strings.Contains(got.Body, "Silence") {
		t.Errorf("a button's words reached the body: %q", got.Body)
	}
	if !strings.Contains(got.Format.Markup(), `<a href="https://grafana.example/d/1">dashboard</a>`) {
		t.Errorf("markup %q lacks the link", got.Format.Markup())
	}
}

// Rich text reads as the person wrote it: styles, links, mentions, emoji as characters,
// lists, quotes and code.
func TestRichTextReads(t *testing.T) {
	t.Parallel()
	m := decoded(t, `{"type":"message","user":"U2","ts":"6.0","text":"","blocks":[{"type":"rich_text","elements":[
	  {"type":"rich_text_section","elements":[
	    {"type":"text","text":"ship "},{"type":"text","text":"it","style":{"bold":true}},
	    {"type":"text","text":" now ","style":{"italic":true}},{"type":"user","user_id":"U2"},
	    {"type":"text","text":" see "},{"type":"link","url":"https://x.io","text":"docs"},
	    {"type":"text","text":" "},{"type":"emoji","name":"thumbsup","unicode":"1f44d"},
	    {"type":"emoji","name":"partyparrot"}]},
	  {"type":"rich_text_list","style":"ordered","elements":[
	    {"type":"rich_text_section","elements":[{"type":"text","text":"one"}]},
	    {"type":"rich_text_section","elements":[{"type":"text","text":"two"}]}]},
	  {"type":"rich_text_quote","elements":[{"type":"text","text":"quoted"}]},
	  {"type":"rich_text_preformatted","elements":[{"type":"text","text":"a < b"}]}
	]}]}`)
	got, ok := incoming("C1", &m, acmeNames())
	if !ok {
		t.Fatal("rich text alone was dropped")
	}
	for _, want := range []string{"ship it now @Dana see docs 👍:partyparrot:", "1. one", "2. two", "> quoted", "a < b"} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("body %q lacks %q", got.Body, want)
		}
	}
	markup := got.Format.Markup()
	for _, want := range []string{"<b>it</b>", "<i>now</i>", `<a href="https://x.io">docs</a>`, "<code>a &lt; b</code>"} {
		if !strings.Contains(markup, want) {
			t.Errorf("markup %q lacks %q", markup, want)
		}
	}
	if len(got.Mentions) != 1 || got.Mentions[0].UserID != "slack:T1.U2" {
		t.Errorf("mentions = %+v, want Dana", got.Mentions)
	}

	// A message with text reads from its text: the blocks beside it say the same.
	both := decoded(t, `{"type":"message","user":"U2","ts":"7.0","text":"the text",
	  "blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"the blocks"}]}]}]}`)
	if got, _ := incoming("C1", &both, acmeNames()); got.Body != "the text" {
		t.Errorf("body = %q, want the text", got.Body)
	}
}
