package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Do-not-disturb, asked for here and held by the daemon so attached terminals agree.
// A mute says what (room, person, space, account) and for how long; it is a
// temporary notification rule (internal/notify) writing show = none or ring = none.

// Notifications is the daemon's notification state, as much as the client acts on.
// Declared here because only daemon.Remote implements it.
type Notifications interface {
	// SetDND puts one temporary rule in force. Every method returns the whole set
	// afterwards.
	SetDND(ctx context.Context, rule notify.Rule) (notify.Temps, error)
	// ClearDND lifts the rule naming this place and person, ClearAllDND every one.
	ClearDND(ctx context.Context, match, sender string) (notify.Temps, error)
	ClearAllDND(ctx context.Context) (notify.Temps, error)
	// DND reports the temporary rules in force.
	DND(ctx context.Context) (notify.Temps, error)
	// ReloadConfig asks the daemon to re-read the configuration file, returning the
	// parse error when it will not load.
	ReloadConfig(ctx context.Context) error
}

// muteTarget is what a do-not-disturb entry being authored will cover.
type muteTarget struct {
	// kind is the scope in a word (room, person, space, global), display only.
	kind string
	// match and sender are the rule's scope: a room ID or space name, and an MXID.
	match, sender string
	// label is the short name, for the picker row and the rule's name.
	label string
	// what is the full phrase for the status line: "Alice, anywhere".
	what string
}

// dndDuration is one offered length of silence.
type dndDuration struct {
	key   string
	label string
	// span is how long it lasts; zero means until it is turned off.
	span time.Duration
}

// dndDurations are the lengths offered; the daemon clears every mute on restart.
var dndDurations = []dndDuration{
	{key: "1h", label: "1 hour", span: time.Hour},
	{key: "2h", label: "2 hours", span: 2 * time.Hour},
	{key: "4h", label: "4 hours", span: 4 * time.Hour},
	{key: "8h", label: "8 hours", span: 8 * time.Hour},
	{key: "24h", label: "24 hours", span: 24 * time.Hour},
	{key: "off", label: "Until I turn it off", span: 0},
}

// muteAxis is which half of a notification the pressed key takes away: picker is the
// scope chooser to open, silence writes the axis into a rule.
type muteAxis struct {
	picker  pickerKind
	silence func(*notify.Rule)
}

// hideAxis takes the notification away entirely; soundAxis takes only its noise.
var (
	hideAxis = muteAxis{
		picker:  pickerDNDScope,
		silence: func(r *notify.Rule) { r.Show = new(notify.LevelNone) },
	}
	soundAxis = muteAxis{
		picker:  pickerMuteScope,
		silence: func(r *notify.Rule) { r.Ring = new(notify.LevelNone) },
	}
)

// notifyState is what the client knows about notifications, plus the half-finished
// do-not-disturb flow (scopes, target, axis), started and finished as a unit.
type notifyState struct {
	// backend is nil without a daemon, and the keys say so.
	backend Notifications
	// on is [notifications] enabled: whether anything is delivered at all.
	on bool
	// rules are the standing rules, held only to describe the silence in the badge.
	rules []notify.Rule
	// temps is the daemon's do-not-disturb rules, cached for the badge; each carries
	// its own deadline, so the badge lapses correctly between refreshes.
	temps notify.Temps

	scopes []muteTarget
	target muteTarget
	axis   muteAxis
}

// attached reports whether a daemon is there to ask.
func (n notifyState) attached() bool { return n.backend != nil }

// holding reports whether any temporary rule is in force at now.
func (n notifyState) holding(now time.Time) bool { return n.temps.Any(now) }

// starting opens the flow for one axis over a set of scopes.
func (n notifyState) starting(axis muteAxis, scopes []muteTarget) notifyState {
	n.axis, n.scopes, n.target = axis, scopes, muteTarget{}
	return n
}

// finished ends the flow, whether or not it reached a length.
func (n notifyState) finished() notifyState {
	n.axis, n.scopes, n.target = muteAxis{}, nil, muteTarget{}
	return n
}

// scopeAt is the scope the picker's index names, and whether the index names one.
func (n notifyState) scopeAt(index int) (muteTarget, bool) {
	if index < 0 || index >= len(n.scopes) {
		return muteTarget{}, false
	}
	return n.scopes[index], true
}

