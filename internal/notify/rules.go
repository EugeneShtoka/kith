package notify

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// One rule, and only rules.

// Scope is the context one notification is judged in: where it happened and who sent
// it. Spaces is a list because a room can belong to several.
type Scope struct {
	// Room answers what a rule's Match names, and how broadly.
	Room   Place
	Sender string
	// Thread is the conversation the message is in — its root — and empty for the
	// room's main timeline.
	Thread string
	// Participating reports that we have spoken in that thread, which is the
	// distinction every client's thread setting is actually about: a conversation you
	// joined is one you asked to hear about.
	Participating bool
}

// ThreadMatch narrows a rule by a message's place in a conversation.
type ThreadMatch int

const (
	// ThreadAny is the default and the zero value: the rule does not ask about threads,
	// so it applies to a message wherever it sits.
	ThreadAny ThreadMatch = iota
	// ThreadParticipating matches only a message in a thread we have spoken in.
	ThreadParticipating
	// ThreadNone matches only a message that is in no thread at all.
	ThreadNone
)

// threadNames maps each ThreadMatch to the spelling used in the config file.
var threadNames = map[ThreadMatch]string{
	ThreadAny:           "any",
	ThreadParticipating: "participating",
	ThreadNone:          "none",
}

// String returns the clause's config spelling.
func (t ThreadMatch) String() string {
	if name, ok := threadNames[t]; ok {
		return name
	}
	return "thread(" + strconv.Itoa(int(t)) + ")"
}

// ParseThread reads a rule's thread clause from its config spelling.
func ParseThread(name string) (ThreadMatch, error) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return ThreadAny, nil
	}
	for match, spelling := range threadNames {
		if spelling == trimmed {
			return match, nil
		}
	}
	return ThreadAny, fmt.Errorf("unknown thread condition %q (want any, participating or none)", name)
}

// Rule is one statement about what gets through, and the only such statement there is.
type Rule struct {
	// Name is how a person says this rule — "Quiet hours", "Alice", "Standup".
	Name string
	// Match names a place: a room ID or a space's name.
	Match string
	// Sender narrows the rule to one person's MXID. Empty matches anyone.
	Sender string
	// Thread narrows the rule to a message's place in a conversation: unset asks
	// nothing, "participating" is only threads we have spoken in, "none" is only the
	// main timeline.
	Thread ThreadMatch
	// When is a daily window the rule is awake in. Nil means always.
	When *Window
	// Show is the bar a message clears to raise a popup at all.
	Show *Level
	// Ring is the bar to also make a sound.
	Ring *Level
	// Sound is the file to play when it rings.
	Sound string
	// Temp marks a rule written by a keystroke rather than read from the config: this
	// is what do-not-disturb is.
	Temp bool
	// Until is when a temporary rule lapses. Zero means until it is turned off.
	Until time.Time
}

// Live reports whether the rule is in force at now: its window covers the clock, or
// its deadline has not passed.
func (r Rule) Live(now time.Time) bool {
	if r.Temp {
		return r.Until.IsZero() || now.Before(r.Until)
	}
	return r.When == nil || r.When.Covers(now)
}

// Remaining is how much of a temporary rule is left, or zero when it has no deadline
// or is not temporary.
func (r Rule) Remaining(now time.Time) time.Duration {
	if !r.Temp || r.Until.IsZero() {
		return 0
	}
	if left := r.Until.Sub(now); left > 0 {
		return left
	}
	return 0
}

// Names reports whether the rule speaks about this exact thing, which is the identity a
// temporary rule is replaced and lifted by: two rules naming the same place and the
// same person are one statement said twice.
func (r Rule) Names(match, sender string) bool {
	return r.Match == match && r.Sender == sender
}

// Specificity is how narrowly a rule matched, and it decides which rule wins.
const (
	noMatch = iota
	matchGlobal
	matchSpace
	matchSender
	matchRoom
	matchSpaceSender
	matchRoomSender
)

// specificity reports how narrowly r matches s, or noMatch.
func (r Rule) specificity(s Scope) int {
	senderOK := r.Sender == "" || r.Sender == s.Sender
	if !senderOK || !r.matchesThread(s) {
		return noMatch
	}
	hasSender := r.Sender != ""

	if r.Match == "" {
		if !hasSender {
			// Constrained by nothing: the account-wide rule, which every other rule
			// refines.
			return matchGlobal
		}
		return matchSender // constrained only by who, so: this person anywhere
	}
	// One vocabulary for *where*, shared with every filter in this client: a room ID
	// bare, `room:<name>`, `space:<name>`, `protocol:<network>`, `dm`, `group`.
	if s.Room == nil {
		return noMatch
	}
	switch s.Room.Reach(r.Match) {
	case OneRoom:
		if hasSender {
			return matchRoomSender
		}
		return matchRoom
	case ClassOfRooms:
		if hasSender {
			return matchSpaceSender
		}
		return matchSpace
	case NoPlace:
		return noMatch
	default:
		return noMatch
	}
}

// Breadth is how far a rule's Match reached: nothing, a set of rooms, or one room.
type Breadth int

const (
	// NoPlace is an entry that does not describe this room at all.
	NoPlace Breadth = iota
	// ClassOfRooms is a space, a network, or every DM — a set named by a property.
	ClassOfRooms
	// OneRoom is this room, by its ID or the name it is shown under.
	OneRoom
)

// Place answers what one rule entry names about the room a notification is judged in.
type Place interface {
	Reach(entry string) Breadth
}

