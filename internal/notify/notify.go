// Package notify delivers desktop notifications to the user.
package notify

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/godbus/dbus/v5"
)

// Notification is one alert to surface, as separate fields. Body is already trimmed by
// the caller; the rest is verbatim so a template can use each on its own.
type Notification struct {
	// Sender is the sender's display name (their MXID when unresolved).
	Sender string
	// MXID is the sender's Matrix ID.
	MXID string
	// Room is the room's display label.
	Room string
	// Body is the message text.
	Body string
	// Protocol is the network the sender is on — "WhatsApp", "Matrix", … — derived from
	// their MXID by domain.ProtocolOf.
	Protocol string
	// Space is the space the room is in, for {space}.
	Space string
	// Sent is when the message was sent, rendered by {date} and {time} in the local
	// timezone.
	Sent time.Time
	// Date and Time are Sent as the person has times written ([display] formats),
	// for {date} and {time}; empty writes ISO. A hook's KITH_DATE and KITH_TIME are
	// ISO always: scripts parse them.
	Date, Time string
	// Sound is a file to play alongside this notification: the global default from
	// [notifications] sound, or whatever the matching rule replaced it with.
	Sound string
	// Silent suppresses this notification's sound while still showing it — what a
	// sound-only mute does (see MuteSound).
	Silent bool
	// Replaces is a key — the room — whose previous popup this one supersedes.
	Replaces string
}

// Templates are the user's title and body formats for a notification. An empty
// template renders empty.
type Templates struct {
	Title string
	Body  string
}

// dateLayout and timeLayout format {date} and {time} in the local timezone.
const (
	dateLayout = "2006-01-02"
	timeLayout = "15:04"
)

// isoSent is Sent's local date and time in ISO, "" for none.
func (n Notification) isoSent() (date, clock string) {
	if n.Sent.IsZero() {
		return "", ""
	}
	local := n.Sent.Local()
	return local.Format(dateLayout), local.Format(timeLayout)
}

// placeholderRe matches a template field. Unknown names are left alone rather than
// blanked: "{tomorrow}" in a title is text somebody meant to be there.
var placeholderRe = regexp.MustCompile(`\{[a-z]+\}`)

// Render expands the placeholders in tpl — {space} {sender} {mxid} {room} {body}
// {protocol} {date} {time} — against n.
func (n Notification) Render(tpl string) string {
	date, clock := n.isoSent()
	if n.Date != "" {
		date = n.Date
	}
	if n.Time != "" {
		clock = n.Time
	}
	fields := map[string]string{
		"{space}":    n.Space,
		"{sender}":   n.Sender,
		"{mxid}":     n.MXID,
		"{room}":     n.Room,
		"{body}":     n.Body,
		"{protocol}": n.Protocol,
		"{date}":     date,
		"{time}":     clock,
	}

	type piece struct {
		text  string
		field bool
	}
	var pieces []piece
	last := 0
	for _, loc := range placeholderRe.FindAllStringIndex(tpl, -1) {
		value, known := fields[tpl[loc[0]:loc[1]]]
		if !known {
			continue // not ours: it stays part of the surrounding literal
		}
		if lit := tpl[last:loc[0]]; lit != "" {
			pieces = append(pieces, piece{text: lit})
		}
		pieces = append(pieces, piece{text: value, field: true})
		last = loc[1]
	}
	if lit := tpl[last:]; lit != "" {
		pieces = append(pieces, piece{text: lit})
	}

	drop := make([]bool, len(pieces))
	for i, p := range pieces {
		if !p.field || p.text != "" {
			continue
		}
		switch {
		case i > 0 && !pieces[i-1].field && !drop[i-1] && isSeparator(pieces[i-1].text):
			drop[i-1] = true
		case i+1 < len(pieces) && !pieces[i+1].field && isSeparator(pieces[i+1].text):
			drop[i+1] = true
		}
	}

	var out strings.Builder
	for i, p := range pieces {
		if !drop[i] {
			out.WriteString(p.text)
		}
	}
	return strings.TrimSpace(out.String())
}

// isSeparator reports whether a literal between two fields is punctuation joining them
// — " · ", " — ", ", " — as opposed to text that means something on its own.
func isSeparator(lit string) bool {
	trimmed := strings.TrimSpace(lit)
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return false
		}
		switch r {
		case '(', ')', '[', ']', '{', '}', '<', '>', '"', '\'', '`':
			return false
		}
	}
	return true
}

