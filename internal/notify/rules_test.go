package notify_test

import (
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/notify"
)

// window is a pointer helper: a rule's fields are pointers because "unset" has to be
// distinguishable from "set to none", which is the difference between a rule that
// leaves an axis alone and one that silences it.
func window(t *testing.T, start, end string) *notify.Window {
	t.Helper()
	w, err := notify.ParseWhen(start + "-" + end)
	if err != nil {
		t.Fatalf("ParseWhen(%s-%s) = %v", start, end, err)
	}
	return &w
}

// clock builds a local wall-clock time on a fixed day. place stands in for the caller's
// place vocabulary.
type place struct {
	room  string // the entry that names this one room
	class string // an entry that names a set containing it: a space, a network, a kind
}

func (p place) Reach(entry string) notify.Breadth {
	switch entry {
	case "":
		return notify.NoPlace
	case p.room:
		return notify.OneRoom
	case p.class:
		return notify.ClassOfRooms
	default:
		return notify.NoPlace
	}
}

func clock(hour, minute int) time.Time {
	return time.Date(2026, 8, 23, hour, minute, 0, 0, time.Local)
}

var (
	mention = notify.Event{Mentioned: true}
	plain   = notify.Event{}
)

// The account-wide policy is a rule with no scope.
func TestGlobalRuleIsJustARule(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{{Name: "global", Show: new(notify.LevelMention)}}
	got := notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}}, clock(12, 0))
	if got.Show != notify.LevelMention {
		t.Errorf("Show = %v, want mention", got.Show)
	}
	if got.ShowBy.Name != "global" {
		t.Errorf("ShowBy = %q, want the global rule to be named as the reason", got.ShowBy.Name)
	}
	if out := got.Decide(mention); !out.Notify {
		t.Error("a mention should notify under the global rule")
	}
	if out := got.Decide(plain); out.Notify {
		t.Error("an ordinary message should not")
	}
}

// Quiet hours are a rule with a schedule, and nothing else.
func TestQuietHoursIsAScheduledRule(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{
		{Name: "global", Show: new(notify.LevelAll)},
		{Name: "Quiet hours", When: window(t, "22:00", "08:00"), Show: new(notify.LevelNone)},
	}
	scope := notify.Scope{Room: place{room: "!a:x"}}

	if out := notify.Resolve(rules, scope, clock(12, 0)).Decide(plain); !out.Notify {
		t.Error("midday should notify: the quiet rule is not awake")
	}
	night := notify.Resolve(rules, scope, clock(23, 0))
	out := night.Decide(plain)
	if out.Notify {
		t.Error("23:00 falls inside quiet hours, so nothing should notify")
	}
	if out.By.Name != "Quiet hours" {
		t.Errorf("the silence is attributed to %q, want the quiet-hours rule — an unexplainable silence is the failure mode this model exists to prevent", out.By.Name)
	}
}

// What used to need `pierce`: an exception during quiet hours. It is now a narrower
// rule, resolved by specificity like everything else.
func TestNarrowerRulePiercesWithoutAPierce(t *testing.T) {
	t.Parallel()

	quiet := window(t, "22:00", "08:00")
	rules := []notify.Rule{
		{Name: "global", Show: new(notify.LevelAll)},
		{Name: "Quiet hours", When: quiet, Show: new(notify.LevelNone)},
		{Name: "Alice at night", Sender: "@alice:x", When: quiet, Show: new(notify.LevelAll), Ring: new(notify.LevelNone)},
	}
	night := clock(23, 30)

	other := notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}, Sender: "@bob:x"}, night)
	if other.Decide(plain).Notify {
		t.Error("Bob is still covered by quiet hours")
	}

	alice := notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}, Sender: "@alice:x"}, night)
	out := alice.Decide(plain)
	if !out.Notify {
		t.Fatal("Alice's rule is narrower than quiet hours, so it should reach us")
	}
	if !out.Silent {
		t.Error("...and seen-not-heard, because her rule sets ring = none")
	}
	if out.By.Name != "Alice at night" {
		t.Errorf("the sound was taken by %q, want Alice's own rule", out.By.Name)
	}
}