// toggleDND and toggleMute are the two keys; the key decides what the mute takes
// away. Either key, with anything already muted, lifts everything.
func (m Model) toggleDND() (Model, tea.Cmd) { return m.openMute(hideAxis) }

// toggleMute silences the sound and leaves the popups.
func (m Model) toggleMute() (Model, tea.Cmd) { return m.openMute(soundAxis) }

// openMute starts the flow for one axis, or lifts what is in force.
func (m Model) openMute(axis muteAxis) (Model, tea.Cmd) {
	if !m.notifications.attached() {
		m = m.say("do not disturb lives in kithd, and none is attached")
		return m, nil
	}
	if m.notifications.holding(time.Now()) {
		m = m.say("lifting do not disturb…")
		return m, m.clearAllDNDCmd()
	}
	m.notifications = m.notifications.starting(axis, m.muteTargets())
	// Only the account to choose: go straight to the length.
	if len(m.notifications.scopes) == 1 {
		return m.chooseDNDScope(0)
	}
	m.picker = newPicker(axis.picker, muteScopeItems(m.notifications.scopes))
	return m, nil
}

// globalMuteLabel names the account-wide row.
const globalMuteLabel = "all notifications"

// muteTargets is what can be silenced from here, narrowest first: this room, the
// selected message's sender, the room's spaces, the account.
func (m Model) muteTargets() []muteTarget {
	var targets []muteTarget

	room, hasRoom := m.currentRoom()
	inRoom := hasRoom && !room.IsInvite()
	if inRoom {
		name := m.roomName(room)
		targets = append(targets, muteTarget{
			kind: "room", match: string(room.ID), label: name, what: name,
		})
	}
	if msg, ok := m.selectedMessage(); ok && msg.Sender != m.me {
		who := m.senderName(msg)
		targets = append(targets, muteTarget{
			kind: "person", sender: msg.Sender, label: who, what: who + ", anywhere",
		})
	}
	if inRoom {
		for _, space := range m.spacesOf(room.ID) {
			shown := isolate(space)
			targets = append(targets, muteTarget{
				kind: "space", match: domain.SpaceEntry(space), label: shown, what: "everything in " + shown,
			})
		}
	}
	return append(targets, muteTarget{
		kind: "global", label: globalMuteLabel, what: globalMuteLabel,
	})
}

// muteScopeItems lists the scopes: short name, kind as qualifier, and both the short
// and long forms to filter against.
func muteScopeItems(targets []muteTarget) []pickerItem {
	items := make([]pickerItem, 0, len(targets))
	for i, target := range targets {
		items = append(items, pickerItem{
			label:  target.label,
			detail: target.kind,
			value:  fmt.Sprint(i),
			match:  target.label + " " + target.what,
		})
	}
	return items
}

// chooseDNDScope moves from what to how long.
func (m Model) chooseDNDScope(index int) (Model, tea.Cmd) {
	target, ok := m.notifications.scopeAt(index)
	if !ok {
		m = m.closePicker()
		m.notifications = m.notifications.finished()
		return m, nil
	}
	m.notifications.target = target
	m.picker = newPicker(pickerDNDFor, dndDurationItems())
	return m, nil
}

// dndDurationItems lists the lengths, each with the clock time it ends at.
func dndDurationItems() []pickerItem {
	now := time.Now()
	items := make([]pickerItem, 0, len(dndDurations))
	for _, d := range dndDurations {
		detail := "no end"
		if d.span > 0 {
			detail = "until " + now.Add(d.span).Format("15:04")
		}
		items = append(items, pickerItem{label: d.label, detail: detail, value: d.key, match: d.label})
	}
	return items
}

// chooseDNDFor sets the mute, sending an instant rather than a duration so the end
// time survives reconnects.
func (m Model) chooseDNDFor(key string) (Model, tea.Cmd) {
	target, axis := m.notifications.target, m.notifications.axis
	m = m.closePicker()
	m.notifications = m.notifications.finished()
	for _, d := range dndDurations {
		if d.key != key {
			continue
		}
		var until time.Time
		if d.span > 0 {
			until = time.Now().Add(d.span)
		}
		m = m.say("silencing " + target.what + " " + forHowLong(d))
		rule := notify.Rule{
			// Stored, so logical: the label carries isolate marks for drawing.
			Name: stripIsolates(target.label), Match: target.match, Sender: target.sender,
			Temp: true, Until: until,
		}
		axis.silence(&rule)
		return m, m.setDNDCmd(rule, target)
	}
	return m, nil
}

