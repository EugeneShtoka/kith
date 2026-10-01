package matrix

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestToDomainMessage(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		evt    *event.Event
		want   domain.Message
		wantOK bool
	}{
		"text message translates": {
			evt: &event.Event{
				ID:        id.EventID("$evt:x"),
				RoomID:    id.RoomID("!r:x"),
				Sender:    id.UserID("@bob:x"),
				Type:      event.EventMessage,
				Timestamp: 1700000000000,
				Content: event.Content{Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "hello",
				}},
			},
			want:   domain.Message{ID: "$evt:x", RoomID: "!r:x", Sender: "@bob:x", Body: "hello", Timestamp: time.UnixMilli(1700000000000)},
			wantOK: true,
		},
		"empty content is skipped": {
			evt: &event.Event{
				RoomID:  id.RoomID("!r:x"),
				Sender:  id.UserID("@bob:x"),
				Content: event.Content{},
			},
			wantOK: false,
		},
		"edit folds onto its target with the new body": {
			evt: &event.Event{
				ID:        id.EventID("$edit:x"),
				RoomID:    id.RoomID("!r:x"),
				Sender:    id.UserID("@bob:x"),
				Type:      event.EventMessage,
				Timestamp: 1700000009000,
				Content: event.Content{Parsed: &event.MessageEventContent{
					MsgType:    event.MsgText,
					Body:       "* new text",
					RelatesTo:  &event.RelatesTo{Type: event.RelReplace, EventID: id.EventID("$orig:x")},
					NewContent: &event.MessageEventContent{MsgType: event.MsgText, Body: "new text"},
				}},
			},
			want:   domain.Message{ID: "$orig:x", RoomID: "!r:x", Sender: "@bob:x", Body: "new text", RevisionID: "$edit:x", Timestamp: time.UnixMilli(1700000009000), EditedAt: time.UnixMilli(1700000009000), Edited: true},
			wantOK: true,
		},
		"edit without new_content strips the fallback star": {
			evt: &event.Event{
				ID:        id.EventID("$edit2:x"),
				RoomID:    id.RoomID("!r:x"),
				Sender:    id.UserID("@bob:x"),
				Type:      event.EventMessage,
				Timestamp: 1700000009000,
				Content: event.Content{Parsed: &event.MessageEventContent{
					MsgType:   event.MsgText,
					Body:      "* fallback text",
					RelatesTo: &event.RelatesTo{Type: event.RelReplace, EventID: id.EventID("$orig:x")},
				}},
			},
			want:   domain.Message{ID: "$orig:x", RoomID: "!r:x", Sender: "@bob:x", Body: "fallback text", RevisionID: "$edit2:x", Timestamp: time.UnixMilli(1700000009000), EditedAt: time.UnixMilli(1700000009000), Edited: true},
			wantOK: true,
		},
		"reply keeps the target and strips the quoted fallback": {
			evt: &event.Event{
				ID:        id.EventID("$reply:x"),
				RoomID:    id.RoomID("!r:x"),
				Sender:    id.UserID("@bob:x"),
				Type:      event.EventMessage,
				Timestamp: 1700000000000,
				Content: event.Content{Parsed: &event.MessageEventContent{
					MsgType:   event.MsgText,
					Body:      "> <@alice:x> original\n\nmy reply",
					RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: id.EventID("$orig:x")}},
				}},
			},
			want:   domain.Message{ID: "$reply:x", RoomID: "!r:x", Sender: "@bob:x", Body: "my reply", Timestamp: time.UnixMilli(1700000000000), ReplyTo: "$orig:x"},
			wantOK: true,
		},
		"thread reply keeps its root and drops the falling-back reply": {
			evt: &event.Event{
				ID:        id.EventID("$reply:x"),
				RoomID:    id.RoomID("!r:x"),
				Sender:    id.UserID("@bob:x"),
				Type:      event.EventMessage,
				Timestamp: 1700000000000,
				Content: event.Content{Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@alice:x> root\n\nin the thread",
					RelatesTo: &event.RelatesTo{
						Type:          event.RelThread,
						EventID:       id.EventID("$root:x"),
						InReplyTo:     &event.InReplyTo{EventID: id.EventID("$prev:x")},
						IsFallingBack: true,
					},
				}},
			},
			want:   domain.Message{ID: "$reply:x", RoomID: "!r:x", Sender: "@bob:x", Body: "in the thread", Timestamp: time.UnixMilli(1700000000000), ThreadRoot: "$root:x"},
			wantOK: true,
		},
		"a real reply inside a thread keeps both pointers": {
			evt: &event.Event{
				ID:        id.EventID("$reply2:x"),
				RoomID:    id.RoomID("!r:x"),
				Sender:    id.UserID("@bob:x"),
				Type:      event.EventMessage,
				Timestamp: 1700000000000,
				Content: event.Content{Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "answering you",
					RelatesTo: &event.RelatesTo{
						Type:      event.RelThread,
						EventID:   id.EventID("$root:x"),
						InReplyTo: &event.InReplyTo{EventID: id.EventID("$target:x")},
					},
				}},
			},
			want:   domain.Message{ID: "$reply2:x", RoomID: "!r:x", Sender: "@bob:x", Body: "answering you", Timestamp: time.UnixMilli(1700000000000), ReplyTo: "$target:x", ThreadRoot: "$root:x"},
			wantOK: true,
		},
		"empty body is skipped": {
			evt:    evt("$0", "!r:x", "@bob:x", 1, &event.MessageEventContent{MsgType: event.MsgText}),
			wantOK: false,
		},
		"an edit with no body is dropped rather than blanking its target": {
			evt: evt("$edit", "!r:x", "@bob:x", 1, &event.MessageEventContent{
				MsgType:   event.MsgText,
				RelatesTo: &event.RelatesTo{Type: event.RelReplace, EventID: eventID("$orig")},
			}),
			wantOK: false,
		},
		"redacted event surfaces as deleted": {
			evt: &event.Event{
				ID:        id.EventID("$evt:x"),
				RoomID:    id.RoomID("!r:x"),
				Sender:    id.UserID("@bob:x"),
				Type:      event.EventMessage,
				Timestamp: 1700000000000,
				Content:   event.Content{Parsed: &event.MessageEventContent{}},
				Unsigned:  event.Unsigned{RedactedBecause: &event.Event{}},
			},
			want:   domain.Message{ID: "$evt:x", RoomID: "!r:x", Sender: "@bob:x", Timestamp: time.UnixMilli(1700000000000), Redacted: true},
			wantOK: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := toDomainMessage(tc.evt)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("msg = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRedactionTarget(t *testing.T) {
	t.Parallel()

	// Room v11 carries the target event in the redaction's content.
	v11 := &event.Event{Type: event.EventRedaction, Content: event.Content{
		Parsed: &event.RedactionEventContent{Redacts: id.EventID("$target:x")}}}
	if got := redactionTarget(v11); got != "$target:x" {
		t.Errorf("v11 target = %q, want $target:x", got)
	}

	// Older rooms carry it as the top-level redacts field (content empty).
	legacy := &event.Event{Type: event.EventRedaction, Redacts: id.EventID("$old:x"),
		Content: event.Content{Parsed: &event.RedactionEventContent{}}}
	if got := redactionTarget(legacy); got != "$old:x" {
		t.Errorf("legacy target = %q, want $old:x", got)
	}
}

func TestToReaction(t *testing.T) {
	t.Parallel()

	good := &event.Event{
		ID:     id.EventID("$r:x"),
		RoomID: id.RoomID("!r:x"),
		Sender: id.UserID("@a:x"),
		Type:   event.EventReaction,
		Content: event.Content{Parsed: &event.ReactionEventContent{
			RelatesTo: event.RelatesTo{Type: event.RelAnnotation, EventID: id.EventID("$m:x"), Key: "👍"},
		}},
	}
	got, ok := toReaction(good)
	if !ok {
		t.Fatal("valid reaction not parsed")
	}
	want := domain.Reaction{ID: "$r:x", RoomID: "!r:x", Target: "$m:x", Sender: "@a:x", Key: "👍"}
	if got != want {
		t.Errorf("toReaction = %+v, want %+v", got, want)
	}

	// An annotation missing its key (or target) is not a usable reaction.
	noKey := &event.Event{
		Type: event.EventReaction,
		Content: event.Content{Parsed: &event.ReactionEventContent{
			RelatesTo: event.RelatesTo{Type: event.RelAnnotation, EventID: id.EventID("$m:x")},
		}},
	}
	if _, ok := toReaction(noKey); ok {
		t.Error("reaction without a key should be ignored")
	}
}

func TestToDomainMessageMedia(t *testing.T) {
	t.Parallel()

	img := &event.Event{
		ID:        id.EventID("$img:x"),
		RoomID:    id.RoomID("!r:x"),
		Sender:    id.UserID("@a:x"),
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgImage,
			Body:    "cat.jpg",
			Info:    &event.FileInfo{MimeType: "image/jpeg", Width: 800, Height: 600, Size: 12345},
		}},
	}
	got, ok := toDomainMessage(img)
	if !ok || got.Media == nil {
		t.Fatalf("image not recognized as media: ok=%v media=%v", ok, got.Media)
	}
	if got.Media.Type != domain.MediaImage || got.Media.Name != "cat.jpg" ||
		got.Media.Mime != "image/jpeg" || got.Media.Width != 800 || got.Media.Height != 600 || got.Media.Size != 12345 {
		t.Errorf("media = %+v", got.Media)
	}

	// An image with no filename must still be kept, not dropped by the empty-body rule.
	noName := &event.Event{
		ID: id.EventID("$img2:x"), RoomID: id.RoomID("!r:x"), Sender: id.UserID("@a:x"),
		Type: event.EventMessage, Timestamp: 1700000000000,
		Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgImage, Body: ""}},
	}
	if _, kept := toDomainMessage(noName); !kept {
		t.Error("an image with no filename should not be dropped")
	}

	// MSC2530: `filename` is the file name and `body` the caption.
	captioned := &event.Event{
		ID: id.EventID("$img3:x"), RoomID: id.RoomID("!r:x"), Sender: id.UserID("@a:x"),
		Type: event.EventMessage, Timestamp: 1700000000000,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType:  event.MsgImage,
			Body:     "look at this sunset",
			FileName: "IMG_20260829.jpg",
			Info:     &event.FileInfo{MimeType: "image/jpeg"},
		}},
	}
	got, ok = toDomainMessage(captioned)
	if !ok || got.Media == nil {
		t.Fatalf("captioned image not recognized as media: ok=%v media=%v", ok, got.Media)
	}
	if got.Body != "look at this sunset" || got.Media.Name != "IMG_20260829.jpg" {
		t.Errorf("captioned image: body = %q, name = %q", got.Body, got.Media.Name)
	}
	if got.Caption() != "look at this sunset" {
		t.Errorf("caption = %q", got.Caption())
	}

	// `filename` with an empty body still names the chip.
	bodyless := &event.Event{
		ID: id.EventID("$img4:x"), RoomID: id.RoomID("!r:x"), Sender: id.UserID("@a:x"),
		Type: event.EventMessage, Timestamp: 1700000000000,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgFile, FileName: "invoice-2026.pdf",
		}},
	}
	got, ok = toDomainMessage(bodyless)
	if !ok || got.Media == nil {
		t.Fatalf("bodyless attachment dropped: ok=%v media=%v", ok, got.Media)
	}
	if got.Media.Name != "invoice-2026.pdf" || got.Caption() != "" {
		t.Errorf("bodyless attachment: name = %q, caption = %q", got.Media.Name, got.Caption())
	}
}

