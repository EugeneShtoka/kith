package chatexport

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// write puts text at name in a fresh folder, zipped as file when file is not "".
func write(t *testing.T, name, file, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if file == "" {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	f, err := os.Create(path) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for _, n := range []string{file, "00000003-PHOTO-2021-03-04.jpg"} {
		w, err := z.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

var berlin = time.FixedZone("CET", 3600)

// Each phone's way of writing a chat reads the same: iOS zipped with its seconds and
// marks, Android's text alone in its minutes, a 12-hour clock with a narrow space, US
// month-first dates. A message's later lines stay with it; Android's own notes go
// (iOS writes its notes as the chat's messages, which stay).
func TestEachPhonesExportReads(t *testing.T) {
	t.Parallel()
	at := func(h, m, s int) time.Time { return time.Date(2021, 3, 4, h, m, s, 0, berlin) }
	want := []Line{
		{At: at(21, 5, 7), Sender: "Dana Wolfe", Body: "Dinner on Friday?"},
		{At: at(21, 6, 0), Sender: "Me", Body: "Yes!\nI'll bring the pie\n12:00 works too"},
		{At: at(0, 1, 2), Sender: "Dana Wolfe", Body: "image omitted"},
	}
	minutes := []Line{want[0], want[1], want[2]}
	for i := range minutes {
		minutes[i].At, minutes[i].Minute = minutes[i].At.Truncate(time.Minute), true
	}
	for name, c := range map[string]struct {
		file, text string
		want       []Line
	}{
		"ios": {"_chat.txt", "\ufeff[04/03/2021, 21:05:07] Dana Wolfe: Dinner on Friday?\r\n" +
			"[04/03/2021, 21:06:00] Me: Yes!\nI'll bring the pie\n12:00 works too\n" +
			"\u200e[04/03/2021, 00:01:02] Dana Wolfe: \u200eimage omitted\n", want},
		"android": {"", "04/03/2021, 21:04 - Messages and calls are end-to-end encrypted.\n" +
			"04/03/2021, 21:05 - Dana Wolfe: Dinner on Friday?\n" +
			"04/03/2021, 21:06 - Me: Yes!\nI'll bring the pie\n12:00 works too\n" +
			"04/03/2021, 21:06 - Dana Wolfe added Lee\n" +
			"04/03/2021, 00:01 - Dana Wolfe: image omitted\n", minutes},
		"us 12-hour": {"WhatsApp Chat with Dana Wolfe.txt", "3/4/21, 9:05:07\u202fPM - Dana Wolfe: Dinner on Friday?\n" +
			"3/4/21, 9:06:00\u202fPM - ~\u202fMe: Yes!\nI'll bring the pie\n12:00 works too\n" +
			"3/4/21, 12:01:02\u202fAM - Dana Wolfe: image omitted\n" +
			"3/14/21, 10:00:00\u202fAM - Dana Wolfe: later\n", append(append([]Line(nil), want...),
			Line{At: time.Date(2021, 3, 14, 10, 0, 0, 0, berlin), Sender: "Dana Wolfe", Body: "later"})},
	} {
		e, err := Read(write(t, "WhatsApp Chat - Dana Wolfe.zip", c.file, c.text), "WhatsApp Chat - Dana Wolfe.zip", berlin)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if e.Title != "WhatsApp Chat - Dana Wolfe" || len(e.Lines) != len(c.want) {
			t.Fatalf("%s: %q, %d lines %+v; want %d", name, e.Title, len(e.Lines), e.Lines, len(c.want))
		}
		for i, l := range e.Lines {
			if !l.At.Equal(c.want[i].At) || l.Minute != c.want[i].Minute || l.Sender != c.want[i].Sender || l.Body != c.want[i].Body {
				t.Errorf("%s: line %d = %+v, want %+v", name, i, l, c.want[i])
			}
		}
	}
}

// A file that holds no chat, or a zip with no one chat in it, is ErrNoChat.
func TestAFileWithNoChatIsRefused(t *testing.T) {
	t.Parallel()
	for name, path := range map[string]string{
		"text":       write(t, "notes.txt", "", "shopping: eggs\nmilk\n"),
		"zip":        write(t, "photos.zip", "readme.md", "04/03/2021, 21:05 - Dana: hi\n"),
		"empty chat": write(t, "chat.zip", "_chat.txt", ""),
	} {
		if _, err := Read(path, filepath.Base(path), berlin); !errors.Is(err, ErrNoChat) {
			t.Errorf("%s: %v, want ErrNoChat", name, err)
		}
	}
}
