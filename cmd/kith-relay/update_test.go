package main

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/config"
)

// page is the desktop's settings page: the settings it shows, and the revision they
// were read at.
type page struct {
	settings *devicev1.DeviceSettings
	revision string
}

// readPage is the page as DeviceSettings answers it.
func readPage(t *testing.T, device devicev1connect.DeviceServiceClient) (page, error) {
	t.Helper()
	res, err := device.DeviceSettings(t.Context(), connect.NewRequest(&devicev1.DeviceSettingsRequest{}))
	if err != nil {
		return page{}, err
	}
	return page{res.Msg.GetSettings(), res.Msg.GetRevision()}, nil
}

// pageTries is how often the page writes one change, as the window's settings.ts does.
const pageTries = 5

// change is the page changing one key (set) as the window does (settings.ts): written at
// the revision read; told the file changed, it reads the file again and writes the
// change onto it again only if no one changed that key (key) meanwhile, else keeps
// theirs. It returns what the page then shows, and whether the change was written.
func change(t *testing.T, device devicev1connect.DeviceServiceClient, shown page,
	key func(*devicev1.DeviceSettings) any, set func(*devicev1.DeviceSettings),
) (page, bool, error) {
	t.Helper()
	was, base := key(shown.settings), shown
	for range pageTries {
		if !reflect.DeepEqual(key(base.settings), was) {
			return base, false, nil
		}
		want, _ := proto.Clone(base.settings).(*devicev1.DeviceSettings)
		set(want)
		res, err := device.UpdateDeviceSettings(t.Context(), connect.NewRequest(&devicev1.UpdateDeviceSettingsRequest{
			BaseRevision: base.revision, Settings: want,
		}))
		if err == nil {
			return page{res.Msg.GetSettings(), res.Msg.GetRevision()}, true, nil
		}
		if connect.CodeOf(err) != connect.CodeAborted {
			return base, false, err
		}
		if base, err = readPage(t, device); err != nil {
			return shown, false, err
		}
	}
	return base, false, nil
}

func typing(s *devicev1.DeviceSettings) any { return s.GetSendTyping() }