func TestMediaSource(t *testing.T) {
	t.Parallel()

	// A plaintext attachment carries an mxc URL and no decryption info.
	plain := &event.MessageEventContent{MsgType: event.MsgImage, URL: "mxc://x/plain"}
	if mxc, fileJSON := mediaSource(plain); mxc != "mxc://x/plain" || fileJSON != "" {
		t.Errorf("plaintext source = %q, %q", mxc, fileJSON)
	}

	// An encrypted attachment carries the mxc in File.URL plus JSON decryption info.
	enc := &event.MessageEventContent{MsgType: event.MsgImage, File: &event.EncryptedFileInfo{URL: "mxc://x/enc"}}
	mxc, fileJSON := mediaSource(enc)
	if mxc != "mxc://x/enc" || fileJSON == "" {
		t.Errorf("encrypted source = %q, %q (want non-empty JSON)", mxc, fileJSON)
	}
}

func TestParseMentions(t *testing.T) {
	t.Parallel()

	fb := `hey <a href="https://matrix.to/#/@alice:x">Alice</a> and ` +
		`<a href="https://matrix.to/#/@bob:server.org">@Bob</a>!`
	got := parseMentions(fb)
	if len(got) != 2 {
		t.Fatalf("mentions = %+v, want 2", got)
	}
	if got[0].UserID != "@alice:x" || got[0].Name != "Alice" {
		t.Errorf("mention 0 = %+v, want @alice:x/Alice", got[0])
	}
	// A leading @ in the pill text is stripped so it matches the plain body.
	if got[1].UserID != "@bob:server.org" || got[1].Name != "Bob" {
		t.Errorf("mention 1 = %+v, want @bob:server.org/Bob", got[1])
	}
	if parseMentions("") != nil || parseMentions("plain, no pills") != nil {
		t.Error("no pills should yield nil")
	}
}

