package setup

import (
	"reflect"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A tag is named by `tag:<name>` wherever a place is (rules, place lists, the rail
// order, priority, name rules), and by its bare name where the rail takes a row's
// label ([display.rail] hidden and hide_when_empty). Renaming one rewrites all of
// those; deleting one drops it from the rail's lists and priority, and leaves the rest
// for the person, since a rule naming a tag that is gone is refused, saying where.

// RenameTag is cfg with the tag named from (case-insensitive) called to: its [[tag]],
// every `tag:<from>` entry (after `not ` too) in any setting, and every bare-name
// entry in the rail's hidden and hide_when_empty. Nothing else changes.
func RenameTag(cfg config.Config, from, to string) config.Config {
	out := cfg.Clone()
	to = strings.TrimSpace(to)
	rewriteStrings(reflect.ValueOf(&out).Elem(), func(s string) string { return renamedReference(s, from, to) })
	for i := range out.Tags {
		if sameTag(out.Tags[i].Name, from) {
			out.Tags[i].Name = to
		}
	}
	rename := func(entries []string) {
		for i := range entries {
			if sameTag(entries[i], from) {
				entries[i] = to
			}
		}
	}
	rename(out.Display.Rail.Hidden)
	rename(out.Display.Rail.HideWhenEmpty)
	for _, archive := range out.Archives() {
		if sameTag(archive.TagName(), from) {
			archive.Tag = to // the archive goes with its tag, default name or not
		}
	}
	return out
}

// DeleteTag is cfg without the tag named name, and without it in the rail's lists and
// [display] priority, where naming a tag that is gone would only be clutter.
func DeleteTag(cfg config.Config, name string) config.Config {
	out := cfg.Clone()
	out.Tags = slices.DeleteFunc(out.Tags, func(t config.Tag) bool { return sameTag(t.Name, name) })
	names := func(entry string) bool {
		tag, ok := domain.TagOf(entry)
		return (ok && sameTag(tag, name)) || sameTag(entry, name)
	}
	out.Display.Rail.Order = slices.DeleteFunc(out.Display.Rail.Order, names)
	out.Display.Rail.Hidden = slices.DeleteFunc(out.Display.Rail.Hidden, names)
	out.Display.Rail.HideWhenEmpty = slices.DeleteFunc(out.Display.Rail.HideWhenEmpty, names)
	out.Display.Priority = slices.DeleteFunc(out.Display.Priority, func(entry string) bool {
		tag, ok := domain.TagOf(entry)
		return ok && sameTag(tag, name)
	})
	for _, archive := range out.Archives() {
		if sameTag(archive.Tag, name) {
			archive.Tag = "" // a network's archive then follows no tag (the default, if any)
		}
	}
	return out
}

// renamedReference is s with a reference to the tag from renamed to to: `tag:<from>`
// or `not tag:<from>`, whole; anything else is s unchanged.
func renamedReference(s, from, to string) string {
	body, not := strings.TrimSpace(s), ""
	if len(body) > len("not ") && strings.EqualFold(body[:len("not ")], "not ") {
		body, not = strings.TrimSpace(body[len("not "):]), "not "
	}
	name, ok := domain.TagOf(body)
	if !ok || !sameTag(name, from) {
		return s
	}
	return not + domain.TagEntry(to)
}

func sameTag(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }

// rewriteStrings replaces every string reachable from v (settable, exported) with
// what f makes of it: struct fields, slice elements and pointed-to values.
func rewriteStrings(v reflect.Value, f func(string) string) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(f(v.String()))
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			rewriteStrings(v.Elem(), f)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			rewriteStrings(v.Index(i), f)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			elem := reflect.New(it.Value().Type()).Elem()
			elem.Set(it.Value())
			rewriteStrings(elem, f)
			v.SetMapIndex(it.Key(), elem)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				rewriteStrings(v.Field(i), f)
			}
		}
	default:
	}
}

