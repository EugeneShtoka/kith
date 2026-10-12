package setup

import (
	"reflect"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A tag is referenced as `tag:<name>`. Deleting one leaves other references for the
// person: a rule naming a missing tag is refused, saying where. A `place:<name>` entry
// names whatever the rail shows by that name, so neither touches it.

// RenameTag renames tag from (case-insensitive) to to: its [[tag]], every
// `[not ]tag:<from>` entry in any setting, and the filters and keys naming it.
func RenameTag(cfg config.Config, from, to string) config.Config {
	out := cfg.Clone()
	to = strings.TrimSpace(to)
	rewriteStrings(reflect.ValueOf(&out).Elem(), func(s string) string { return renamedReference(s, from, to) })
	for i := range out.Tags {
		if sameTag(out.Tags[i].Name, from) {
			out.Tags[i].Name = to
		}
	}
	for _, archive := range out.Archives() {
		if sameTag(archive.TagName(), from) {
			archive.Tag = to // the archive goes with its tag, default name or not
		}
	}
	for i := range out.Filters {
		if sameTag(out.Filters[i].Tag, from) {
			out.Filters[i].Tag = to
		}
	}
	for i := range out.Keys.Tag {
		if sameTag(out.Keys.Tag[i].Tag, from) {
			out.Keys.Tag[i].Tag = to
		}
	}
	return out
}

// DeleteTag removes tag name, its entries in the rail's lists and [display] priority,
// and the filters and keys moving rooms into it.
func DeleteTag(cfg config.Config, name string) config.Config {
	out := cfg.Clone()
	out.Tags = slices.DeleteFunc(out.Tags, func(t config.Tag) bool { return sameTag(t.Name, name) })
	names := func(entry string) bool {
		tag, ok := domain.TagOf(entry)
		return ok && sameTag(tag, name)
	}
	out.Display.Rail.Order = slices.DeleteFunc(out.Display.Rail.Order, names)
	out.Display.Rail.Hidden = slices.DeleteFunc(out.Display.Rail.Hidden, names)
	out.Display.Rail.HideWhenEmpty = slices.DeleteFunc(out.Display.Rail.HideWhenEmpty, names)
	out.Display.Priority = slices.DeleteFunc(out.Display.Priority, names)
	out.Filters = slices.DeleteFunc(out.Filters, func(f config.Filter) bool { return sameTag(f.Tag, name) })
	out.Keys.Tag = slices.DeleteFunc(out.Keys.Tag, func(k config.TagKey) bool { return sameTag(k.Tag, name) })
	for _, archive := range out.Archives() {
		if sameTag(archive.Tag, name) {
			archive.Tag = "" // a network's archive then follows no tag (the default, if any)
		}
	}
	return out
}

// renamedReference renames a whole `[not ]tag:<from>` entry; anything else is
// returned unchanged.
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

// rewriteStrings replaces every settable exported string reachable from v with f(s).
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

// CombineTags folds tag from into tag into (case-insensitive), keeping into's settings
// and holding exactly the rooms either held (per held, over rooms). Rules and lists are
// joined; if either rule has a `not` term, or joining would close a cycle, the result
// picks the held rooms by ID instead. References to from move to into, deduplicated.
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
	for i := range rooms {
		if held(a.Name, rooms[i]) || held(b.Name, rooms[i]) {
			exact.Picked = append(exact.Picked, rooms[i].ID)
		}
	}
	if hasNot(a.Rule) || hasNot(b.Rule) {
		return combined(out, fi, ii, from, exact)
	}
	a.Rule = joined(a.Rule, b.Rule)
	a.Picked = joined(a.Picked, b.Picked)
	a.Excluded = joined(unheld(a.Excluded, b.Name, rooms, held), unheld(b.Excluded, a.Name, rooms, held))
	joinedRules := combined(out, fi, ii, from, a)
	// A moved reference can close a cycle (A names C, C named B); pick by ID instead.
	if _, warnings, err := Tags(joinedRules); err != nil || len(warnings) > 0 {
		if _, before, _ := Tags(cfg); len(warnings) > len(before) || err != nil {
			return combined(out, fi, ii, from, exact)
		}
	}
	out = joinedRules
	return out
}

// combined replaces tag ii with merged, drops tag fi, and moves references to from
// onto merged, dropping duplicates and self-references.
func combined(out config.Config, fi, ii int, from string, merged config.Tag) config.Config {
	out = out.Clone()
	out.Tags[ii] = merged
	out.Tags = slices.Delete(out.Tags, fi, fi+1)
	out = RenameTag(out, from, merged.Name) // tag from is gone; only references move
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

// unheld drops exclusions naming a room other holds, replacing each with the IDs of
// its rooms other does not hold (a room:<name> may match several).
func unheld(excluded []string, other string, rooms []domain.RoomFacts, held func(string, domain.RoomFacts) bool) []string {
	var out []string
	for _, entry := range excluded {
		if !slices.ContainsFunc(rooms, func(r domain.RoomFacts) bool { return r.Names(entry) && held(other, r) }) {
			out = append(out, entry)
			continue
		}
		for i := range rooms {
			if rooms[i].Names(entry) && !held(other, rooms[i]) {
				out = append(out, rooms[i].ID)
			}
		}
	}
	return out
}