// Notifier surfaces a notification. Implementations must be safe to call from a
// background goroutine and must not block the caller.
type Notifier interface {
	Notify(n Notification)
}

// Nop drops every notification; used when notifications are disabled or in tests.
type Nop struct{}

// Notify does nothing.
func (Nop) Notify(Notification) {}

// Settings are the delivery options that do not vary from one notification to the next:
// which sinks exist, how they render, and how long a popup stays up.
type Settings struct {
	// Desktop posts the built-in freedesktop D-Bus notification.
	Desktop bool
	// Command runs a user hook once per notification.
	Command string
	// SoundCommand plays a notification's sound file, given the path as its only
	// argument.
	SoundCommand string
	// Timeout is how long a desktop popup stays up.
	Timeout time.Duration
	// Templates is the user's title and body format.
	Templates Templates
	// Report, when set, hears every delivery that failed (no session bus, the popup
	// refused, the hook would not start or exited non-zero). Notify has no caller
	// to return to, so without it a broken sink is silent. Called from any goroutine.
	Report func(sink string, err error)
}

// reporter is Settings.Report made safe to call when unset.
type reporter func(sink string, err error)

func (r reporter) failed(sink string, err error) {
	if r != nil && err != nil {
		r(sink, err)
	}
}

// New composes a Notifier from the enabled sinks: the built-in D-Bus desktop
// notification when Desktop is set, a user command hook when Command is non-empty, and
// a sound player when SoundCommand is.
func New(s Settings) Notifier {
	tpl, command, soundCommand, report := s.Templates, s.Command, s.SoundCommand, reporter(s.Report)
	var sinks multi
	if s.Desktop {
		sinks = append(sinks, dbusSink{tpl: tpl, timeout: s.Timeout, ids: &popupIDs{}, report: report})
	}
	if command != "" {
		sinks = append(sinks, commandSink{template: command, tpl: tpl, report: report, run: newBounded()})
	}
	if soundCommand != "" {
		sinks = append(sinks, soundSink{player: soundCommand, report: report, run: newBounded()})
	}
	switch len(sinks) {
	case 0:
		return Nop{}
	case 1:
		return sinks[0]
	default:
		return sinks
	}
}

// multi fans a notification out to several sinks.
type multi []Notifier

// Notify delivers to each sink in turn.
func (ms multi) Notify(n Notification) {
	for _, s := range ms {
		s.Notify(n)
	}
}

// dbusSink posts a freedesktop.org desktop notification over the session bus.
type dbusSink struct {
	tpl     Templates
	timeout time.Duration
	// ids remembers which popup belongs to which coalescing key, so a summary can
	// replace the room's previous one instead of stacking under it.
	ids    *popupIDs
	report reporter
}

// callTimeout bounds the D-Bus round trip to the notification daemon.
const callTimeout = 2 * time.Second

