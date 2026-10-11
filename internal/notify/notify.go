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

// Notification is one alert. Body is trimmed by the caller; other fields are verbatim
// for templates.
type Notification struct {
	// Sender is the sender's display name (their MXID when unresolved).
	Sender string
	// MXID is the sender's Matrix ID.
	MXID string
	// Room is the room's display label.
	Room string
	// Body is the message text.
	Body string
	// Protocol is the sender's network ("WhatsApp", "Matrix", …), from domain.ProtocolOf.
	Protocol string
	// Space is the room's space, for {space}.
	Space string
	// Sent is the send time, rendered by {date} and {time} in local time.
	Sent time.Time
	// Date and Time are Sent in the user's [display] formats; empty means ISO. Hook
	// KITH_DATE and KITH_TIME are always ISO, since scripts parse them.
	Date, Time string
	// Sound is the file to play: the [notifications] default or the matching rule's.
	Sound string
	// Silent shows the notification without sound.
	Silent bool
	// Replaces is a coalescing key (the room) whose previous popup this one supersedes.
	Replaces string
}

// Templates are the user's title and body formats; an empty template renders empty.
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

// placeholderRe matches a template field. Unknown names are kept as literal text.
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
			continue // unknown: stays literal
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

// isSeparator reports whether a literal between fields is joining punctuation such as
// " · " or ", ".
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

// Settings are the per-notifier delivery options: sinks, templates, popup timeout.
type Settings struct {
	// Desktop posts the built-in freedesktop D-Bus notification.
	Desktop bool
	// Command runs a user hook once per notification.
	Command string
	// SoundCommand plays a sound file, given its path as the only argument.
	SoundCommand string
	// Timeout is how long a desktop popup stays up.
	Timeout time.Duration
	// Templates is the user's title and body format.
	Templates Templates
	// Report, when set, receives every failed delivery; Notify returns nothing, so
	// otherwise failures are silent. Called from any goroutine.
	Report func(sink string, err error)
}

// reporter is Settings.Report made safe to call when unset.
type reporter func(sink string, err error)

func (r reporter) failed(sink string, err error) {
	if r != nil && err != nil {
		r(sink, err)
	}
}

// New composes a Notifier from the enabled sinks: D-Bus desktop, command hook, sound player.
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
	// ids maps coalescing keys to popup IDs so a popup replaces the room's previous one.
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

// replacing is the popup id this notification replaces; remember records the id it got.
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

// popupIDs maps a coalescing key to the daemon's id for its popup.
type popupIDs struct {
	mu    sync.Mutex
	byKey map[string]uint32
}

// hints carries the sound decision to the notification daemon.
func (n Notification) hints() map[string]dbus.Variant {
	if n.Silent || n.Sound == "" {
		// Also suppresses the daemon's own default sound.
		return map[string]dbus.Variant{"suppress-sound": dbus.MakeVariant(true)}
	}
	return map[string]dbus.Variant{"sound-file": dbus.MakeVariant(n.Sound)}
}

// expireTimeout converts d to the freedesktop field: milliseconds; -1 is the daemon
// default, 0 is until dismissed.
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
	// Failures report the exit status, never the message.
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

// Notify plays n.Sound unless it is empty or the notification is silent.
func (s soundSink) Notify(n Notification) {
	if n.Sound == "" || n.Silent {
		return
	}
	s.run.start("sound", "sound player", s.report, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, s.player, n.Sound) // #nosec G204 -- player and path are both user config, passed as argv with no shell
	})
}

// Each hook run is time-bounded, with at most hookSlots concurrent per sink, so a hung
// hook cannot pile up processes.
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

// start runs build's command if a slot is free and reaps it on another goroutine.
// label names it in errors; sink keys the report.
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
