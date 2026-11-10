package main

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/config"
)

// foldOp is one fold a writer makes: room folded (true) or unfolded.
type foldOp struct {
	room string
	fold bool
}

// The TUI (config.UpdateDevice over the file as it last had it) and the desktop (read, then
// write at that revision; told the file changed, read again and make the fold on that) fold
// rooms in the one device file. Over every interleaving of the TUI's folds with the
// desktop's read and write steps, neither writer's folds are lost to the other's: the file
// ends with the rooms each writer left folded.
func TestTUIAndDesktopFoldsMergeInEveryInterleaving(t *testing.T) {
	t.Parallel()
	tui := []foldOp{{"!a", true}, {"!c", true}, {"!a", false}}
	desktop := []foldOp{{"!b", true}, {"!d", true}, {"!b", false}}
	want := []string{"!c", "!d"}

	path, _ := relayConfig(t, t.TempDir())
	device := deviceClient(relayFor(t, kithdSocket(t), path))
	devicePath := config.DevicePath(path)
	// Each schedule places the TUI's 3 folds among the desktop's 6 steps (a read, a write each).
	for _, schedule := range interleavings(len(tui), 2*len(desktop)) {
		handWrite(t, path, "")
		var tuiBase config.Device // the file as the TUI last had it: empty
		var shown page
		ti, di := 0, 0
		for _, tuiTurn := range schedule {
			var err error
			switch {
			case tuiTurn:
				now := tuiBase.Clone()
				now.Display.Threads.Folded = foldedWith(now.Display.Threads.Folded, tui[ti])
				tuiBase, err = config.UpdateDevice(devicePath, tuiBase, now)
				ti++
			case di%2 == 0:
				shown, err = readPage(t, device)
				di++
			default:
				err = foldOnPage(t, device, shown, desktop[di/2])
				di++
			}
			if err != nil {
				t.Fatalf("%s: %v", scheduleText(schedule), err)
			}
		}
		got, err := readPage(t, device)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.settings.GetThreadsFolded(), want) {
			t.Errorf("%s: folded %q, want %q", scheduleText(schedule), got.settings.GetThreadsFolded(), want)
		}
	}
}

// foldOnPage is the desktop's fold (session.ts setThreadsFold): written at the revision read;
// told the file changed, made again on the file as read now.
func foldOnPage(t *testing.T, device devicev1connect.DeviceServiceClient, shown page, op foldOp) error {
	t.Helper()
	for range pageTries {
		want, _ := proto.Clone(shown.settings).(*devicev1.DeviceSettings)
		folded := slices.DeleteFunc(slices.Clone(want.GetThreadsFolded()), func(room string) bool { return room == op.room })
		if op.fold {
			folded = append(folded, op.room)
			slices.Sort(folded)
		}
		want.ThreadsFolded = folded
		_, err := device.UpdateDeviceSettings(t.Context(), connect.NewRequest(&devicev1.UpdateDeviceSettingsRequest{
			BaseRevision: shown.revision, Settings: want,
		}))
		if connect.CodeOf(err) != connect.CodeAborted {
			return err
		}
		if shown, err = readPage(t, device); err != nil {
			return err
		}
	}
	return errors.New("the fold lost every try")
}

// foldedWith is the TUI's fold table after op (toggleThreadsFold).
func foldedWith(table map[string]bool, op foldOp) map[string]bool {
	out := maps.Clone(table)
	if out == nil {
		out = map[string]bool{}
	}
	if op.fold {
		out[op.room] = true
	} else {
		delete(out, op.room)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// interleavings are the orders of a steps of one writer among b of another's; true is the
// first's turn.
func interleavings(a, b int) [][]bool {
	if a == 0 || b == 0 {
		return [][]bool{slices.Repeat([]bool{a > 0}, a+b)}
	}
	var out [][]bool
	for _, rest := range interleavings(a-1, b) {
		out = append(out, append([]bool{true}, rest...))
	}
	for _, rest := range interleavings(a, b-1) {
		out = append(out, append([]bool{false}, rest...))
	}
	return out
}

// scheduleText spells a schedule: T for the TUI's turn, D for the desktop's.
func scheduleText(schedule []bool) string {
	var b strings.Builder
	for _, tuiTurn := range schedule {
		if tuiTurn {
			b.WriteByte('T')
		} else {
			b.WriteByte('D')
		}
	}
	return b.String()
}
