package domain

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// A filter can put a person into a tag, as it moves a room: a person verdict, kept per
// ID and tag. While the tag is muted the person is hidden in every room. A person
// taking them out releases them, and no filter puts a released person back.

// PersonVerdict is one person's mark into one tag, by one of their IDs, or its release.
type PersonVerdict struct {
	Person string
	Tag    string
	Rule   MoveRule
	Filter string
	At     time.Time
	// Released means a person took them out; no filter may put them back.
	Released bool
}

// In reports whether this verdict puts the person in its tag; a release never does.
func (v PersonVerdict) In() bool { return v.Rule != MoveNone && !v.Released }

// WithPersonVerdict is a verdict set with v applied: a mark or a release replaces the
// person's entry for that tag, anything else drops it. A filter's mark never overwrites
// a release.
func WithPersonVerdict(set []PersonVerdict, v PersonVerdict) ([]PersonVerdict, error) {
	same := func(o PersonVerdict) bool { return o.Person == v.Person && SameTag(o.Tag, v.Tag) }
	at := slices.IndexFunc(set, same)
	if at >= 0 && v.In() && set[at].Released {
		return set, fmt.Errorf("%s in %s: %w", v.Person, v.Tag, ErrPersonReleased)
	}
	out := slices.Clone(set)
	if at >= 0 {
		out = slices.Delete(out, at, at+1)
	}
	if v.In() || v.Released {
		out = append(out, v)
	}
	return out, nil
}

// RetaggedPeople is a verdict set with tag from's entries moved to tag to, or dropped
// when to is empty; changed reports whether any moved. Where a person has an entry in
// to already (two tags combined), a release wins, else to's own entry stays.
func RetaggedPeople(set []PersonVerdict, from, to string) (out []PersonVerdict, changed bool) {
	out = slices.Clone(set)
	for i := 0; i < len(out); {
		if !SameTag(out[i].Tag, from) {
			i++
			continue
		}
		moving := out[i]
		out = slices.Delete(out, i, i+1)
		changed = true
		if to == "" {
			continue
		}
		moving.Tag = to
		into := slices.IndexFunc(out, func(o PersonVerdict) bool { return o.Person == moving.Person && SameTag(o.Tag, to) })
		switch {
		case into < 0:
			out = append(out, moving)
		case moving.Released && !out[into].Released:
			out[into] = moving
		}
	}
	return out, changed
}

// PersonCandidate is a person a rescan of cached history would mark into a tag, and the
// message that marks them.
type PersonCandidate struct {
	Verdict PersonVerdict
	Sample  Message
}

// ErrPersonReleased refuses a filter's mark of a person someone took out of that tag.
var ErrPersonReleased = errors.New("the person was taken out of that tag")

// MutedTags is the names of every tag with notifies = false: the ones whose people,
// listed or put there by a verdict, are hidden.
func (s TagSet) MutedTags() []string {
	var out []string
	for i := range s.tags {
		if s.tags[i].tag.Mute {
			out = append(out, s.tags[i].tag.Name)
		}
	}
	return out
}

// HiddenByVerdict is the people verdicts put in one of the muted tags.
func HiddenByVerdict(verdicts []PersonVerdict, muted []string) []string {
	var out []string
	for _, v := range verdicts {
		if v.In() && slices.ContainsFunc(muted, func(tag string) bool { return SameTag(tag, v.Tag) }) &&
			!slices.Contains(out, v.Person) {
			out = append(out, v.Person)
		}
	}
	return out
}