// Notify sends the notification via org.freedesktop.Notifications.
func (d dbusSink) Notify(n Notification) {
	conn, err := dbus.SessionBus() // shared, cached by godbus
	if err != nil {
		d.report.failed("desktop", fmt.Errorf("notify: session bus: %w", err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	var id uint32
	err = obj.CallWithContext(ctx, "org.freedesktop.Notifications.Notify", 0,
		"kith",                // app_name
		d.replacing(n),        // replaces_id (0 = a new popup)
		"",                    // app_icon
		n.Render(d.tpl.Title), // summary
		n.Render(d.tpl.Body),  // body
		[]string{},            // actions
		n.hints(),             // sound-file / suppress-sound
		expireTimeout(d.timeout),
	).Store(&id)
	if err != nil {
		d.report.failed("desktop", fmt.Errorf("notify: desktop popup: %w", err))
		return
	}
	d.remember(n, id)
}

// replacing is the id this notification should take the place of, and remember records
// the id it was given.
func (d dbusSink) replacing(n Notification) uint32 {
	if n.Replaces == "" {
		return 0
	}
	d.ids.mu.Lock()
	defer d.ids.mu.Unlock()
	return d.ids.byKey[n.Replaces]
}

func (d dbusSink) remember(n Notification, id uint32) {
	if n.Replaces == "" || id == 0 {
		return
	}
	d.ids.mu.Lock()
	defer d.ids.mu.Unlock()
	if d.ids.byKey == nil {
		d.ids.byKey = make(map[string]uint32, 8)
	}
	d.ids.byKey[n.Replaces] = id
}

// popupIDs is the map of coalescing key to the id the notification daemon gave the
// popup showing it.
type popupIDs struct {
	mu    sync.Mutex
	byKey map[string]uint32
}

// hints carries the sound decision to the notification daemon.
func (n Notification) hints() map[string]dbus.Variant {
	if n.Silent || n.Sound == "" {
		// suppress-sound is set even with no file of ours to play, because a daemon may
		// have a sound of its own — and a mute that only stopped *our* sound would not
		// be a mute.
		return map[string]dbus.Variant{"suppress-sound": dbus.MakeVariant(true)}
	}
	return map[string]dbus.Variant{"sound-file": dbus.MakeVariant(n.Sound)}
}

// expireTimeout converts a duration to the freedesktop field: milliseconds, with -1
// meaning "the daemon decides" and 0 meaning "stay until dismissed".
func expireTimeout(d time.Duration) int32 {
	switch {
	case d == 0:
		return -1
	case d < 0:
		return 0
	default:
		// #nosec G115 -- min clamps to MaxInt32, so the conversion cannot wrap
		return int32(min(d.Milliseconds(), math.MaxInt32))
	}
}

// commandSink runs a user command per notification.
type commandSink struct {
	template string
	tpl      Templates
	report   reporter
	run      *bounded
}

// Notify spawns the configured command with the notification in its environment.
func (c commandSink) Notify(n Notification) {
	date, clock := n.isoSent()
	env := append(os.Environ(),
		"KITH_TITLE="+n.Render(c.tpl.Title),
		"KITH_BODY="+n.Render(c.tpl.Body),
		"KITH_SENDER="+n.Sender,
		"KITH_MXID="+n.MXID,
		"KITH_ROOM="+n.Room,
		"KITH_SPACE="+n.Space,
		"KITH_MESSAGE="+n.Body,
		"KITH_PROTOCOL="+n.Protocol,
		"KITH_DATE="+date,
		"KITH_TIME="+clock,
	)
	// A hook that fails says so (its exit status, never the message).
	c.run.start("command", "notification command", c.report, func(ctx context.Context) *exec.Cmd {
		cmd := shellCommand(ctx, c.template)
		cmd.Env = env
		return cmd
	})
}

// soundSink plays a notification's sound, if a rule gave it one.
type soundSink struct {
	player string
	report reporter
	run    *bounded
}

// Notify plays n.Sound, and does nothing when there is none or when the
// notification was silenced.
func (s soundSink) Notify(n Notification) {
	if n.Sound == "" || n.Silent {
		return
	}
	s.run.start("sound", "sound player", s.report, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, s.player, n.Sound) // #nosec G204 -- player and path are both user config, passed as argv with no shell
	})
}

// A hook or player that hangs must cost a slot, not a process and a goroutine per
// message: each run is bounded in time, and at most hookSlots run at once per sink.
const (
	hookTimeout = 30 * time.Second
	hookSlots   = 4
	// hookWaitDelay is how long Wait gives a killed child's output pipes to close.
	hookWaitDelay = time.Second
)

// bounded runs a sink's processes under hookTimeout and hookSlots. Shared by pointer,
// since sinks are values.
type bounded struct {
	slots   chan struct{}
	timeout time.Duration
}

func newBounded() *bounded {
	return &bounded{slots: make(chan struct{}, hookSlots), timeout: hookTimeout}
}

// start runs what build makes, unless every slot is taken, and reaps it off the
// caller's goroutine. label names it in errors; sink is what report files it under.
func (b *bounded) start(sink, label string, report reporter, build func(context.Context) *exec.Cmd) {
	select {
	case b.slots <- struct{}{}:
	default:
		report.failed(sink, fmt.Errorf("notify: %d of the %s are still running; skipped this one", cap(b.slots), label))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	cmd := build(ctx)
	cmd.WaitDelay = hookWaitDelay
	if err := cmd.Start(); err != nil {
		cancel()
		<-b.slots
		report.failed(sink, fmt.Errorf("notify: start %s: %w", label, err))
		return
	}
	go func() {
		defer func() { cancel(); <-b.slots }()
		if err := cmd.Wait(); err != nil {
			report.failed(sink, fmt.Errorf("notify: %s: %w", label, err))
		}
	}()
}