// The window's change is written onto the device file as the file has it: a key it did
// not change stays as the file spells it (unset stays unset, a default is never written
// out), a read rule keeps its delay, which the window is not told, and the answer is the
// file as now written. A value the TUI would refuse is refused; a file changed since the
// window read it (by hand) is not written over, and the page writes its change again
// onto the hand edit; a file that does not load is left alone.
func TestTheDesktopsChangeIsWrittenOntoTheDeviceFile(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	devicePath := config.DevicePath(path)
	handWrite(t, path, "[display]\nfps = 20\n\n"+
		"[[display.read_rule]]\nmatch = \"space:Work\"\nsend = true\ndelay = 5\n\n"+
		"[[display.read_rule]]\nsender = \"matrix:@alice:example.org\"\ndelay = -1\n")
	shown, err := readPage(t, device)
	if err != nil {
		t.Fatal(err)
	}
	write := func(base page, set func(*devicev1.DeviceSettings)) (page, error) {
		want, _ := proto.Clone(base.settings).(*devicev1.DeviceSettings)
		set(want)
		res, werr := device.UpdateDeviceSettings(t.Context(), connect.NewRequest(&devicev1.UpdateDeviceSettingsRequest{
			BaseRevision: base.revision, Settings: want,
		}))
		if werr != nil {
			return base, werr
		}
		return page{res.Msg.GetSettings(), res.Msg.GetRevision()}, nil
	}
	onDisk := func() (config.Device, []byte) {
		t.Helper()
		body, rerr := os.ReadFile(devicePath)
		if rerr != nil {
			t.Fatal(rerr)
		}
		dev, rerr := config.LoadDevice(devicePath)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return dev, body
	}

	no := false
	shown, err = write(shown, func(s *devicev1.DeviceSettings) {
		s.SendTyping = false
		s.SkinTone = "\U0001F3FB"
		s.ReadRules[0].Send = &no
		s.ReadRules = append(s.ReadRules, &devicev1.ReadRule{Match: "tag:spam", Send: &no})
	})
	if err != nil {
		t.Fatal(err)
	}
	dev, body := onDisk()
	if shown.revision != config.Revision(body) {
		t.Errorf("answered revision %q, the file's is %q", shown.revision, config.Revision(body))
	}
	if again, _ := readPage(t, device); settingsText(again.settings) != settingsText(shown.settings) {
		t.Errorf("answered %s, the file reads %s", settingsText(shown.settings), settingsText(again.settings))
	}
	d := dev.Display
	if d.SendTyping == nil || *d.SendTyping || d.SkinTone != "light" || d.FPS != 20 {
		t.Errorf("send_typing %v, skin_tone %q, fps %d; want false, light and the file's 20", d.SendTyping, d.SkinTone, d.FPS)
	}
	if d.SendReceipts != nil || d.UnreadLine != nil || dev.Composer.Markdown != nil || dev.Composer.ConfirmSMS != nil ||
		d.Emoji.Set != "" || d.Reactions.Scope != "" {
		t.Errorf("a key the window did not change was written out:\n%s", body)
	}
	delays := make([]string, len(d.ReadRules))
	for i, r := range d.ReadRules {
		delays[i] = "none"
		if r.Delay != nil {
			delays[i] = fmt.Sprint(*r.Delay)
		}
	}
	if len(d.ReadRules) != 3 || d.ReadRules[0].Send == nil || *d.ReadRules[0].Send || strings.Join(delays, " ") != "5 -1 none" {
		t.Errorf("read rules written %+v with delays %v; want space:Work private, delays kept (5 -1 none)", d.ReadRules, delays)
	}

	for what, set := range map[string]func(*devicev1.DeviceSettings){
		"a skin tone no tone has":  func(s *devicev1.DeviceSettings) { s.SkinTone = "x" },
		"an emoji set there isn't": func(s *devicev1.DeviceSettings) { s.EmojiSet = "huge" },
		"a ranking there isn't":    func(s *devicev1.DeviceSettings) { s.ReactionsScope = "everywhere" },
		"a read rule's bad place": func(s *devicev1.DeviceSettings) {
			s.ReadRules = append(s.ReadRules, &devicev1.ReadRule{Match: "space:"})
		},
	} {
		if _, werr := write(shown, set); connect.CodeOf(werr) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want it refused", what, werr)
		}
		if _, now := onDisk(); config.Revision(now) != shown.revision {
			t.Errorf("%s was written:\n%s", what, now)
		}
	}

	// By hand, between the page's read and its write.
	edited := string(body) + "\n[composer]\nmarkdown = false\n"
	handWrite(t, path, edited)
	if _, werr := write(shown, func(s *devicev1.DeviceSettings) { s.UnreadLine = false }); connect.CodeOf(werr) != connect.CodeAborted {
		t.Errorf("a write over a hand edit: %v, want it aborted", werr)
	}
	if _, now := onDisk(); string(now) != edited {
		t.Errorf("the hand edit was written over:\n%s", now)
	}
	shown, written, err := change(t, device, shown, func(s *devicev1.DeviceSettings) any { return s.GetUnreadLine() },
		func(s *devicev1.DeviceSettings) { s.UnreadLine = false })
	if dev, _ := onDisk(); err != nil || !written || dev.Display.ShowUnreadLine() || dev.Composer.MarkdownEnabled() {
		t.Errorf("the page's change written again onto the hand edit: written %v (%v), unread_line %v, markdown %v; want both changes",
			written, err, dev.Display.ShowUnreadLine(), dev.Composer.MarkdownEnabled())
	}

	// The TUI's editor, between the page's read and its write, changing the same key:
	// the page keeps the editor's value and shows it.
	was, err := config.LoadDevice(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	now := was.Clone()
	now.Display.Emoji.Set = "complete"
	if _, uerr := config.UpdateDevice(devicePath, was, now); uerr != nil {
		t.Fatal(uerr)
	}
	shown, written, err = change(t, device, shown, func(s *devicev1.DeviceSettings) any { return s.GetEmojiSet() },
		func(s *devicev1.DeviceSettings) { s.EmojiSet = "standard" })
	if dev, _ := onDisk(); err != nil || written || dev.Display.Emoji.Set != "complete" || shown.settings.GetEmojiSet() != "complete" {
		t.Errorf("the page wrote over the editor's emoji set: written %v (%v), file %q, shown %q; want the editor's complete",
			written, err, dev.Display.Emoji.Set, shown.settings.GetEmojiSet())
	}

	broken := "[display]\nsend_receipts = \"no\"\n"
	handWrite(t, path, broken)
	if _, err := write(shown, func(s *devicev1.DeviceSettings) { s.UnreadLine = true }); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a write over a file that does not load: %v, want it refused", err)
	}
	if now, _ := os.ReadFile(devicePath); string(now) != broken {
		t.Errorf("the broken file was written over:\n%s", now)
	}
}

