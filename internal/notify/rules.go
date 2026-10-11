package notify

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Scope is the context a notification is judged in: where it happened and who sent it.
type Scope struct {
	// Room answers what a rule's Match names, and how broadly.
	Room   Place
	Sender string
	// Thread is the thread root, empty for the main timeline.
	Thread string
	// Participating reports that the user has posted in that thread.
	Participating bool
}

// ThreadMatch narrows a rule by a message's place in a conversation.
type ThreadMatch int

const (
	// ThreadAny (the zero value) matches regardless of thread.
	ThreadAny ThreadMatch = iota
	// ThreadParticipating matches only a message in a thread the user has posted in.
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

// Rule is one statement about which notifications get through.
type Rule struct {
	// Name is the display name, e.g. "Quiet hours".
	Name string
	// Match names a place: a room ID or a space's name.
	Match string
	// Sender narrows the rule to one person's MXID. Empty matches anyone.
	Sender string
	// Thread narrows the rule by thread; see ThreadMatch.
	Thread ThreadMatch
	// When is a daily window the rule is awake in. Nil means always.
	When *Window
	// Show is the bar a message clears to raise a popup.
	Show *Level
	// Ring is the bar to also make a sound.
	Ring *Level
	// Sound is the file to play when it rings.
	Sound string
	// Temp marks a do-not-disturb rule set at runtime rather than read from config.
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

// Remaining is how much of a temporary rule is left; zero if it has no deadline or is not temporary.
func (r Rule) Remaining(now time.Time) time.Duration {
	if !r.Temp || r.Until.IsZero() {
		return 0
	}
	if left := r.Until.Sub(now); left > 0 {
		return left
	}
	return 0
}

// Names reports whether the rule names this place and sender; temporary rules are
// replaced and lifted by this identity.
func (r Rule) Names(match, sender string) bool {
	return r.Match == match && r.Sender == sender
}

// Specificity ranks, narrowest last, decide which rule wins. A sender rule beats a
// room rule; a rule naming both beats either.
const (
	noMatch = iota
	matchGlobal
	matchSpace
	matchRoom
	matchSender
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
			return matchGlobal
		}
		return matchSender
	}
	// Match uses the client-wide place vocabulary: a bare room ID, `room:<name>`,
	// `space:<name>`, `protocol:<network>`, `dm`, `group`.
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

// threadRank is the secondary sort key: among equal-place rules, one with a thread
// condition is narrower and wins.
func (r Rule) threadRank() int {
	if r.Thread == ThreadAny {
		return 0
	}
	return 1
}

// Resolved is the effective result of the rules for one scope at one instant, with the
// rule behind each part.
type Resolved struct {
	// Show and Ring are the effective bars.
	Show, Ring Level
	// Sound is the file to play when a notification rings.
	Sound string
	// ShowBy, RingBy and SoundBy are the rules that last set each.
	ShowBy, RingBy, SoundBy Rule
	// Applied is every matching rule, least specific first, for explaining a decision.
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
	// Ring follows Show unless a rule set it.
	if !ringSet {
		out.Ring, out.RingBy = out.Show, out.ShowBy
	}
	return out
}

// Outcome is what happens to one message, and which rule decided it.
type Outcome struct {
	// Notify raises a popup.
	Notify bool
	// Silent shows it without a sound.
	Silent bool
	// Sound is the file to play when it is not silent.
	Sound string
	// By is the rule whose Show blocked it, whose Ring silenced it, or whose Show admitted it.
	By Rule
}

// Decide applies the resolved bars to one message.
func (r Resolved) Decide(e Event) Outcome {
	if e.Mine {
		// The user's own message, echoed by sync.
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

// Temps is the set of temporary (do-not-disturb) rules the daemon holds.
type Temps []Rule

// Add puts a temporary rule in force, replacing one with the same place and sender.
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

// Live is the rules still in force at now, widest scope first.
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

// Any reports whether any temporary rule is in force at now.
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

// Rules is the standing rules with the temporary ones appended.
func Rules(standing []Rule, temps Temps) []Rule {
	out := make([]Rule, 0, len(standing)+len(temps))
	out = append(out, standing...)
	return append(out, temps...)
}
