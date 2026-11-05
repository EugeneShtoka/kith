package main

import (
	"fmt"
	"os"
	"slices"
	"testing"

	"connectrpc.com/connect"

	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/config"
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
		want *devicev1.DeviceSettings
	}{
		{"nothing set", "", spelling(&devicev1.DeviceSettings{SendReceipts: true, UnreadLine: true, Markdown: true, ConfirmSms: true, SendTyping: true, EmojiSet: "curated", ReactionsScope: "room"})},
		{
			"keys and rules set",
			"[display]\nsend_receipts = false\nunread_line = false\nfiling_spaces = [\"space:Work\", \"place:Home\"]\n\n" +
				"[[display.read_rule]]\nmatch = \"space:Work\"\nsend = true\n\n" +
				"[[display.read_rule]]\nsender = \"matrix:@alice:example.org\"\ndelay = 5\n",
			spelling(&devicev1.DeviceSettings{
				ReadRules: []*devicev1.ReadRule{
					{Match: "space:Work", Send: &yes},
					{Sender: "matrix:@alice:example.org"},
				},
				Markdown: true, ConfirmSms: true, SendTyping: true, EmojiSet: "curated", ReactionsScope: "room",
				FilingSpaces: []string{"space:Work", "place:Home"},
			}),
		},
		{
			"the composer's keys set",
			"[composer]\nmarkdown = false\nconfirm_sms = false\n\n" +
				"[display]\nsend_typing = false\nskin_tone = \"medium\"\n\n" +
				"[display.emoji]\nset = \"standard\"\nextra = { shipit = \"🐿️\" }\n\n" +
				"[display.reactions]\nscope = \"static\"\nstatic = [\"👍\", \"🎉\"]\n",
			spelling(&devicev1.DeviceSettings{
				SendReceipts: true, UnreadLine: true, SkinTone: "\U0001F3FD", EmojiSet: "standard",
				EmojiExtra: map[string]string{"shipit": "🐿️"}, ReactionsScope: "static", ReactionsStatic: []string{"👍", "🎉"},
			}),
		},
		{
			"spelling and completion set",
			"[spell]\nunderline = \"line\"\nrare_underline = \"none\"\ncheck_before_send = true\n" +
				"rare_before_send = true\nautocorrect = \"all\"\ndeclined = [\"de_DE\"]\nfrequencies_declined = [\"he_IL\"]\n\n" +
				"[complete]\nenabled = false\nghost = false\nratio = 5\nscope = \"global\"\nsources = [\"history\"]\n\n" +
				"[complete.model]\ndeclined = true\n",
			&devicev1.DeviceSettings{
				SendReceipts: true, UnreadLine: true, Markdown: true, ConfirmSms: true, SendTyping: true, EmojiSet: "curated", ReactionsScope: "room",
				SpellUnderline: "line", RareUnderline: "none", CheckBeforeSend: true, RareBeforeSend: true, Autocorrect: "all",
				SpellDeclined: []string{"de_DE"}, FrequenciesDeclined: []string{"he_IL"},
				CompleteRatio: 5, CompleteScope: "global", CompleteSources: []string{"history"}, ModelDeclined: true,
			},
		},
		{
			"changed by hand meanwhile",
			"[display]\n[[display.read_rule]]\nmatch = \"matrix:fedcba9876543210/!room:example.org\"\nsend = false\n",
			spelling(&devicev1.DeviceSettings{
				SendReceipts: true, UnreadLine: true, Markdown: true, ConfirmSms: true, SendTyping: true, EmojiSet: "curated", ReactionsScope: "room",
				ReadRules: []*devicev1.ReadRule{{Match: "matrix:fedcba9876543210/!room:example.org", Send: &no}},
			}),
		},
	} {
		handWrite(t, path, step.file)
		got, err := read()
		if err != nil {
			t.Fatalf("%s: %v", step.what, err)
		}
		if settingsText(got.GetSettings()) != settingsText(step.want) {
			t.Errorf("%s: DeviceSettings = %s, want %s", step.what, settingsText(got.GetSettings()), settingsText(step.want))
		}
		if want := config.Revision([]byte(step.file)); got.GetRevision() != want {
			t.Errorf("%s: revision %q, want the file's %q", step.what, got.GetRevision(), want)
		}
	}
	if err := os.Remove(config.DevicePath(path)); err != nil {
		t.Fatal(err)
	}
	if got, err := read(); err != nil || got.GetRevision() != "" {
		t.Errorf("DeviceSettings with no device file: revision %q (%v), want none", got.GetRevision(), err)
	}

	handWrite(t, path, "[display]\nsend_receipts = \"no\"\n")
	if _, err := read(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("DeviceSettings over a broken device file = %v, want it refused", err)
	}
}

// spelling is s with [spell] and [complete] as the TUI reads them unset.
func spelling(s *devicev1.DeviceSettings) *devicev1.DeviceSettings {
	s.SpellUnderline, s.RareUnderline, s.Autocorrect = "curly", "dotted", "off"
	s.Complete, s.CompleteGhost, s.CompleteRatio, s.CompleteScope = true, true, 3, "room"
	return s
}

