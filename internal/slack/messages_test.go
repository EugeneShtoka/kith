package slack

import (
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A Slack ts is seconds and microseconds.
func TestATsIsATime(t *testing.T) {
	t.Parallel()
	if got, want := tsTime("1700000000.123456"), time.Unix(1700000000, 123456000); !got.Equal(want) {
		t.Errorf("tsTime = %v, want %v", got, want)
	}
	if got := tsTime("x"); !got.IsZero() {
		t.Errorf("tsTime of nonsense = %v, want zero", got)
	}
}

// What people and bots wrote is shown, each with its sender; notices and changes to
// other messages are not (yet). A thread reply knows its thread, unless it was also
// sent to the channel.
func TestWhatPeopleWroteIsShown(t *testing.T) {
	t.Parallel()
	n := acmeNames()
	for _, tc := range []struct {
		name   string
		msg    slackgo.Msg
		shown  bool
		check  func(domain.Message) bool
		reason string
	}{
		{"plain", slackgo.Msg{User: "U2", Text: "hi", Timestamp: "1.000001"}, true,
			func(m domain.Message) bool {
				return m.ID == "slack:T1/C1/1.000001" && m.RoomID == "slack:T1/C1" && m.Sender == "slack:T1.U2" && m.SenderName == "Dana"
			}, "a person's message, by its ts, from them"},
		{"bot", slackgo.Msg{SubType: "bot_message", BotID: "B1", Username: "CI", Text: "green", Timestamp: "1.1"}, true,
			func(m domain.Message) bool { return m.Sender == "slack:T1.B1" && m.SenderName == "CI" }, "from the bot, by its name"},
		{"emote", slackgo.Msg{SubType: "me_message", User: "U2", Text: "waves", Timestamp: "1.2"}, true,
			func(m domain.Message) bool { return m.Emote }, "an emote"},
		{"file", slackgo.Msg{SubType: "file_share", User: "U2", Text: "look", Timestamp: "1.3", Files: []slackgo.File{{Name: "a.png"}}}, true,
			func(m domain.Message) bool { return m.Body == "look\n[file] a.png" && m.Media == nil }, "its words, then a file it cannot load, named"},
		{"image", slackgo.Msg{SubType: "file_share", User: "U2", Timestamp: "1.31", Files: []slackgo.File{{
			ID: "F1", Name: "a.png", Mimetype: "image/png", Size: 2048, OriginalW: 640, OriginalH: 480, URLPrivate: "https://files.slack.com/files-pri/T1-F1/a.png"}}}, true,
			func(m domain.Message) bool {
				return m.Body == "a.png" && m.Media != nil && m.Media.Type == domain.MediaImage &&
					m.Media.Name == "a.png" && m.Media.Width == 640 && m.Media.Height == 480 && m.Media.Size == 2048
			}, "the image as its attachment, its name for no caption"},
		{"files", slackgo.Msg{SubType: "file_share", User: "U2", Text: "both", Timestamp: "1.32", Files: []slackgo.File{
			{ID: "F2", Name: "doc", IsExternal: true, URLPrivate: "https://drive.example/doc"},
			{ID: "F3", Name: "b.pdf", Mimetype: "application/pdf", URLPrivate: "https://files.slack.com/files-pri/T1-F3/b.pdf"}}}, true,
			func(m domain.Message) bool {
				return m.Body == "both\n[file] doc" && m.Media != nil && m.Media.Type == domain.MediaFile && m.Media.Name == "b.pdf"
			}, "the first file kith can load attached, another service's named"},
		{"deleted file", slackgo.Msg{SubType: "file_share", User: "U2", Timestamp: "1.33", Files: []slackgo.File{
			{ID: "F4", Name: "gone.png", Mode: "tombstone", URLPrivate: "https://files.slack.com/files-pri/T1-F4/gone.png"}}}, true,
			func(m domain.Message) bool { return m.Body == "[file] gone.png" && m.Media == nil }, "a deleted file named, not attached"},
		{"attachments", slackgo.Msg{SubType: "bot_message", BotID: "B1", Timestamp: "1.4", Attachments: []slackgo.Attachment{{Fallback: "build passed"}}}, true,
			func(m domain.Message) bool { return m.Body == "build passed" }, "the attachment's words"},
		{"reply", slackgo.Msg{User: "U2", Text: "in thread", Timestamp: "2.0", ThreadTimestamp: "1.0"}, true,
			func(m domain.Message) bool { return m.ThreadRoot == "slack:T1/C1/1.0" }, "in its thread"},
		{"broadcast", slackgo.Msg{SubType: "thread_broadcast", User: "U2", Text: "also here", Timestamp: "2.1", ThreadTimestamp: "1.0"}, true,
			func(m domain.Message) bool { return m.ThreadRoot == "" }, "in the channel"},
		{"root", slackgo.Msg{User: "U2", Text: "starts one", Timestamp: "1.0", ThreadTimestamp: "1.0"}, true,
			func(m domain.Message) bool { return m.ThreadRoot == "" }, "a thread's root is in the channel"},
		{"edited", slackgo.Msg{User: "U2", Text: "fixed", Timestamp: "1.5", Edited: &slackgo.Edited{Timestamp: "9.0"}}, true,
			func(m domain.Message) bool { return m.Edited && m.EditedAt.Equal(time.Unix(9, 0)) }, "marked edited, when"},
		{"own here", slackgo.Msg{User: "U1", Text: "<!here> hi", Timestamp: "1.6"}, true,
			func(m domain.Message) bool { return !m.Mentioned }, "one's own @here does not mention oneself"},
		{"join", slackgo.Msg{SubType: "channel_join", User: "U2", Text: "<@U2> has joined", Timestamp: "1.7"}, false, nil, ""},
		{"changed", slackgo.Msg{SubType: "message_changed", Hidden: true, Timestamp: "1.8"}, false, nil, ""},
		{"empty", slackgo.Msg{User: "U2", Timestamp: "1.9"}, false, nil, ""},
	} {
		got, shown := incoming("C1", &tc.msg, n)
		if shown != tc.shown {
			t.Errorf("%s: shown = %v, want %v", tc.name, shown, tc.shown)
			continue
		}
		if shown && !tc.check(got) {
			t.Errorf("%s: %+v, want %s", tc.name, got, tc.reason)
		}
	}
}
