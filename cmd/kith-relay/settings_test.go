package main

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"

	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
)

// DeviceSettings reads the device file again on each call, an unset key its default,
// and says when the file does not load rather than answering defaults: a window that
// read "send_receipts = true" from a broken file would tell people what it should not.
func TestDeviceSettingsAreTheDeviceFileResolved(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	read := func() (*devicev1.DeviceSettingsResponse, error) {
		resp, err := device.DeviceSettings(t.Context(), connect.NewRequest(&devicev1.DeviceSettingsRequest{}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	yes, no := true, false
	for _, step := range []struct {
		what string
		file string
		want *devicev1.DeviceSettingsResponse
	}{
		{"nothing set", "", &devicev1.DeviceSettingsResponse{SendReceipts: true, UnreadLine: true, Markdown: true, ConfirmSms: true, SendTyping: true, EmojiSet: "curated", ReactionsScope: "room"}},
		{
			"keys and rules set",
			"[display]\nsend_receipts = false\nunread_line = false\n\n" +
				"[[display.read_rule]]\nmatch = \"space:Work\"\nsend = true\n\n" +
				"[[display.read_rule]]\nsender = \"matrix:@alice:example.org\"\ndelay = 5\n",
			&devicev1.DeviceSettingsResponse{
				ReadRules: []*devicev1.ReadRule{
					{Match: "space:Work", Send: &yes},
					{Sender: "matrix:@alice:example.org"},
				},
				Markdown: true, ConfirmSms: true, SendTyping: true, EmojiSet: "curated", ReactionsScope: "room",
			},
		},
		{
			"the composer's keys set",
			"[composer]\nmarkdown = false\nconfirm_sms = false\n\n" +
				"[display]\nsend_typing = false\nskin_tone = \"medium\"\n\n" +
				"[display.emoji]\nset = \"standard\"\nextra = { shipit = \"🐿️\" }\n\n" +
				"[display.reactions]\nscope = \"static\"\nstatic = [\"👍\", \"🎉\"]\n",
			&devicev1.DeviceSettingsResponse{
				SendReceipts: true, UnreadLine: true, SkinTone: "\U0001F3FD", EmojiSet: "standard",
				EmojiExtra: map[string]string{"shipit": "🐿️"}, ReactionsScope: "static", ReactionsStatic: []string{"👍", "🎉"},
			},
		},
		{
			"changed by hand meanwhile",
			"[display]\n[[display.read_rule]]\nmatch = \"matrix:fedcba9876543210/!room:example.org\"\nsend = false\n",
			&devicev1.DeviceSettingsResponse{
				SendReceipts: true, UnreadLine: true, Markdown: true, ConfirmSms: true, SendTyping: true, EmojiSet: "curated", ReactionsScope: "room",
				ReadRules: []*devicev1.ReadRule{{Match: "matrix:fedcba9876543210/!room:example.org", Send: &no}},
			},
		},
	} {
		handWrite(t, path, step.file)
		got, err := read()
		if err != nil {
			t.Fatalf("%s: %v", step.what, err)
		}
		if settingsText(got) != settingsText(step.want) {
			t.Errorf("%s: DeviceSettings = %s, want %s", step.what, settingsText(got), settingsText(step.want))
		}
	}

	handWrite(t, path, "[display]\nsend_receipts = \"no\"\n")
	if _, err := read(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("DeviceSettings over a broken device file = %v, want it refused", err)
	}
}

// settingsText spells s for comparing: each key, and each rule with send unset told apart.
func settingsText(s *devicev1.DeviceSettingsResponse) string {
	text := fmt.Sprintf("send_receipts=%v unread_line=%v markdown=%v confirm_sms=%v send_typing=%v tone=%q set=%q extra=%v scope=%q static=%q rules:",
		s.GetSendReceipts(), s.GetUnreadLine(), s.GetMarkdown(), s.GetConfirmSms(), s.GetSendTyping(), s.GetSkinTone(),
		s.GetEmojiSet(), s.GetEmojiExtra(), s.GetReactionsScope(), s.GetReactionsStatic())
	for _, r := range s.GetReadRules() {
		send := "unset"
		if r.Send != nil {
			send = fmt.Sprint(r.GetSend())
		}
		text += fmt.Sprintf(" {match=%q sender=%q send=%s}", r.GetMatch(), r.GetSender(), send)
	}
	return text
}
