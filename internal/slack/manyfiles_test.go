package slack

import (
	"context"
	"strings"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// threeFiles is a message with an image, a PDF and a link to a document Slack does
// not hold.
func threeFiles(ts string) slackgo.Msg {
	return slackgo.Msg{
		Type: "message", SubType: "file_share", User: "U2", Timestamp: ts, Text: "see these",
		Files: []slackgo.File{
			{ID: "F1", Name: "a.png", Mimetype: "image/png", Mode: "hosted", URLPrivate: "https://files.slack.com/files-pri/T1-F1/a.png"},
			{ID: "F2", Name: "b.pdf", Mimetype: "application/pdf", Mode: "hosted", URLPrivate: "https://files.slack.com/files-pri/T1-F2/b.pdf"},
			{ID: "F3", Name: "Plan", Title: "Plan", IsExternal: true, URLPrivate: "https://docs.example.org/plan"},
		},
	}
}

// Every file of a message kith can load is one it can open and save: the first is the
// message's attachment, each other a row of its own just under it, from the same
// sender, loading its own file. One it cannot load is named in the text. What is
// done to a row is done to its message, except what would change the message itself.
func TestEveryFileOfAMessageLoads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, host, client := slackWithFiles(t)
	host.files["/files-pri/T1-F2/b.pdf"] = []byte("pdf bytes")
	// Slack would take both, so only kith can refuse them.
	f.on("chat.delete", func(map[string]string) any { return map[string]any{"ok": true, "channel": "C1", "ts": "1.0"} })
	f.on("chat.update", func(map[string]string) any { return map[string]any{"ok": true, "channel": "C1", "ts": "1.0"} })
	a, w := connectedTo(t, client)
	room, id := roomID("T1", "C1"), domain.EventID("slack:T1/C1/1.0")
	row := fileRowID(id, "F2")

	a.cachePage(ctx, w, "C1", []slackgo.Message{{Msg: threeFiles("1.0")}}, time.Now())
	msgs, err := a.cache.Messages(ctx, room, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].ID != id || msgs[1].ID != row {
		t.Fatalf("cached %+v, want the message and then the PDF's row", msgs)
	}
	if msgs[0].Media == nil || msgs[0].Media.Name != "a.png" || !strings.Contains(msgs[0].Body, "[file] Plan") || strings.Contains(msgs[0].Body, "b.pdf") {
		t.Errorf("the message = %q with %+v, want the image attached and only the link named", msgs[0].Body, msgs[0].Media)
	}
	if msgs[1].Media == nil || msgs[1].Media.Name != "b.pdf" || msgs[1].Sender != msgs[0].Sender {
		t.Errorf("the row = %+v, want the PDF, from the same sender", msgs[1])
	}
	for event, want := range map[domain.EventID]string{id: "png bytes", row: "pdf bytes"} {
		if data, err := a.LoadImage(ctx, room, event); err != nil || string(data) != want {
			t.Errorf("LoadImage(%s) = (%q, %v), want %q", event, data, err, want)
		}
	}

	if channel, ts, ok := cutLast(domain.ParseID(string(row)).Native); !ok || channel != "C1" || ts != "1.0" {
		t.Errorf("a row names message %q in %q, want 1.0 in C1 (a reaction or reply to it is to its message)", ts, channel)
	}
	if err := a.Redact(ctx, room, row, ""); err == nil {
		t.Error("deleting a file's row was sent: it would delete the whole message")
	}
	if err := a.Send(ctx, room, domain.Draft{Body: "x", Edits: row}); err == nil {
		t.Error("editing a file's row was sent: it would put the file's name in the message")
	}
	if n := len(f.calls("chat.delete")) + len(f.calls("chat.update")); n != 0 {
		t.Errorf("Slack was asked to change the message %d times for a file's row", n)
	}
}