// The TUI's settings editor (config.UpdateDevice) and the desktop's page write the
// device file at once. A key only one changes is kept (the editor's terminal, the
// page's send_typing). For the key both change (reactions.static), the page never writes
// over a value it was not shown: each writer's value names the one it replaced, and the
// editor finds its own last value under the page's only where the page read it.
func TestTheTUIsEditorAndTheDesktopsPageWritingAtOnce(t *testing.T) {
	t.Parallel()
	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	devicePath := config.DevicePath(path)
	handWrite(t, path, "terminal = \"t-1\"\n[display.reactions]\nstatic = [\"t-1\"]\n")
	const rounds = 120

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range rounds {
			was, err := config.LoadDevice(devicePath)
			if err != nil {
				t.Error(err)
				return
			}
			last := fmt.Sprintf("t%d", i-1)
			if was.Terminal != last {
				t.Errorf("round %d: terminal = %q, want the editor's last %q", i, was.Terminal, last)
				return
			}
			static := was.Display.Reactions.Static
			if at := slices.Index(static, last); at < 0 || slices.ContainsFunc(static[:at], func(s string) bool { return s[0] != 'd' }) {
				t.Errorf("round %d: reactions.static = %q: the page wrote over the editor's %s unseen", i, static, last)
				return
			}
			now := was.Clone()
			now.Terminal = fmt.Sprintf("t%d", i)
			now.Display.Reactions.Static = append([]string{now.Terminal}, static...)
			if _, err := config.UpdateDevice(devicePath, was, now); err != nil {
				t.Error(err)
				return
			}
		}
	})
	typed := true // the default, until the page writes it
	wg.Go(func() {
		shown, err := readPage(t, device)
		if err != nil {
			t.Error(err)
			return
		}
		for i := range rounds {
			on := i%2 == 0
			var written bool
			if shown, written, err = change(t, device, shown, typing, func(s *devicev1.DeviceSettings) { s.SendTyping = on }); err != nil {
				t.Error(err)
				return
			}
			if written {
				typed = on
			}
			if shown, _, err = change(t, device, shown, func(s *devicev1.DeviceSettings) any { return s.GetReactionsStatic() },
				func(s *devicev1.DeviceSettings) {
					s.ReactionsStatic = append([]string{fmt.Sprintf("d%d", i)}, s.GetReactionsStatic()...)
				}); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()

	final, err := config.LoadDevice(devicePath)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("t%d", rounds-1); final.Terminal != want || final.Display.SendsTyping() != typed {
		t.Errorf("finally terminal %q, send_typing %v; want the editor's %s and the page's last written %v",
			final.Terminal, final.Display.SendsTyping(), want, typed)
	}
}