// CombineTags is cfg with the tag named from folded into the tag named into (both
// exist; case-insensitive): what renaming from to into's name means. The combined tag
// holds exactly what either held — held says whether a tag holds a room now, among
// rooms, every room an entry may name — and keeps into's settings. Where either rule
// has a `not` term, which would start filtering the other tag's rooms, both are
// replaced by the rooms they hold, picked by ID; otherwise the rules are joined, the
// lists joined, and an exclusion dropped where the other tag holds the room — unless a
// reference moved onto the combined tag would close a cycle through it, when it holds
// the rooms instead too. Every
// reference to from (rules, place lists, rail, priority, a network's archive) names
// into after, with what that leaves twice, or a tag naming itself, dropped.
func CombineTags(cfg config.Config, from, into string, rooms []domain.RoomFacts, held func(tag string, room domain.RoomFacts) bool) config.Config {
	out := cfg.Clone()
	fi := slices.IndexFunc(out.Tags, func(t config.Tag) bool { return sameTag(t.Name, from) })
	ii := slices.IndexFunc(out.Tags, func(t config.Tag) bool { return sameTag(t.Name, into) })
	if fi < 0 || ii < 0 || fi == ii {
		return out
	}
	a, b := out.Tags[ii], out.Tags[fi]
	exact := a
	exact.Rule, exact.Excluded, exact.Picked = nil, nil, nil
	for _, r := range rooms {
		if held(a.Name, r) || held(b.Name, r) {
			exact.Picked = append(exact.Picked, r.ID)
		}
	}
	if hasNot(a.Rule) || hasNot(b.Rule) {
		return combined(out, fi, ii, from, exact)
	}
	a.Rule = joined(a.Rule, b.Rule)
	a.Picked = joined(a.Picked, b.Picked)
	a.Excluded = joined(unheld(a.Excluded, b.Name, rooms, held), unheld(b.Excluded, a.Name, rooms, held))
	joinedRules := combined(out, fi, ii, from, a)
	// A reference moved onto the combined tag can close a cycle through it (A names C,
	// C named B), whose references then match nothing: the rooms it holds instead.
	if _, warnings, err := Tags(joinedRules); err != nil || len(warnings) > 0 {
		if _, before, _ := Tags(cfg); len(warnings) > len(before) || err != nil {
			return combined(out, fi, ii, from, exact)
		}
	}
	out = joinedRules
	return out
}

// combined is out with tag fi gone, tag ii made merged, and every reference to from
// moved onto it — what is then listed twice, or a tag naming itself, dropped.
func combined(out config.Config, fi, ii int, from string, merged config.Tag) config.Config {
	out = out.Clone()
	out.Tags[ii] = merged
	out.Tags = slices.Delete(out.Tags, fi, fi+1)
	out = RenameTag(out, from, merged.Name) // no tag is called from now: only references move
	for i := range out.Tags {
		if sameTag(out.Tags[i].Name, merged.Name) {
			out.Tags[i].Rule = slices.DeleteFunc(out.Tags[i].Rule, func(term string) bool {
				name, ok := domain.TagOf(strings.TrimPrefix(strings.TrimSpace(term), "not "))
				return ok && sameTag(name, merged.Name)
			})
		}
	}
	out.Display.Rail.Order = joined(out.Display.Rail.Order)
	out.Display.Rail.Hidden = joined(out.Display.Rail.Hidden)
	out.Display.Rail.HideWhenEmpty = joined(out.Display.Rail.HideWhenEmpty)
	out.Display.Priority = joined(out.Display.Priority)
	return out
}

// hasNot reports whether a rule has a `not` term.
func hasNot(rule []string) bool {
	return slices.ContainsFunc(rule, func(term string) bool {
		t := strings.TrimSpace(term)
		return len(t) > len("not ") && strings.EqualFold(t[:len("not ")], "not ")
	})
}

// joined is the lists' entries in order, each once (case-insensitive).
func joined(lists ...[]string) []string {
	var out []string
	for _, list := range lists {
		for _, e := range list {
			if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(strings.TrimSpace(o), strings.TrimSpace(e)) }) {
				out = append(out, e)
			}
		}
	}
	return out
}

// unheld is the exclusions less the rooms the tag other holds: an entry naming one of
// those is dropped, and replaced by the IDs of the rooms it named that other does not
// hold (a room:<name> two rooms share), so those stay out.
func unheld(excluded []string, other string, rooms []domain.RoomFacts, held func(string, domain.RoomFacts) bool) []string {
	var out []string
	for _, entry := range excluded {
		if !slices.ContainsFunc(rooms, func(r domain.RoomFacts) bool { return r.Names(entry) && held(other, r) }) {
			out = append(out, entry)
			continue
		}
		for _, r := range rooms {
			if r.Names(entry) && !held(other, r) {
				out = append(out, r.ID)
			}
		}
	}
	return out
}
