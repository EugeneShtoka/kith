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
