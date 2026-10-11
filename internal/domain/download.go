package domain

import (
	"path/filepath"
	"strings"
	"time"
)

// DownloadPlace is what a download path template can use about the message saved.
type DownloadPlace struct {
	// RoomID is what a rule matches on.
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

// DownloadTarget is a resolved destination: the directory to create and the file name.
type DownloadTarget struct {
	Dir  string
	Name string
}

// Path is the full file path.
func (t DownloadTarget) Path() string { return filepath.Join(t.Dir, t.Name) }

// NoTemplate turns the layout off for a place; an empty template means inherit.
const NoTemplate = "-"

// ResolveDownload works out where an attachment is saved.
func ResolveDownload(dir, template string, rules []DownloadRule, place DownloadPlace) DownloadTarget {
	if rule, ok := narrowestDownloadRule(rules, place); ok {
		if rule.Dir != "" {
			dir = rule.Dir
		}
		if rule.Template != "" {
			template = rule.Template
		}
	}
	// The filename is untrusted: keep only its last element.
	name := safeSegment(filepath.Base(filepath.Clean("/"+place.Name)), "attachment")
	if strings.TrimSpace(template) == "" || template == NoTemplate {
		return DownloadTarget{Dir: dir, Name: name}
	}

	expanded := expandDownloadTemplate(template, place, name)
	// A template of only empty fields still saves the file.
	if expanded == "" {
		return DownloadTarget{Dir: dir, Name: name}
	}
	return DownloadTarget{
		Dir:  filepath.Join(dir, filepath.Dir(expanded)),
		Name: filepath.Base(expanded),
	}
}

// DownloadRule is one per-place override.
type DownloadRule struct {
	// Match is a room ID, a space's name or a thread root; Sender is an MXID.
	Match  string
	Sender string
	Dir    string
	// Template is the layout, or NoTemplate to turn it off for this place.
	Template string
}

// narrowestDownloadRule picks the most specific matching rule (Narrowest).
func narrowestDownloadRule(rules []DownloadRule, place DownloadPlace) (DownloadRule, bool) {
	at, ok := Narrowest(Matches(rules, func(r DownloadRule) Match {
		return Match{Place: r.Match, Sender: r.Sender}
	}), place.scope())
	if !ok {
		return DownloadRule{}, false
	}
	return rules[at], true
}

func (p DownloadPlace) scope() Scope {
	return Scope{RoomID: p.RoomID, Space: p.Space, Thread: p.Thread, Sender: p.Sender}
}

// expandDownloadTemplate fills in fields as safe segments and drops empty segments.
func expandDownloadTemplate(template string, place DownloadPlace, name string) string {
	stem, ext := splitFileName(name)
	fields := map[string]string{
		"{space}":  safeSegment(HomeLabel(place.Space), ""),
		"{room}":   safeSegment(place.Room, ""),
		"{person}": safeSegment(place.Person, ""),
		"{sender}": safeSegment(place.Person, ""),
		"{name}":   safeSegment(stem, "attachment"),
		"{ext}":    safeExt(ext),
		"{date}":   place.Sent.Format("2006-01-02"),
		"{time}":   place.Sent.Format("15-04"), // no colon in filenames
	}
	if place.Sent.IsZero() {
		fields["{date}"], fields["{time}"] = "", ""
	}

	parts := make([]string, 0, 4)
	for segment := range strings.SplitSeq(template, "/") {
		for field, value := range fields {
			segment = strings.ReplaceAll(segment, field, value)
		}
		if segment = strings.Trim(segment, " -_"); segment != "" {
			parts = append(parts, segment)
		}
	}
	return filepath.Join(parts...)
}

// splitFileName splits a filename into {name} and {ext}.
func splitFileName(name string) (stem, ext string) {
	ext = filepath.Ext(name)
	return strings.TrimSuffix(name, ext), ext
}

// safeSegment reduces value to one path segment: no separators, "..", leading dot or
// control characters. fallback is used when nothing survives.
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

// safeExt sanitizes an extension; one ending in "/.." yields "".
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