// Do-not-disturb is a temporary rule.
func TestGlobalDNDIsTheBlanketAndNarrowRulesSurviveIt(t *testing.T) {
	t.Parallel()

	standing := []notify.Rule{
		{Name: "global", Show: new(notify.LevelAll)},
		{Name: "Alice", Sender: "@alice:x", Show: new(notify.LevelAll)},
	}
	dnd := notify.Temps{}.Add(notify.Rule{Name: "all notifications", Show: new(notify.LevelNone)})
	now := clock(12, 0)
	rules := notify.Rules(standing, dnd)

	if notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}, Sender: "@bob:x"}, now).Decide(plain).Notify {
		t.Error("Bob is covered by account-wide do-not-disturb")
	}
	if !notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}, Sender: "@alice:x"}, now).Decide(plain).Notify {
		t.Error("Alice's rule is narrower than the blanket and should still get through")
	}
}

// ...and muting Alice is *also* sender-scope, so it beats her standing rule by being
// the newer statement. A mute you just set is never a silent no-op.
func TestTemporaryRuleBeatsAStandingOneOfTheSameScope(t *testing.T) {
	t.Parallel()

	standing := []notify.Rule{
		{Name: "global", Show: new(notify.LevelAll)},
		{Name: "Alice", Sender: "@alice:x", Show: new(notify.LevelAll)},
	}
	muted := notify.Temps{}.Add(notify.Rule{Name: "Alice", Sender: "@alice:x", Show: new(notify.LevelNone)})
	now := clock(12, 0)

	out := notify.Resolve(notify.Rules(standing, muted), notify.Scope{Room: place{room: "!a:x"}, Sender: "@alice:x"}, now).Decide(plain)
	if out.Notify {
		t.Error("muting Alice must beat the standing rule that lets her through")
	}
	if !out.By.Temp {
		t.Error("and the mute should be named as the reason, not the standing rule")
	}
}

// A rule sets only what it names; the rest is inherited from the less specific answer.
func TestRuleOverridesOnlyWhatItSets(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{
		{Name: "global", Show: new(notify.LevelMention), Sound: "/base.wav"},
		{Name: "Work", Match: "space:Work", Sound: "/work.wav"},
	}
	got := notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x", class: "space:Work"}}, clock(12, 0))
	if got.Show != notify.LevelMention {
		t.Errorf("Show = %v, want the global mention to survive a rule that only sets a sound", got.Show)
	}
	if got.Sound != "/work.wav" {
		t.Errorf("Sound = %q, want the space's own", got.Sound)
	}
}

// Ring follows Show unless something pulls them apart, which is what makes two axes
// cost nothing: a config that never mentions ring behaves exactly as one axis would.
func TestRingFollowsShowUnlessSet(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{{Show: new(notify.LevelAll)}}
	got := notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}}, clock(12, 0))
	if got.Ring != notify.LevelAll {
		t.Errorf("Ring = %v, want it to follow Show", got.Ring)
	}
	if out := got.Decide(plain); out.Silent {
		t.Error("a notification with nothing said about sound should make one")
	}
}

// Show = none is silent by construction: ring is only asked after show passes, which
// is why ctrl+n writes one field and says nothing about sound.
func TestShowNoneNeedsNoRingToBeSilent(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{
		{Show: new(notify.LevelAll), Ring: new(notify.LevelAll)},
		{Name: "hidden", Match: "!a:x", Show: new(notify.LevelNone)},
	}
	out := notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}}, clock(12, 0)).Decide(plain)
	if out.Notify || out.Silent {
		t.Errorf("a hidden room should produce no notification at all, got %+v", out)
	}
}