// A reply that merely answers you (reply fallback pill, m.mentions of the replied-to
// sender) is not a mention; a pill in the reply's own body is.
func TestMentionsMe(t *testing.T) {
	t.Parallel()

	const me = id.UserID("@me:x")
	pill := func(mxid, name string) string {
		return `<a href="https://matrix.to/#/` + mxid + `">` + name + `</a>`
	}
	reply := func(quoted, rest string) string {
		return `<mx-reply><blockquote>` + quoted + `<br>the original</blockquote></mx-reply>` + rest
	}

	tests := map[string]struct {
		content event.MessageEventContent
		want    bool
	}{
		"m.mentions naming me": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "hey",
				Mentions: &event.Mentions{UserIDs: []id.UserID{me}},
			},
			want: true,
		},
		"a pill naming me": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "hey", Format: event.FormatHTML,
				FormattedBody: "hey " + pill(string(me), "Me"),
			},
			want: true,
		},
		"m.mentions naming somebody else": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "hey",
				Mentions: &event.Mentions{UserIDs: []id.UserID{"@bob:x"}},
			},
			want: false,
		},
		"a reply to one of my own messages": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "sure, on it", Format: event.FormatHTML,
				FormattedBody: reply(pill(string(me), "Me"), "sure, on it"),
			},
			want: false,
		},
		"m.mentions on a reply, which every reply to me carries": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "forgot to answer this one",
				Mentions: &event.Mentions{UserIDs: []id.UserID{me}},
				RelatesTo: &event.RelatesTo{
					InReplyTo: &event.InReplyTo{EventID: id.EventID("$mine")},
				},
			},
			want: false,
		},
		"a reply that names me as well as answering me": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "Me too", Format: event.FormatHTML,
				FormattedBody: reply(pill(string(me), "Me"), pill(string(me), "Me")+" too"),
				Mentions:      &event.Mentions{UserIDs: []id.UserID{me}},
				RelatesTo: &event.RelatesTo{
					InReplyTo: &event.InReplyTo{EventID: id.EventID("$mine")},
				},
			},
			want: true,
		},
		"a reply to somebody else that then names me": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "Me knows", Format: event.FormatHTML,
				FormattedBody: reply(pill("@dana:x", "Dana"), pill(string(me), "Me")+" knows"),
			},
			want: true,
		},
		// Only a pill addresses somebody; plain-text names do not count.
		"my name as plain text": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "Me will change the flow", Format: event.FormatHTML,
				FormattedBody: "Me will change the flow",
			},
			want: false,
		},
		"a permalink to one of my messages": {
			content: event.MessageEventContent{
				MsgType: event.MsgText, Body: "see this", Format: event.FormatHTML,
				FormattedBody: `see <a href="https://matrix.to/#/!r:x/$e">this</a> from ` + string(me),
			},
			want: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			content := tc.content
			evt := &event.Event{Content: event.Content{Parsed: &content}}
			if got := mentionsMe(evt, me); got != tc.want {
				t.Errorf("mentionsMe = %v, want %v", got, tc.want)
			}
		})
	}
	// With nobody logged in there is nobody to mention.
	viaMentions := &event.Event{Content: event.Content{Parsed: &event.MessageEventContent{
		MsgType: event.MsgText, Body: "hey", Mentions: &event.Mentions{UserIDs: []id.UserID{me}},
	}}}
	if mentionsMe(viaMentions, "") {
		t.Error("with no logged-in user there is no mention")
	}
}

