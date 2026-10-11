package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Script needs (context it asks for) and outputs (where its stdout goes).

// NeedKind is one kind of context a script can ask for.
type NeedKind int

const (
	NeedNone NeedKind = iota
	// NeedMessage is the message under the cursor: who sent it, what it says, when.
	NeedMessage
	// NeedURL is one http/https link out of that message.
	NeedURL
	// NeedHistory is the last N messages in the room, oldest first.
	NeedHistory
)

// Need is one parsed entry from a script's `needs` list.
type Need struct {
	Kind NeedKind
	// Count is how many, for the kinds that take a number. Zero for the rest.
	Count int
}

// needNames is the single source of the needs vocabulary.
var needNames = map[string]NeedKind{
	"message": NeedMessage,
	"url":     NeedURL,
	"history": NeedHistory,
}

// needsCount is which kinds require a `:n` count.
var needsCount = map[NeedKind]bool{NeedHistory: true}

// MaxHistoryNeed caps `history:n`.
const MaxHistoryNeed = 500

// ParseNeed reads one `needs` entry, with its count where it takes one.
func ParseNeed(entry string) (Need, bool) {
	name, param, hasParam := strings.Cut(strings.TrimSpace(entry), ":")
	kind, known := needNames[strings.ToLower(strings.TrimSpace(name))]
	if !known {
		return Need{}, false
	}
	if !needsCount[kind] {
		// A count on a need that takes none is refused, not ignored.
		if hasParam {
			return Need{}, false
		}
		return Need{Kind: kind}, true
	}
	count, err := strconv.Atoi(strings.TrimSpace(param))
	if err != nil || count < 1 || count > MaxHistoryNeed {
		return Need{}, false
	}
	return Need{Kind: kind, Count: count}, true
}

// String writes a need back the way it is spelled in config.
func (n Need) String() string {
	for name, kind := range needNames {
		if kind != n.Kind {
			continue
		}
		if needsCount[kind] {
			return fmt.Sprintf("%s:%d", name, n.Count)
		}
		return name
	}
	return ""
}

// NeedVocabulary is every need, sorted, for error messages.
func NeedVocabulary() []string {
	out := make([]string, 0, len(needNames))
	for name, kind := range needNames {
		if needsCount[kind] {
			name += ":n"
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ScriptOutput is where what a script prints goes.
type ScriptOutput int

const (
	// OutputCompose replaces the composer's contents; the default.
	OutputCompose ScriptOutput = iota
	// OutputSend posts it to the room unreviewed.
	OutputSend
	// OutputStatus puts one line on the status bar and nothing anywhere else.
	OutputStatus
	// OutputPager shows it in the read-and-dismiss overlay.
	OutputPager
	// OutputClipboard copies it, as the copy keys do.
	OutputClipboard
	// OutputNone discards it, for scripts run for their side effect.
	OutputNone
)

// outputNames is the single source of the output vocabulary.
var outputNames = map[string]ScriptOutput{
	"compose":   OutputCompose,
	"send":      OutputSend,
	"status":    OutputStatus,
	"pager":     OutputPager,
	"clipboard": OutputClipboard,
	"none":      OutputNone,
}

// ParseScriptOutput reads an `output` value; empty is OutputCompose.
func ParseScriptOutput(value string) (ScriptOutput, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return OutputCompose, true
	}
	out, known := outputNames[value]
	return out, known
}

// OutputVocabulary is every output, sorted, for error messages.
func OutputVocabulary() []string {
	out := make([]string, 0, len(outputNames))
	for name := range outputNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