// specificityLadder is every kind of rule matching one message, least specific first:
// more constraints beat fewer, and among one constraint a person beats a room beats a
// space, with the account-wide rule at the bottom.
func specificityLadder() (notify.Scope, []notify.Rule) {
	scope := notify.Scope{Room: place{room: "!room:x", class: "space:Work"}, Sender: "@alice:x"}
	return scope, []notify.Rule{
		{Name: "global"},
		{Name: "space", Match: "space:Work"},
		{Name: "room", Match: "!room:x"},
		{Name: "person", Sender: "@alice:x"},
		{Name: "space+person", Match: "space:Work", Sender: "@alice:x"},
		{Name: "room+person", Match: "!room:x", Sender: "@alice:x"},
	}
}

// Every rung of the ladder beats every rung below it, whichever comes first in the
// file: each pair, both ways round.
func TestEveryRungBeatsTheOnesBelow(t *testing.T) {
	t.Parallel()
	scope, ladder := specificityLadder()
	for hi := range ladder {
		for lo := range hi {
			for _, rules := range [][]notify.Rule{{ladder[lo], ladder[hi]}, {ladder[hi], ladder[lo]}} {
				rules = slices.Clone(rules)
				for i := range rules {
					rules[i].Show = new(notify.LevelAll)
				}
				if got := notify.Resolve(rules, scope, clock(12, 0)).ShowBy.Name; got != ladder[hi].Name {
					t.Errorf("%s against %s: %s won, want %s", ladder[hi].Name, ladder[lo].Name, got, ladder[hi].Name)
				}
			}
		}
	}
}

// A rule about a person holds wherever they are: a bot muted is muted in a room let
// through whole, and someone always let through gets through a room muted whole.
func TestAPersonsRuleHoldsInAnyRoom(t *testing.T) {
	t.Parallel()
	room := place{room: "!group:x"}
	bot := []notify.Rule{
		{Name: "the group", Match: "!group:x", Show: new(notify.LevelAll)},
		{Name: "the bot", Sender: "@bot:x", Show: new(notify.LevelNone)},
	}
	if notify.Resolve(bot, notify.Scope{Room: room, Sender: "@bot:x"}, clock(12, 0)).Decide(plain).Notify {
		t.Error("the muted bot notified in a room set to show everything")
	}
	if !notify.Resolve(bot, notify.Scope{Room: room, Sender: "@dana:x"}, clock(12, 0)).Decide(plain).Notify {
		t.Error("someone else in that room did not notify")
	}
	partner := []notify.Rule{
		{Name: "the group", Match: "!group:x", Show: new(notify.LevelNone)},
		{Name: "partner", Sender: "@sam:x", Show: new(notify.LevelAll)},
	}
	if !notify.Resolve(partner, notify.Scope{Room: room, Sender: "@sam:x"}, clock(12, 0)).Decide(plain).Notify {
		t.Error("someone always let through was silenced by a muted room")
	}
	if notify.Resolve(partner, notify.Scope{Room: room, Sender: "@dana:x"}, clock(12, 0)).Decide(plain).Notify {
		t.Error("the muted room let someone else through")
	}
}

// The specificity ladder, pinned whole: the most specific rule has the last word, and
// the chain starts at the account-wide rule.
func TestSpecificityOrder(t *testing.T) {
	t.Parallel()

	scope, ladder := specificityLadder()
	// Each rung sets Show to a distinct level so the winner is identifiable; they are
	// fed in ladder order and then in reverse, because a resolution that depended on
	// input order would pass the first and fail the second.
	for i := range ladder {
		ladder[i].Show = new(notify.LevelAll)
	}
	forward := make([]notify.Rule, len(ladder))
	copy(forward, ladder)
	backward := make([]notify.Rule, len(ladder))
	for i, r := range ladder {
		backward[len(ladder)-1-i] = r
	}
	for name, rules := range map[string][]notify.Rule{"in order": forward, "reversed": backward} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := notify.Resolve(rules, scope, clock(12, 0))
			if got.ShowBy.Name != "room+person" {
				t.Errorf("the winner is %q, want room+person", got.ShowBy.Name)
			}
			if len(got.Applied) != len(ladder) {
				t.Errorf("Applied has %d rules, want all %d in the chain", len(got.Applied), len(ladder))
			}
			if got.Applied[0].Name != "global" {
				t.Errorf("the chain starts with %q, want the least specific first", got.Applied[0].Name)
			}
		})
	}
}

