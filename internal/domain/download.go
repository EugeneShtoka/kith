package domain

import (
	"path/filepath"
	"strings"
	"time"
)

// Where a saved attachment goes.

// DownloadPlace is what the template has to work with: everything about the message
// being saved that a person might want in the path.
type DownloadPlace struct {
	// RoomID is what a rule matches on; a displayed name is not a handle.
	RoomID string
	Space  string
	Room   string
	Person string
	// Thread is the root event, empty for the main timeline.
	Thread string
	// Sender is the MXID behind Person.
	Sender string
	// Name is the attachment's own filename, extension included.
	Name string
	// Sent is the message's timestamp, for {date} and {time}.
	Sent time.Time
}

// DownloadTarget is a resolved destination: the directory to create, and the file to
// write inside it.
type DownloadTarget struct {
	Dir  string
	Name string
}

// Path is the whole thing, for saying where a file went.
func (t DownloadTarget) Path() string { return filepath.Join(t.Dir, t.Name) }

// NoTemplate is what a per-place rule writes to mean "no layout here", which an empty
// string cannot say because empty means "inherit".
const NoTemplate = "-"

// ResolveDownload works out where one attachment belongs.
func ResolveDownload(dir, template string, rules []DownloadRule, place DownloadPlace) DownloadTarget {
	if rule, ok := narrowestDownloadRule(rules, place); ok {
		if rule.Dir != "" {
			dir = rule.Dir
		}
		if rule.Template != "" {
			template = rule.Template
		}
	}
	// The filename comes off a message a stranger wrote, so only its last element is
	// taken before anything else looks at it.
	name := safeSegment(filepath.Base(filepath.Clean("/"+place.Name)), "attachment")
	if strings.TrimSpace(template) == "" || template == NoTemplate {
		return DownloadTarget{Dir: dir, Name: name}
	}

	expanded := expandDownloadTemplate(template, place, name)
	// A template that expands to nothing but empty segments — "{space}/{room}" in a
	// room that is in no space and has no name — must still save the file somewhere.
	if expanded == "" {
		return DownloadTarget{Dir: dir, Name: name}
	}
	return DownloadTarget{
		Dir:  filepath.Join(dir, filepath.Dir(expanded)),
		Name: filepath.Base(expanded),
	}
}

// DownloadRule is one per-place override, in the pure layer's own terms.
type DownloadRule struct {
	// Match is a room ID, a space's name or a thread root; Sender is an MXID.
	Match  string
	Sender string
	Dir    string
	// Template is the layout, or NoTemplate to turn it off for this place.
	Template string
}

// narrowestDownloadRule picks the rule that applies — the most specific one, by the
// ladder every other per-place setting is resolved with.
func narrowestDownloadRule(rules []DownloadRule, place DownloadPlace) (DownloadRule, bool) {
	at, ok := Narrowest(Matches(rules, func(r DownloadRule) Match {
		return Match{Place: r.Match, Sender: r.Sender}
	}), place.scope())
	if !ok {
		return DownloadRule{}, false
	}
	return rules[at], true
}

// scope is the DownloadPlace as the rule ladder sees it.
func (p DownloadPlace) scope() Scope {
	return Scope{RoomID: p.RoomID, Space: p.Space, Thread: p.Thread, Sender: p.Sender}
}

// expandDownloadTemplate fills the fields in, one safe segment each, and drops the path
// separators an empty field leaves behind.
func expandDownloadTemplate(template string, place DownloadPlace, name string) string {
	stem, ext := splitFileName(name)
	fields := map[string]string{
		"{space}":  safeSegment(place.Space, ""),
		"{room}":   safeSegment(place.Room, ""),
		"{person}": safeSegment(place.Person, ""),
		"{sender}": safeSegment(place.Person, ""),
		"{name}":   safeSegment(stem, "attachment"),
		"{ext}":    safeExt(ext),
		"{date}":   place.Sent.Format("2006-01-02"),
		"{time}":   place.Sent.Format("15-04"), // not HH:MM — a colon is a poor filename
	}
	if place.Sent.IsZero() {
		fields["{date}"], fields["{time}"] = "", ""
	}

	parts := make([]string, 0, 4)
	for segment := range strings.SplitSeq(template, "/") {
		for field, value := range fields {
			segment = strings.ReplaceAll(segment, field, value)
		}
		// A separator is meaningful; a segment that expanded to nothing is not.
		if segment = strings.Trim(segment, " -_"); segment != "" {
			parts = append(parts, segment)
		}
	}
	return filepath.Join(parts...)
}

// splitFileName divides a filename into the part a template calls {name} and the part it
// calls {ext}.
func splitFileName(name string) (stem, ext string) {
	ext = filepath.Ext(name)
	return strings.TrimSuffix(name, ext), ext
}

// safeSegment reduces a value to one path segment that cannot traverse anywhere: no
// separators, no "..", no leading dot to hide the file, and no control characters.
// fallback is used when nothing survives.
func safeSegment(value, fallback string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == 0:
			return '-'
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, value)
	// Any run of dots collapses to one.
	for strings.Contains(value, "..") {
		value = strings.ReplaceAll(value, "..", ".")
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, ".-_ ")
	if value == "" {
		return fallback
	}
	return value
}

// safeExt keeps an extension that looks like one and drops anything else, so a
// "filename" ending in "/.." contributes nothing.
func safeExt(ext string) string {
	if ext == "" {
		return ""
	}
	cleaned := safeSegment(strings.TrimPrefix(ext, "."), "")
	if cleaned == "" {
		return ""
	}
	return "." + cleaned
}