// Regression: /messages events arrive unparsed (Content.Parsed == nil) and used to vanish.
func TestTimelineParsesRawMessages(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{
				"start": "s",
				"end": "e",
				"chunk": [{
					"type": "m.room.message",
					"event_id": "$evt:x",
					"sender": "@bob:x",
					"origin_server_ts": 1700000000000,
					"content": {"msgtype": "m.text", "body": "hello from history"}
				}]
			}`))
		case strings.HasSuffix(r.URL.Path, "/joined_members"):
			_, _ = w.Write([]byte(`{"joined": {"@bob:x": {"display_name": "Bob"}}}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	b := New(nil) // no cache, no crypto: plaintext scrollback path
	b.client = client

	page, err := b.Timeline(context.Background(), domain.RoomID("!r:x"), "", 50)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("got %d messages, want 1: %+v", len(page.Messages), page.Messages)
	}
	got := page.Messages[0]
	want := domain.Message{
		ID:         "$evt:x",
		RoomID:     "!r:x",
		Sender:     "@bob:x",
		SenderName: "Bob",
		Body:       "hello from history",
		Timestamp:  time.UnixMilli(1700000000000),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("message = %+v, want %+v", got, want)
	}
	if page.Next != "e" {
		t.Errorf("Next = %q, want %q", page.Next, "e")
	}
}