// A file taken out of a message by an edit goes as a deleted message does, and the
// message's deletion takes its files' rows with it.
func TestAFilesRowGoesWithItsFileAndItsMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, _, client := slackWithFiles(t)
	a, w := connectedTo(t, client)
	room, id := roomID("T1", "C1"), domain.EventID("slack:T1/C1/1.0")
	deleted := func(event domain.EventID) bool {
		m, found, err := a.cache.MessageByID(ctx, room, event)
		return err == nil && found && m.Redacted
	}

	before := threeFiles("1.0")
	before.Files = append(before.Files, slackgo.File{ID: "F4", Name: "c.png", Mimetype: "image/png", Mode: "hosted", URLPrivate: "https://files.slack.com/files-pri/T1-F4/c.png"})
	a.arrived(ctx, w, "C1", &before)
	if _, found, _ := a.cache.MessageByID(ctx, room, fileRowID(id, "F4")); !found {
		t.Fatal("a file heard live has no row")
	}

	after := threeFiles("1.0")
	after.Edited = &slackgo.Edited{User: "U2", Timestamp: "2.0"}
	a.onEdited(ctx, w, &slackgo.MessageEvent{
		Msg: slackgo.Msg{Channel: "C1", SubType: "message_changed", Timestamp: "2.0"}, SubMessage: &after, PreviousMessage: &before,
	})
	if !deleted(fileRowID(id, "F4")) || deleted(fileRowID(id, "F2")) {
		t.Errorf("after the edit: c.png's row deleted %v (want true), b.pdf's %v (want false)",
			deleted(fileRowID(id, "F4")), deleted(fileRowID(id, "F2")))
	}

	a.onDeleted(ctx, w, &slackgo.MessageEvent{
		Msg: slackgo.Msg{Channel: "C1", SubType: "message_deleted", Timestamp: "3.0", DeletedTimestamp: "1.0"}, PreviousMessage: &after,
	})
	if !deleted(id) || !deleted(fileRowID(id, "F2")) {
		t.Errorf("after the message's deletion: it deleted %v, b.pdf's row %v; want both", deleted(id), deleted(fileRowID(id, "F2")))
	}
}

// Messages cached before further files were rows named them in their text; each is
// read again from Slack (a thread's reply from its thread) and its files become rows.
func TestMessagesCachedNamingFilesAreReadAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, _, client := slackWithFiles(t)
	reply := threeFiles("5.0")
	reply.ThreadTimestamp = "4.0"
	f.on("conversations.history", func(p map[string]string) any {
		return map[string]any{"ok": true, "messages": []any{threeFiles(p["latest"])}}
	})
	f.on("conversations.replies", func(map[string]string) any {
		return map[string]any{"ok": true, "messages": []any{reply}}
	})
	a, w := connectedTo(t, client)
	room := roomID("T1", "C1")
	top, inThread := domain.EventID("slack:T1/C1/1.0"), domain.EventID("slack:T1/C1/5.0")
	old := []domain.Message{
		{ID: top, Sender: personID("T1", "U2"), Body: "see these\n[file] b.pdf", Timestamp: tsTime("1.0"), Media: &domain.Media{Type: domain.MediaImage, Name: "a.png"}},
		{ID: inThread, Sender: personID("T1", "U2"), Body: "see these\n[file] b.pdf", Timestamp: tsTime("5.0"), ThreadRoot: "slack:T1/C1/4.0", Media: &domain.Media{Type: domain.MediaImage, Name: "a.png"}},
	}
	if _, ok := a.record(ctx, w, room, old); !ok {
		t.Fatal("could not cache the old messages")
	}
	for i := range old {
		if err := a.cache.SaveMediaSource(ctx, old[i].ID, room, "", `{"slack":"F1","url":"https://files.slack.com/files-pri/T1-F1/a.png"}`); err != nil {
			t.Fatal(err)
		}
	}

	w.filesReread.Do(func() { a.refetchFileMessages(ctx, w) })
	for _, id := range []domain.EventID{top, inThread} {
		if _, found, err := a.cache.MessageByID(ctx, room, fileRowID(id, "F2")); err != nil || !found {
			t.Errorf("%s's PDF has no row after it was read again (%v)", id, err)
		}
		if m, _, _ := a.cache.MessageByID(ctx, room, id); strings.Contains(m.Body, "b.pdf") {
			t.Errorf("%s still names the PDF in its text: %q", id, m.Body)
		}
	}
	if asked := f.calls("conversations.replies"); len(asked) != 1 || asked[0]["ts"] != "4.0" {
		t.Errorf("the thread was asked %v, want the reply read from thread 4.0", asked)
	}
	w.filesReread.Do(func() { a.refetchFileMessages(ctx, w) }) // a reconnect, as goCatchUp does it
	if n := len(f.calls("conversations.history")); n != 1 {
		t.Errorf("history asked %d times, want once a run", n)
	}
}