// forHowLong says the length in the status line's voice.
func forHowLong(d dndDuration) string {
	if d.span == 0 {
		return "until you turn it off"
	}
	return "for " + d.label
}

// handleDND takes the daemon's answer: every call returns the whole set, including
// other terminals' changes.
func (m Model) handleDND(msg dndMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m = m.sayErr("do not disturb", msg.err)
		return m, nil
	}
	m.notifications.temps = msg.temps
	if msg.note != "" {
		m = m.say(msg.note)
	}
	return m, nil
}

// handlePollTick re-reads state changed elsewhere (drafts, spam, mutes) and always
// reschedules itself, even with no notification backend attached yet.
func (m Model) handlePollTick() (Model, tea.Cmd) {
	cmds := []tea.Cmd{m.pollTickCmd(), m.loadDraftsCmd(), m.loadSpamCmd()}
	if m.notifications.attached() {
		cmds = append(cmds, m.readDNDCmd())
	}
	return m, tea.Batch(cmds...)
}

// silenceBadge marks notifications being held back and names what is responsible:
// notifications off; the account-wide mute with its remaining time; a count of
// scoped mutes; or the standing rule silencing the open room.
func (m Model) silenceBadge() string {
	if !m.notifications.on {
		// A setting rather than a chosen silence, so muted styling, not a badge.
		return m.theme.Muted.Bold(true).Render(" notifications off ")
	}
	now := time.Now()
	if global, ok := m.notifications.temps.Global(now); ok {
		return m.theme.Badge(true).Render(" " + badgeWord(global) + remainingNote(global, now) + " ")
	}
	if live := m.notifications.temps.Live(now); len(live) > 0 {
		return m.theme.Badge(true).Render(fmt.Sprintf(" %s ×%d ", badgeWord(live[0]), len(live)))
	}
	if name, ok := m.silencedBy(now); ok {
		return m.theme.Badge(false).Render(" " + drawSentence("quiet: "+isolate(name)) + " ")
	}
	return ""
}

// silencedBy names the standing rule silencing the open room, resolved with no
// sender (person rules cannot be judged without a message).
func (m Model) silencedBy(now time.Time) (string, bool) {
	room, ok := m.currentRoom()
	if !ok {
		return "", false
	}
	// An empty set resolves to a zero Show, which would falsely read as silenced.
	if len(m.notifications.rules) == 0 {
		return "", false
	}
	resolved := notify.Resolve(m.notifications.rules, notify.Scope{Room: setup.Place{Room: m.factsFor(room)}}, now)
	if resolved.Show != notify.LevelNone {
		return "", false
	}
	return ruleName(resolved.ShowBy), true
}

// ruleName is a rule's name on screen, or the narrowest thing it names.
func ruleName(r notify.Rule) string {
	switch {
	case r.Name != "":
		return r.Name
	case r.Sender != "":
		return r.Sender
	case r.Match != "":
		return r.Match
	default:
		// The account-wide default from [notifications].
		return "all rooms"
	}
}

// badgeWord names the axis: "dnd" hides notifications, "muted" only silences them.
func badgeWord(r notify.Rule) string {
	if r.Show == nil {
		return "muted"
	}
	return "dnd"
}

// remainingNote is how much of a timed mute is left, in whole minutes.
func remainingNote(r notify.Rule, now time.Time) string {
	left := r.Remaining(now)
	if left <= 0 {
		return ""
	}
	// Rounded up (never "0m" while in force), before splitting hours (no "60m").
	mins := int((left + time.Minute - 1) / time.Minute)
	if mins < 60 {
		return fmt.Sprintf(" %dm", mins)
	}
	return fmt.Sprintf(" %dh%02dm", mins/60, mins%60)
}

// mutedNote names what was silenced, once the daemon has confirmed it.
func mutedNote(target muteTarget, temps notify.Temps) string {
	for i := range temps {
		rule := &temps[i]
		if !rule.Names(target.match, target.sender) {
			continue
		}
		note := "do not disturb: " + target.what
		if rule.Show == nil {
			note = "sound off: " + target.what
		}
		if !rule.Until.IsZero() {
			note += " until " + rule.Until.Format("15:04")
		}
		return note
	}
	return "do not disturb: " + target.what
}