func receiptEvent(raw string) *event.Event {
	return &event.Event{Type: event.EphemeralEventReceipt, Content: event.Content{VeryRaw: []byte(raw)}}
}

func TestReadReceipts(t *testing.T) {
	t.Parallel()

	// room lists the room candidates as "event" or "event unthreaded"; threads as
	// "root → event". Both sorted: a receipt event is a map.
	tests := map[string]struct {
		events      []*event.Event
		wantRoom    []string
		wantThreads []string
	}{
		"our read receipt is found": {
			events:   []*event.Event{receiptEvent(`{"$e1:x":{"m.read":{"@me:x":{"ts":1}}}}`)},
			wantRoom: []string{"$e1:x unthreaded"},
		},
		"another user's receipt is ignored": {
			events: []*event.Event{receiptEvent(`{"$e1:x":{"m.read":{"@bob:x":{"ts":1}}}}`)},
		},
		"non-receipt events are skipped": {
			events: []*event.Event{{Type: event.EventMessage}},
		},
		"no events yields empty": {events: nil},
		"a threaded receipt of ours is not the room's": {
			events:      []*event.Event{receiptEvent(`{"$e1:x":{"m.read":{"@me:x":{"ts":1,"thread_id":"$root:x"}}}}`)},
			wantThreads: []string{"$root:x → $e1:x"},
		},
		"the main-timeline receipt still counts": {
			events:   []*event.Event{receiptEvent(`{"$e1:x":{"m.read":{"@me:x":{"ts":1,"thread_id":"main"}}}}`)},
			wantRoom: []string{"$e1:x"},
		},
		// Private receipts (read policy says not to announce) are still ours.
		"our private receipts are ours": {
			events: []*event.Event{receiptEvent(`{
				"$e1:x":{"m.read.private":{"@me:x":{"ts":1}}},
				"$e2:x":{"m.read.private":{"@me:x":{"ts":2,"thread_id":"$root:x"}}}}`)},
			wantRoom:    []string{"$e1:x unthreaded"},
			wantThreads: []string{"$root:x → $e2:x"},
		},
		"another user's private receipt is ignored": {
			events: []*event.Event{nil, receiptEvent(`{"$e1:x":{"m.read.private":{"@bob:x":{"ts":1}}}}`)},
		},
		"a batch is collected whole": {
			events: []*event.Event{receiptEvent(`{
				"$e1:x":{"m.read":{"@me:x":{"ts":1,"thread_id":"main"}}},
				"$e2:x":{"m.read":{"@me:x":{"ts":2,"thread_id":"$a:x"}}},
				"$e3:x":{"m.read":{"@me:x":{"ts":3,"thread_id":"$b:x"}}}}`)},
			wantRoom:    []string{"$e1:x"},
			wantThreads: []string{"$a:x → $e2:x", "$b:x → $e3:x"},
		},
		// Every kind is a candidate; which is the room's position is the events' order,
		// which only the cache knows (see applyReceipts).
		"unthreaded, main and private are all candidates": {
			events: []*event.Event{
				receiptEvent(`{"$old:x":{"m.read":{"@me:x":{"ts":1}}}}`),
				receiptEvent(`{"$new:x":{"m.read":{"@me:x":{"ts":2,"thread_id":"main"}}}}`),
				receiptEvent(`{"$old:x":{"m.read.private":{"@me:x":{"ts":3}}}}`),
			},
			wantRoom: []string{"$new:x", "$old:x unthreaded", "$old:x unthreaded"},
		},
		"public and private on one event are both kept": {
			events: []*event.Event{receiptEvent(`{"$e1:x":{
				"m.read":{"@me:x":{"ts":1,"thread_id":"main"}},
				"m.read.private":{"@me:x":{"ts":2}}}}`)},
			wantRoom: []string{"$e1:x", "$e1:x unthreaded"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := readReceipts(tc.events, id.UserID("@me:x"))
			var room, threads []string
			for _, c := range got.Room {
				label := string(c.Event)
				if c.Unthreaded {
					label += " unthreaded"
				}
				room = append(room, label)
			}
			for _, r := range got.Threads {
				threads = append(threads, string(r.Root)+" → "+string(r.Event))
			}
			sort.Strings(room)
			sort.Strings(threads)
			if !reflect.DeepEqual(room, tc.wantRoom) {
				t.Errorf("room candidates = %v, want %v", room, tc.wantRoom)
			}
			if !reflect.DeepEqual(threads, tc.wantThreads) {
				t.Errorf("threads = %v, want %v", threads, tc.wantThreads)
			}
		})
	}
}