// A rule that matches nothing here takes no part, and a scope nothing matches leaves
// the defaults standing — which are silent, because a rule set that says nothing should
// not invent a notification.
func TestNoMatchesIsSilent(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{{Name: "elsewhere", Match: "!other:x", Show: new(notify.LevelAll)}}
	got := notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}}, clock(12, 0))
	if got.Show != notify.LevelNone || len(got.Applied) != 0 {
		t.Errorf("Resolve = %+v, want nothing applied and nothing admitted", got)
	}
	if got.Decide(mention).Notify {
		t.Error("no rule, no notification")
	}
}

// Our own message never notifies, whatever the rules say.
func TestOwnMessageNeverNotifies(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{{Show: new(notify.LevelAll)}}
	if notify.Resolve(rules, notify.Scope{Room: place{room: "!a:x"}}, clock(12, 0)).Decide(notify.Event{Mine: true}).Notify {
		t.Error("our own message must not notify")
	}
}

// Temporary rules: replaced by name, lifted by name, and lapsed by their own deadline
// without anything having to fire.
func TestTempsAddReplacesAndExpires(t *testing.T) {
	t.Parallel()

	now := clock(12, 0)
	temps := notify.Temps{}.
		Add(notify.Rule{Name: "Standup", Match: "!standup:x", Show: new(notify.LevelNone), Until: now.Add(time.Hour)}).
		Add(notify.Rule{Name: "Standup", Match: "!standup:x", Show: new(notify.LevelNone), Until: now.Add(4 * time.Hour)})
	if len(temps) != 1 {
		t.Fatalf("two mutes on one room = %d entries, want one with the later deadline", len(temps))
	}
	if got := temps[0].Remaining(now); got != 4*time.Hour {
		t.Errorf("Remaining = %v, want the later deadline to have replaced the earlier", got)
	}
	if !temps.Any(now) {
		t.Error("Any should see it")
	}
	if temps.Any(now.Add(5 * time.Hour)) {
		t.Error("a lapsed rule is not in force: expiry is computed from the deadline, never scheduled")
	}
	if got := temps.Remove("!standup:x", ""); len(got) != 0 {
		t.Errorf("Remove left %+v, want nothing", got)
	}
}

// A lapsed temporary rule stops resolving, with no timer having fired and nothing
// having been cleaned up.
func TestLapsedTempStopsApplying(t *testing.T) {
	t.Parallel()

	now := clock(12, 0)
	standing := []notify.Rule{{Show: new(notify.LevelAll)}}
	temps := notify.Temps{}.Add(notify.Rule{Match: "!a:x", Show: new(notify.LevelNone), Until: now.Add(time.Hour)})
	rules := notify.Rules(standing, temps)
	scope := notify.Scope{Room: place{room: "!a:x"}}

	if notify.Resolve(rules, scope, now).Decide(plain).Notify {
		t.Error("the room is muted right now")
	}
	if !notify.Resolve(rules, scope, now.Add(2*time.Hour)).Decide(plain).Notify {
		t.Error("two hours later the mute has lapsed and the room is loud again")
	}
}

// The account-wide temporary rule is the one the badge treats differently, so it has
// to be findable without knowing what else is in force.
func TestTempsGlobal(t *testing.T) {
	t.Parallel()

	now := clock(12, 0)
	temps := notify.Temps{}.
		Add(notify.Rule{Name: "Standup", Match: "!standup:x", Show: new(notify.LevelNone)}).
		Add(notify.Rule{Name: "all notifications", Show: new(notify.LevelNone), Until: now.Add(time.Hour)})
	got, ok := temps.Global(now)
	if !ok || got.Name != "all notifications" {
		t.Fatalf("Global = %+v, %v, want the account-wide rule", got, ok)
	}
	if got.Remaining(now) != time.Hour {
		t.Errorf("Remaining = %v, want an hour", got.Remaining(now))
	}
	if _, ok := (notify.Temps{}).Global(now); ok {
		t.Error("nothing in force means no global rule")
	}
}

