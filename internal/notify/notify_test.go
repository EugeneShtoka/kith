package notify_test

import (
	"math"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/notify"
)

// tpl is the built-in format, used wherever the test only cares about composition.
var tpl = notify.Templates{Title: "{space} · {room} · {sender}", Body: "{body}"}

func TestNewComposition(t *testing.T) {
	t.Parallel()

	// No sinks enabled → a no-op notifier.
	if _, ok := notify.New(notify.Settings{Templates: tpl}).(notify.Nop); !ok {
		t.Errorf("New with no sinks = %T, want Nop", notify.New(notify.Settings{Templates: tpl}))
	}
	// A command-only notifier is not a Nop and delivers without panicking.
	n := notify.New(notify.Settings{Command: "true", Templates: tpl})
	if _, ok := n.(notify.Nop); ok {
		t.Error("New(false, cmd) should not be a Nop")
	}
	n.Notify(notify.Notification{Sender: "t", Body: "b"}) // must not panic
}

func TestRender(t *testing.T) {
	t.Parallel()

	n := notify.Notification{
		Sender:   "Alice",
		MXID:     "@whatsapp_44700900123:example.org",
		Room:     "Standup",
		Body:     "ping",
		Protocol: "WhatsApp",
		Space:    "Work",
		Sent:     time.Date(2026, 8, 21, 14, 5, 0, 0, time.Local),
	}

	tests := map[string]struct{ tpl, want string }{
		"default title":      {tpl.Title, "Work · Standup · Alice"},
		"default body":       {tpl.Body, "ping"},
		"every placeholder":  {"{space}|{sender}|{mxid}|{room}|{body}|{protocol}|{date}|{time}", "Work|Alice|@whatsapp_44700900123:example.org|Standup|ping|WhatsApp|2026-08-21|14:05"},
		"protocol prefix":    {"[{protocol}] {sender}", "[WhatsApp] Alice"},
		"empty template":     {"", ""},
		"no placeholders":    {"new message", "new message"},
		"unknown left as-is": {"{nope}", "{nope}"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := n.Render(tc.tpl); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.tpl, got, tc.want)
			}
		})
	}
}

// A body that itself looks like a placeholder must not be re-expanded — one
// substitution pass only, so a message can't forge another field.
func TestRenderIsSinglePass(t *testing.T) {
	t.Parallel()

	n := notify.Notification{Sender: "Mallory", Body: "{room}{sender}", Room: "secret"}
	if got, want := n.Render("{body}"), "{room}{sender}"; got != want {
		t.Errorf("Render = %q, want %q (body must not be re-expanded)", got, want)
	}
}

// A zero timestamp renders {date}/{time} as empty rather than year 1.
func TestRenderZeroTime(t *testing.T) {
	t.Parallel()

	if got := (notify.Notification{}).Render("[{date} {time}]"); got != "[ ]" {
		t.Errorf("Render with zero Sent = %q, want %q", got, "[ ]")
	}
}

// A multi-line message body survives templating verbatim.
func TestRenderKeepsNewlines(t *testing.T) {
	t.Parallel()

	n := notify.Notification{Body: "line one\nline two"}
	if got, want := n.Render("{body}"), "line one\nline two"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

// A sound sink only fires when a rule gave the notification a sound, which most have
// not — so the common case must be a no-op rather than an attempt to play nothing.
func TestSoundSinkOnlyPlaysWhenAsked(t *testing.T) {
	t.Parallel()

	// "true" is a program that exists and does nothing, so a play is harmless here.
	n := notify.New(notify.Settings{SoundCommand: "true", Templates: tpl})
	if _, isNop := n.(notify.Nop); isNop {
		t.Fatal("a sound command should compose a sink")
	}
	n.Notify(notify.Notification{Sender: "a", Body: "b"})              // no sound: nothing to play
	n.Notify(notify.Notification{Sender: "a", Body: "b", Sound: "/x"}) // must not panic

	// No sound command means no sink at all, so a rule's sound is simply unheard rather
	// than an error.
	if _, isNop := notify.New(notify.Settings{Templates: tpl}).(notify.Nop); !isNop {
		t.Error("with nothing configured, New should still be a Nop")
	}
}

// The freedesktop expire_timeout is a signed 32-bit millisecond count, where -1 means
// "the daemon decides" and 0 means "stay until dismissed" — so the mapping has to be
// exact at both ends, and must not wrap a long timeout into one of them.
func TestPopupTimeoutMapping(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   time.Duration
		want int32
	}{
		"unset leaves it to the daemon":         {0, -1},
		"negative stays until dismissed":        {-1, 0},
		"fifteen seconds":                       {15 * time.Second, 15000},
		"absurdly long is clamped, not wrapped": {1000 * 24 * time.Hour, math.MaxInt32},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := notify.ExportedExpireTimeout(tc.in); got != tc.want {
				t.Errorf("expireTimeout(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// An empty field takes one separator with it, so a template written for the common case
// reads correctly in the uncommon one.
func TestRenderDropsTheSeparatorOfAnEmptyField(t *testing.T) {
	t.Parallel()

	spaceless := notify.Notification{Sender: "Alice", Room: "Standup", Body: "ping"}
	tests := map[string]struct{ tpl, want string }{
		"leading field takes the separator after it":   {"{space} · {room} · {sender}", "Standup · Alice"},
		"middle field takes the separator before it":   {"{room} · {space} · {sender}", "Standup · Alice"},
		"trailing field takes the separator before it": {"{room} · {space}", "Standup"},
		"every field empty leaves nothing":             {"{space} · {space}", ""},
		// A bracket is not a separator: it describes a shape rather than joining two
		// things, and half a bracket is worse than an empty pair.
		"brackets are kept": {"[{space}] {room}", "[] Standup"},
		// Words are not separators, so nothing around them is dropped — the gap the
		// missing field left is what the template asked for.
		"literal text stays": {"in {space} somewhere", "in  somewhere"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := spaceless.Render(tc.tpl); got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.tpl, got, tc.want)
			}
		})
	}
}