// Presence-not-value merge: a field updates only when the delta carries it.
func TestMergeUnread(t *testing.T) {
	t.Parallel()

	client, err := mautrix.NewClient("http://localhost", id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	b := New(nil)
	b.client = client
	const room = domain.RoomID("!r:x")

	counts := func(n, h int) *mautrix.SyncJoinedRoom {
		return &mautrix.SyncJoinedRoom{UnreadNotifications: &mautrix.UnreadNotificationCounts{NotificationCount: n, HighlightCount: h}}
	}
	receipt := func(raw string) *mautrix.SyncJoinedRoom {
		return &mautrix.SyncJoinedRoom{Ephemeral: mautrix.SyncEventsList{Events: []*event.Event{receiptEvent(raw)}}}
	}
	mark := func(raw string) *mautrix.SyncJoinedRoom {
		return &mautrix.SyncJoinedRoom{AccountData: mautrix.SyncEventsList{Events: []*event.Event{{
			Type:    event.AccountDataMarkedUnread,
			Content: event.Content{VeryRaw: []byte(raw)},
		}}}}
	}

	steps := []struct {
		name        string
		jr          *mautrix.SyncJoinedRoom
		wantChanged bool
		want        domain.Unread
	}{
		{
			name: "initial counts", jr: counts(5, 2), wantChanged: true,
			want: domain.Unread{RoomID: room, Notifications: 5, Highlights: 2},
		},
		{
			name: "receipt only preserves counts", jr: receipt(`{"$e1:x":{"m.read":{"@me:x":{"ts":1}}}}`), wantChanged: true,
			want: domain.Unread{RoomID: room, Notifications: 5, Highlights: 2, ReadEvent: "$e1:x"},
		},
		{
			name: "identical resend is no change", jr: counts(5, 2), wantChanged: false,
			want: domain.Unread{RoomID: room, Notifications: 5, Highlights: 2, ReadEvent: "$e1:x"},
		},
		{
			name: "counts drop to zero on read", jr: counts(0, 0), wantChanged: true,
			want: domain.Unread{RoomID: room, Notifications: 0, Highlights: 0, ReadEvent: "$e1:x"},
		},
		{
			name: "empty delta is no change", jr: &mautrix.SyncJoinedRoom{}, wantChanged: false,
			want: domain.Unread{RoomID: room, Notifications: 0, Highlights: 0, ReadEvent: "$e1:x"},
		},
		{
			name: "a mark set elsewhere arrives", jr: mark(`{"unread":true}`), wantChanged: true,
			want: domain.Unread{RoomID: room, ReadEvent: "$e1:x", Marked: true},
		},
		{
			name: "a sync that says nothing leaves the mark", jr: counts(1, 0), wantChanged: true,
			want: domain.Unread{RoomID: room, Notifications: 1, ReadEvent: "$e1:x", Marked: true},
		},
		{
			name: "the mark is cleared explicitly", jr: mark(`{"unread":false}`), wantChanged: true,
			want: domain.Unread{RoomID: room, Notifications: 1, ReadEvent: "$e1:x", Marked: false},
		},
	}
	for _, s := range steps {
		read, _ := b.applyReceipts(context.Background(), room, readReceipts(s.jr.Ephemeral.Events, client.UserID))
		var marked *bool
		if flag, present := markedUnread(s.jr.AccountData.Events); present {
			marked = &flag
		}
		got, changed := b.mergeUnread(context.Background(), room, s.jr, read, marked)
		if changed != s.wantChanged {
			t.Errorf("%s: changed = %v, want %v", s.name, changed, s.wantChanged)
		}
		if !reflect.DeepEqual(got, s.want) {
			t.Errorf("%s: unread = %+v, want %+v", s.name, got, s.want)
		}
	}
}

func TestReusableMembers(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prev        domain.Room
		known       bool
		wantMembers []string
		wantRefetch bool
	}{
		"unknown room fetches": {
			prev:        domain.Room{},
			known:       false,
			wantMembers: nil,
			wantRefetch: true,
		},
		"known room with members is reused": {
			prev:        domain.Room{Members: []string{"Rowan Blackwood"}},
			known:       true,
			wantMembers: []string{"Rowan Blackwood"},
			wantRefetch: false,
		},
		"known room with empty members refetches": {
			prev:        domain.Room{Name: "Rowan Blackwood"},
			known:       true,
			wantMembers: nil,
			wantRefetch: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			members, refetch := reusableMembers(tc.prev, tc.known)
			if refetch != tc.wantRefetch {
				t.Errorf("refetch = %v, want %v", refetch, tc.wantRefetch)
			}
			if strings.Join(members, ",") != strings.Join(tc.wantMembers, ",") {
				t.Errorf("members = %v, want %v", members, tc.wantMembers)
			}
		})
	}
}