// A thread clause narrows *where* a rule applies, and a rule without one is unchanged
// by this existing at all — which is what makes every config written before it mean
// exactly what it meant.
func TestAThreadClauseNarrowsWhereARuleApplies(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{
		{Name: "everything", Show: new(notify.LevelAll)},
		{Name: "threads I am not in", Show: new(notify.LevelNone)},
		{Name: "threads I am in", Thread: notify.ThreadParticipating, Show: new(notify.LevelAll)},
	}
	// The second rule silences the room; the third takes it back for a conversation
	// this account has spoken in, by being the narrower statement about the same place.
	inThread := notify.Scope{Room: place{room: "!a:x"}, Thread: "$root", Participating: true}
	if got := notify.Resolve(rules, inThread, clock(12, 0)); !got.Decide(plain).Notify {
		t.Errorf("a thread we are in should notify, silenced by %q", got.ShowBy.Name)
	}
	away := notify.Scope{Room: place{room: "!a:x"}, Thread: "$root"}
	if got := notify.Resolve(rules, away, clock(12, 0)); got.Decide(plain).Notify {
		t.Error("a thread we have not spoken in should stay silent")
	}
}

// "none" is the main timeline and nothing else, which is the other half of the same
// distinction: a rule can speak about the room without speaking about the conversations
// inside it.
func TestAThreadClauseOfNoneIsTheMainTimeline(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{
		{Name: "everything", Show: new(notify.LevelNone)},
		{Name: "the room itself", Thread: notify.ThreadNone, Show: new(notify.LevelAll)},
	}
	main := notify.Scope{Room: place{room: "!a:x"}}
	if got := notify.Resolve(rules, main, clock(12, 0)); !got.Decide(plain).Notify {
		t.Errorf("the main timeline should notify, silenced by %q", got.ShowBy.Name)
	}
	inThread := notify.Scope{Room: place{room: "!a:x"}, Thread: "$root", Participating: true}
	if got := notify.Resolve(rules, inThread, clock(12, 0)); got.Decide(plain).Notify {
		t.Error("a thread should not be admitted by a rule that says main timeline only")
	}
}

// A thread clause narrows a rule within its place, and does not move it up the place
// ladder.
func TestAThreadClauseDoesNotOutrankAPlace(t *testing.T) {
	t.Parallel()

	rules := []notify.Rule{
		{Name: "threads anywhere", Thread: notify.ThreadParticipating, Show: new(notify.LevelNone)},
		{Name: "this room", Match: "!a:x", Show: new(notify.LevelAll)},
	}
	s := notify.Scope{Room: place{room: "!a:x"}, Thread: "$root", Participating: true}
	got := notify.Resolve(rules, s, clock(12, 0))
	if got.ShowBy.Name != "this room" {
		t.Errorf("ShowBy = %q, want the rule naming the room to have the last word", got.ShowBy.Name)
	}
}

// The spellings are refused rather than defaulted, and "any" is the same statement as
// saying nothing — a rule that widens where the user asked it to narrow is the one
// mistake a notification setting must not make.
func TestParseThread(t *testing.T) {
	t.Parallel()

	for _, spelling := range []string{"", "any", "ANY", " participating ", "none"} {
		if _, err := notify.ParseThread(spelling); err != nil {
			t.Errorf("ParseThread(%q) = %v, want it accepted", spelling, err)
		}
	}
	if got, err := notify.ParseThread("participate"); err == nil {
		t.Errorf("ParseThread(\"participate\") = %v, want a refusal", got)
	}
}