// settingsText spells s for comparing: each key, and each rule with send unset told apart.
func settingsText(s *devicev1.DeviceSettings) string {
	text := fmt.Sprintf("send_receipts=%v unread_line=%v markdown=%v confirm_sms=%v send_typing=%v tone=%q set=%q extra=%v scope=%q static=%q rules:",
		s.GetSendReceipts(), s.GetUnreadLine(), s.GetMarkdown(), s.GetConfirmSms(), s.GetSendTyping(), s.GetSkinTone(),
		s.GetEmojiSet(), s.GetEmojiExtra(), s.GetReactionsScope(), s.GetReactionsStatic())
	text += fmt.Sprintf(" underline=%q rare=%q before_send=%v/%v autocorrect=%q declined=%q/%q complete=%v ghost=%v ratio=%d scope=%q sources=%q model_declined=%v",
		s.GetSpellUnderline(), s.GetRareUnderline(), s.GetCheckBeforeSend(), s.GetRareBeforeSend(), s.GetAutocorrect(),
		s.GetSpellDeclined(), s.GetFrequenciesDeclined(), s.GetComplete(), s.GetCompleteGhost(), s.GetCompleteRatio(),
		s.GetCompleteScope(), s.GetCompleteSources(), s.GetModelDeclined())
	text += fmt.Sprintf(" filing_spaces=%q", s.GetFilingSpaces())
	for _, r := range s.GetReadRules() {
		send := "unset"
		if r.Send != nil {
			send = fmt.Sprint(r.GetSend())
		}
		text += fmt.Sprintf(" {match=%q sender=%q send=%s}", r.GetMatch(), r.GetSender(), send)
	}
	return text
}

// The window reads [keys] as the TUI binds it: every action, its default where unset,
// each list split into sequences, and a key kith does not name left out.
func TestDeviceSettingsCarryTheKeys(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	handWrite(t, path, "[keys]\njump_to = \"gj, ctrl+k\"\njump_back = \"bogus,ctrl+o\"\n")
	resp, err := device.DeviceSettings(t.Context(), connect.NewRequest(&devicev1.DeviceSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	keys := resp.Msg.GetSettings().GetKeys()
	for path, want := range map[string][]string{
		"jump_to":        {"g j", "ctrl+k"},
		"jump_back":      {"ctrl+o"},
		"jump_forward":   {"ctrl+i", "alt+o"}, // unset: the default
		"timeline.reply": {"r"},
	} {
		if got := keys[path].GetSequences(); !slices.Equal(got, want) {
			t.Errorf("keys[%q] = %q, want %q", path, got, want)
		}
	}
	if len(keys) != len(config.DefaultKeys().Bindings) {
		t.Errorf("%d actions' keys, want every one of %d", len(keys), len(config.DefaultKeys().Bindings))
	}
}

// The window reads [[keys.jump]] as the TUI binds them: each chord one sequence, to a
// room by its ID or a rail row by its entry, and a chord or target it cannot read left out.
func TestDeviceSettingsCarryTheJumps(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	handWrite(t, path, `[[keys.jump]]
chord = "gw"
target = "room:matrix:0123456789abcdef/!work:example.org"

[[keys.jump]]
chord = "g S"
target = "space:Work"

[[keys.jump]]
chord = "g bogus"
target = "tag:Later"

[[keys.jump]]
chord = "g n"
target = "room:Standup"
`)
	resp, err := device.DeviceSettings(t.Context(), connect.NewRequest(&devicev1.DeviceSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, j := range resp.Msg.GetSettings().GetJumps() {
		got = append(got, fmt.Sprintf("%s→room=%s,place=%s", j.GetSequence(), j.GetRoom(), j.GetPlace()))
	}
	want := []string{"g w→room=matrix:0123456789abcdef/!work:example.org,place=", "g S→room=,place=space:Work"}
	if !slices.Equal(got, want) {
		t.Errorf("jumps = %q, want %q", got, want)
	}
}

// The window reads [[keys.tag]] as the TUI binds them: each chord one sequence, to the
// tag it names, and a chord it cannot read or a binding with no tag left out.
func TestDeviceSettingsCarryTheTagKeys(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	handWrite(t, path, `[[keys.tag]]
chord = "!"
tag = "Spam"

[[keys.tag]]
chord = "g a"
tag = " Archive "

[[keys.tag]]
chord = "g bogus"
tag = "Later"

[[keys.tag]]
chord = "g n"
tag = ""
`)
	resp, err := device.DeviceSettings(t.Context(), connect.NewRequest(&devicev1.DeviceSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range resp.Msg.GetSettings().GetTagKeys() {
		got = append(got, k.GetSequence()+"→"+k.GetTag())
	}
	if want := []string{"!→Spam", "g a→Archive"}; !slices.Equal(got, want) {
		t.Errorf("tag keys = %q, want %q", got, want)
	}
}

// The window plays a voice note as the TUI does: at [display.media.audio] speed, skipping by its skip,
// each its default where unset.
func TestDeviceSettingsCarryThePlayer(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	read := func() (float64, int64) {
		resp, err := device.DeviceSettings(t.Context(), connect.NewRequest(&devicev1.DeviceSettingsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetSettings().GetAudioSpeed(), resp.Msg.GetSettings().GetAudioSkip()
	}
	if speed, skip := read(); speed != config.DefaultAudioSpeed || skip != config.DefaultAudioSkip {
		t.Errorf("unset: speed %v, skip %d; want the defaults", speed, skip)
	}
	handWrite(t, path, "[display.media.audio]\nspeed = 1.5\nskip = 5\n")
	if speed, skip := read(); speed != 1.5 || skip != 5 {
		t.Errorf("speed %v, skip %d; want 1.5 and 5", speed, skip)
	}
}