// An unreachable homeserver must not be reported as a rejected session: re-login
// mints a new device and loses E2EE history.
func TestResumeSeparatesUnreachableFromRejected(t *testing.T) {
	t.Parallel()

	session := func(homeserver string) domain.Session {
		return domain.Session{
			Homeserver:  homeserver,
			UserID:      "@eugene:example.org",
			DeviceID:    "DEV1",
			AccessToken: "token",
		}
	}

	t.Run("rejected token asks for a new login", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN_TOKEN","error":"Invalid access token"}`))
		}))
		defer srv.Close()

		err := New(nil).Resume(t.Context(), session(srv.URL))
		if !errors.Is(err, api.ErrSessionRejected) {
			t.Fatalf("err = %v, want ErrSessionRejected", err)
		}
		if errors.Is(err, api.ErrUnreachable) {
			t.Error("a rejected token is not an unreachable homeserver")
		}
	})

	t.Run("an unreachable homeserver keeps the session", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()

		err := New(nil).Resume(t.Context(), session(addr))
		if !errors.Is(err, api.ErrUnreachable) {
			t.Fatalf("err = %v, want ErrUnreachable", err)
		}
		if errors.Is(err, api.ErrSessionRejected) {
			t.Error("an unreachable homeserver must not be reported as a dead session")
		}
	})

	t.Run("a server error is unreachable, not rejected", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()

		err := New(nil).Resume(t.Context(), session(srv.URL))
		if !errors.Is(err, api.ErrUnreachable) {
			t.Fatalf("err = %v, want ErrUnreachable — a 502 is the gateway, not the token", err)
		}
	})
}