// matchesThread reports whether the message sits where the rule's thread clause asks.
func (r Rule) matchesThread(s Scope) bool {
	switch r.Thread {
	case ThreadParticipating:
		return s.Thread != "" && s.Participating
	case ThreadNone:
		return s.Thread == ""
	default:
		return true
	}
}

// threadRank is the secondary key: a rule that names a thread condition is the narrower
// statement among rules matching the same place, and so has the last word over one that
// does not.
func (r Rule) threadRank() int {
	if r.Thread == ThreadAny {
		return 0
	}
	return 1
}

// Resolved is what the rules add up to for one scope at one instant, and — the part
// that matters as much as the answer — *which rule* produced each half of it.
type Resolved struct {
	// Show and Ring are the effective bars.
	Show, Ring Level
	// Sound is the file to play when a notification rings.
	Sound string
	// ShowBy, RingBy and SoundBy are the rules that last set each.
	ShowBy, RingBy, SoundBy Rule
	// Applied is every rule that took part, least specific first — the chain, for
	// explaining a decision rather than merely stating it.
	Applied []Rule
}

// Resolve layers the rules in force at now onto each other, least specific first, so
// the most specific match has the last word.
func Resolve(rules []Rule, s Scope, now time.Time) Resolved {
	type scored struct {
		rule Rule
		rank int
		at   int
	}
	matched := make([]scored, 0, len(rules))
	for i := range rules {
		if !rules[i].Live(now) {
			continue
		}
		if rank := rules[i].specificity(s); rank != noMatch {
			matched = append(matched, scored{rule: rules[i], rank: rank, at: i})
		}
	}
	sort.SliceStable(matched, func(a, b int) bool {
		if matched[a].rank != matched[b].rank {
			return matched[a].rank < matched[b].rank
		}
		if ta, tb := matched[a].rule.threadRank(), matched[b].rule.threadRank(); ta != tb {
			return ta < tb
		}
		if matched[a].rule.Temp != matched[b].rule.Temp {
			return !matched[a].rule.Temp
		}
		return matched[a].at < matched[b].at
	})

	var out Resolved
	ringSet := false
	for i := range matched {
		rule := matched[i].rule
		out.Applied = append(out.Applied, rule)
		if rule.Show != nil {
			out.Show, out.ShowBy = *rule.Show, rule
		}
		if rule.Ring != nil {
			out.Ring, out.RingBy, ringSet = *rule.Ring, rule, true
		}
		if rule.Sound != "" {
			out.Sound, out.SoundBy = rule.Sound, rule
		}
	}
	// Ring follows Show unless something said otherwise: a notification that appears
	// makes its noise, which is what every version of this feature has done.
	if !ringSet {
		out.Ring, out.RingBy = out.Show, out.ShowBy
	}
	return out
}

// Outcome is what happens to one message, and which rule is answerable for it.
type Outcome struct {
	// Notify raises a popup.
	Notify bool
	// Silent shows it without a sound.
	Silent bool
	// Sound is the file to play when it is not silent.
	Sound string
	// By is the rule that decided: the one whose Show held the message back, or whose
	// Ring took its sound, or — when the notification goes out whole — the one that
	// admitted it.
	By Rule
}

// Decide applies the resolved bars to one message.
func (r Resolved) Decide(e Event) Outcome {
	if e.Mine {
		// Our own message, echoed back by sync.
		return Outcome{}
	}
	if !r.Show.Admits(e) {
		return Outcome{By: r.ShowBy}
	}
	if !r.Ring.Admits(e) {
		return Outcome{Notify: true, Silent: true, By: r.RingBy}
	}
	return Outcome{Notify: true, Sound: r.Sound, By: r.ShowBy}
}

// Temps is the set of temporary rules the daemon holds — do-not-disturb, as a set
// rather than a switch.
type Temps []Rule

// Add puts one temporary rule in force.
func (t Temps) Add(r Rule) Temps {
	r.Temp = true
	out := make(Temps, 0, len(t)+1)
	for i := range t {
		if !t[i].Names(r.Match, r.Sender) {
			out = append(out, t[i])
		}
	}
	return append(out, r)
}

// Remove lifts the rule naming this place and person.
func (t Temps) Remove(match, sender string) Temps {
	out := make(Temps, 0, len(t))
	for i := range t {
		if !t[i].Names(match, sender) {
			out = append(out, t[i])
		}
	}
	return out
}

// Live is the rules still in force at now, widest scope first — the order they should
// be *read* in, which is the opposite of the order the chooser asks in.
func (t Temps) Live(now time.Time) Temps {
	out := make(Temps, 0, len(t))
	for i := range t {
		if t[i].Live(now) {
			out = append(out, t[i])
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].specificity(anyScope) < out[b].specificity(anyScope)
	})
	return out
}

// anyScope ranks a temporary rule by shape alone, for listing.
var anyScope = Scope{}

// Any reports whether anything is in force at now — what one keystroke asks before
// deciding whether it is setting a silence or lifting one.
func (t Temps) Any(now time.Time) bool {
	for i := range t {
		if t[i].Live(now) {
			return true
		}
	}
	return false
}

// Global is the account-wide temporary rule, if one is in force.
func (t Temps) Global(now time.Time) (Rule, bool) {
	for i := range t {
		if t[i].Match == "" && t[i].Sender == "" && t[i].Live(now) {
			return t[i], true
		}
	}
	return Rule{}, false
}

// Rules is a standing rule set with the temporary ones layered on, which is what the
// decision is actually made against.
func Rules(standing []Rule, temps Temps) []Rule {
	out := make([]Rule, 0, len(standing)+len(temps))
	out = append(out, standing...)
	return append(out, temps...)
}