// Reachable must classify errors the same way Resume does.
func TestReachableClassifiesLikeResume(t *testing.T) {
	t.Parallel()

	resumed := func(t *testing.T, url string) *InProc {
		t.Helper()
		b := New(nil)
		_ = b.Resume(t.Context(), domain.Session{
			Homeserver: url, UserID: "@eugene:example.org", DeviceID: "DEV1", AccessToken: "token",
		})
		return b
	}

	t.Run("a kept client can be retried", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()

		b := resumed(t, addr)
		if b.client == nil {
			t.Fatal("Resume dropped the client on an unreachable homeserver; nothing left to retry")
		}
		if err := b.Reachable(t.Context()); !errors.Is(err, api.ErrUnreachable) {
			t.Errorf("Reachable = %v, want ErrUnreachable", err)
		}
	})

	t.Run("reports success once the homeserver answers", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"user_id":"@eugene:example.org","device_id":"DEV1"}`))
		}))
		defer srv.Close()

		if err := resumed(t, srv.URL).Reachable(t.Context()); err != nil {
			t.Errorf("Reachable = %v, want nil", err)
		}
	})

	t.Run("a rejected token is not something to wait out", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN_TOKEN","error":"Invalid access token"}`))
		}))
		defer srv.Close()

		b := New(nil)
		_ = b.Resume(t.Context(), domain.Session{
			Homeserver: srv.URL, UserID: "@eugene:example.org", DeviceID: "DEV1", AccessToken: "token",
		})
		if err := b.Reachable(t.Context()); !errors.Is(err, api.ErrUnreachable) && !errors.Is(err, api.ErrSessionRejected) {
			t.Errorf("Reachable = %v, want a classified error", err)
		}
	})
}

// An edit brings its replacement's formatting, sanitized, and a plain replacement none.
func TestAnEditCarriesItsNewFormatting(t *testing.T) {
	t.Parallel()

	edit := func(newContent *event.MessageEventContent) *event.Event {
		return &event.Event{
			ID: id.EventID("$edit:x"), RoomID: id.RoomID("!r:x"), Sender: id.UserID("@me:x"),
			Type: event.EventMessage, Timestamp: 1700000000000,
			Content: event.Content{Parsed: &event.MessageEventContent{
				MsgType: event.MsgText, Body: "* new",
				RelatesTo:  &event.RelatesTo{Type: event.RelReplace, EventID: id.EventID("$orig:x")},
				NewContent: newContent,
			}},
		}
	}
	formatted, ok := toDomainMessage(edit(&event.MessageEventContent{
		MsgType: event.MsgText, Body: "new", Format: event.FormatHTML,
		FormattedBody: `<b>new</b><script>alert(1)</script>`,
	}))
	if !ok || formatted.ID != "$orig:x" || formatted.Body != "new" || formatted.Format.Markup() != "<b>new</b>" {
		t.Fatalf("formatted edit = %+v, want the new sanitized HTML on $orig:x", formatted)
	}
	plain, ok := toDomainMessage(edit(&event.MessageEventContent{MsgType: event.MsgText, Body: "new"}))
	if !ok || plain.Format.Markup() != "" {
		t.Fatalf("plain edit = %+v, want no HTML", plain)
	}
}
