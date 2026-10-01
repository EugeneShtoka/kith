package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Whether the model layer may see one room, and what it is allowed to be told.

// ModelPermit is what the model layer is allowed to do in one room.
type ModelPermit struct {
	// Allowed is whether a request may be made at all.
	Allowed bool
	// Refusal names why not, in words a status line can show. Empty when Allowed.
	Refusal string
}

// Reasons a room is refused, spelled once so the UI and the tests agree.
const (
	// ModelRefusedOff is no endpoint configured — the default state of the feature.
	ModelRefusedOff = "no model endpoint configured"
	// ModelRefusedRoom is a room the lists do not admit — either it is missing from a
	// non-empty `rooms`, or it is named in `except`.
	ModelRefusedRoom = "this room is not one [assist] allows"
	// ModelRefusedEncrypted is an encrypted room while encrypted rooms are excluded.
	ModelRefusedEncrypted = "this room is encrypted; set [assist] encrypted = true to include those"
	// ModelRefusedNothingListed is a Listed scope whose allow list is empty: nothing
	// has been shared, so no room is admitted.
	ModelRefusedNothingListed = "no rooms are listed, so nothing is shared"
)

// ModelScope is which rooms the model layer may see.
type ModelScope struct {
	// Only, when it names anything, is the whole of what is allowed.
	Only []string
	// Except is what is refused when Only names nothing. Empty on both means every room.
	Except []string
	// Encrypted admits end-to-end encrypted rooms to the lists at all.
	Encrypted bool
	// Listed is least privilege: empty Only admits nothing, and Except subtracts from
	// Only.
	Listed bool
}

// NeedsPlaces reports whether a decision reads where a room sits — a space: or
// protocol: entry in either list. When the places are unknown, only a scope that does not
// can be answered without guessing.
func (s ModelScope) NeedsPlaces() bool { return NamesAPlace(s.Only) || NamesAPlace(s.Except) }

// NamesAPlace reports whether a list has a space: or protocol: entry.
func NamesAPlace(list []string) bool {
	return slices.ContainsFunc(list, func(entry string) bool {
		entry = strings.TrimSpace(entry)
		return hasPrefixFold(entry, entrySpace) || hasPrefixFold(entry, entryProtocol)
	})
}

// Shares reports whether this scope can admit any room at all without looking at one —
// false only for a Listed scope whose Only names nothing.
func (s ModelScope) Shares() bool {
	return !s.Listed || len(nonEmpty(s.Only)) > 0
}

// AllowModel decides whether the model may be asked about one room.
func AllowModel(scope ModelScope, room RoomFacts, endpoint string, encrypted bool) ModelPermit {
	if strings.TrimSpace(endpoint) == "" {
		return ModelPermit{Refusal: ModelRefusedOff}
	}
	if !scope.Shares() {
		return ModelPermit{Refusal: ModelRefusedNothingListed}
	}
	if encrypted && !scope.Encrypted {
		return ModelPermit{Refusal: ModelRefusedEncrypted}
	}
	// Listed: Only (non-empty, see Shares) minus Except. Otherwise Only alone when it
	// names anything, else everything but Except.
	hasOnly := len(nonEmpty(scope.Only)) > 0
	if (hasOnly && !namesAny(scope.Only, room)) ||
		((scope.Listed || !hasOnly) && namesAny(scope.Except, room)) {
		return ModelPermit{Refusal: ModelRefusedRoom}
	}
	return ModelPermit{Allowed: true}
}

// nonEmpty drops blank entries, so a list holding only whitespace is the empty list it
// looks like rather than a whitelist admitting nothing.
func nonEmpty(list []string) []string {
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if strings.TrimSpace(entry) != "" {
			out = append(out, entry)
		}
	}
	return out
}

// namesAny reports whether any entry in the list describes this room.
func namesAny(list []string, room RoomFacts) bool {
	return slices.ContainsFunc(list, room.Names)
}

// ModelContext is the conversation quoted to the model, newest last, bounded.
func ModelContext(messages []Message, me string, budget int) []string {
	if budget <= 0 {
		return nil
	}
	var out []string
	spent := 0
	for i := range slices.Backward(messages) {
		body := strings.TrimSpace(messages[i].Body)
		if body == "" || messages[i].Redacted {
			continue
		}
		cost := len(body)/charsPerToken + 1
		if spent+cost > budget {
			break
		}
		spent += cost
		out = append(out, speaker(messages[i], me)+": "+body)
	}
	slices.Reverse(out) // oldest first, as said
	return out
}

// speaker is how one message is attributed in the quoted conversation.
func speaker(msg Message, me string) string {
	if me != "" && msg.Sender == me {
		return "You"
	}
	if msg.SenderName != "" {
		return msg.SenderName
	}
	if IsUserID(msg.Sender) {
		if short := ShortName(msg.Sender); short != "" {
			return short
		}
	}
	return "someone"
}

// charsPerToken is the estimate the budget is counted in.
const charsPerToken = 4

// ModelRequest is one ask of the model layer.
type ModelRequest struct {
	// Task is which prompt to use — "complete" or "rewrite".
	Task        string
	RoomID      RoomID // decides permission and what may be quoted
	Draft       string
	Instruction string  // for tasks that take one ("shorter")
	ReplyTo     EventID // the reply target or thread root the draft answers
	Span        SummarySpan
	// DryRun assembles the request and sends nothing, for the preview overlay.
	DryRun bool
}

// The task names, spelled here so the config, the RPC and the UI agree.
const (
	// ModelComplete continues the sentence being written.
	ModelComplete = "complete"
	// ModelRewrite rewrites a draft to an instruction.
	ModelRewrite = "rewrite"
	// ModelTodo is what people are waiting on you for, across rooms.
	ModelTodo = "todo"
	// ModelSummary says what a conversation has been about.
	ModelSummary = "summary"
	// ModelThreadName names a thread from what is in it.
	ModelThreadName = "thread_name"
)

// ThreadNameLimit is the longest a generated thread name may be, in runes.
const ThreadNameLimit = 48

// ThreadNameFrom turns a model's reply into a label, or "" if there is nothing usable.
func ThreadNameFrom(reply string) string {
	lines := make([]string, 0, 4)
	for line := range strings.SplitSeq(reply, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	name := lines[0]
	// A line ending in a colon with something after it is a preamble, not an answer.
	if len(lines) > 1 && strings.HasSuffix(name, ":") {
		name = lines[1]
	}
	// Strip markdown emphasis, bullets and quotes.
	name = strings.Trim(name, "-*#> \t")
	name = strings.Trim(name, "\"'“”‘’`")
	name = strings.TrimRight(name, ".,;:")
	name = strings.Join(strings.Fields(name), " ")
	// Far over the limit means the model misunderstood; the snippet is a better label.
	if len([]rune(name)) > 2*ThreadNameLimit {
		return ""
	}
	if runes := []rune(name); len(runes) > ThreadNameLimit {
		// Cut on a word boundary when one is within reach.
		cut := string(runes[:ThreadNameLimit])
		if space := strings.LastIndex(cut, " "); space > ThreadNameLimit/2 {
			cut = cut[:space]
		}
		name = strings.TrimRight(cut, " ,;:") + "…"
	}
	return name
}

// ModelResult is what came back, or why nothing did.
type ModelResult struct {
	Text    string   // the reply, or for a dry run the prompt that would be sent
	Options []string // a completion's continuations, best first (Text is the first)
	Refusal string   // why nothing was asked
	Note    string   // what was read, e.g. how many messages a summary covered
	// Endpoint and Model are what answered, or would have.
	Endpoint string
	Model    string
}

// Asked reports whether anything actually went out.
func (r ModelResult) Asked() bool { return r.Refusal == "" }

// MessagesAfter is everything said after one event, and everything when the event is
// not in the slice.
func MessagesAfter(messages []Message, after EventID) []Message {
	if after == "" {
		return messages
	}
	for i := range messages {
		if messages[i].ID == after {
			return messages[i+1:]
		}
	}
	return messages
}

// StoredDraft is a half-written message as it survives a restart.
type StoredDraft struct {
	RoomID   RoomID
	Body     string
	Caret    int // byte offset into Body; the caller clamps it
	Mentions []Mention
	// ReplyTo and Editing are what the draft is aimed at; EditSaved is what the
	// composer held before an edit borrowed it.
	ReplyTo   EventID
	Editing   EventID
	EditSaved string
	// ThreadRoot is the thread the draft is written into, empty for the main timeline.
	// A thread alone is not worth keeping: Empty ignores it.
	ThreadRoot EventID
	Author     string // empty for the person at the keyboard
	Updated    time.Time
}

// DraftAgent is the author recorded for a draft this client did not type.
const DraftAgent = "agent"

// Empty reports whether there is nothing worth keeping (which deletes a stored draft).
func (d StoredDraft) Empty() bool {
	return d.Body == "" && d.ReplyTo == "" && d.Editing == "" && d.EditSaved == ""
}

// SummarySpan is how much of a room a summary reads, when the person asking said.
type SummarySpan struct {
	Since time.Time // zero when not given
	Count int       // zero when not given
	Said  string    // the argument as typed
}

// Given reports whether anything was asked for.
func (s SummarySpan) Given() bool { return !s.Since.IsZero() || s.Count > 0 }

// ParseSummarySpan reads `/summary`'s optional argument.
func ParseSummarySpan(arg string, now time.Time) (SummarySpan, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return SummarySpan{}, nil
	}
	if count, err := strconv.Atoi(arg); err == nil {
		if count <= 0 {
			return SummarySpan{}, fmt.Errorf("%q is not a number of messages", arg)
		}
		return SummarySpan{Count: count, Said: arg}, nil
	}
	if since, ok := parseWhen(arg, now, false); ok {
		if since.After(now) {
			return SummarySpan{}, fmt.Errorf("%q is in the future", arg)
		}
		return SummarySpan{Since: since, Said: arg}, nil
	}
	return SummarySpan{}, fmt.Errorf(
		"%q is not a span — try 2h, 7d, yesterday, 2026-09-18, or a number of messages", arg)
}

// MessagesIn is the messages a named span covers: those at or after `since`, or the
// last `count` of them, or both.
func MessagesIn(messages []Message, since time.Time, count int) []Message {
	out := messages
	if !since.IsZero() {
		cut := len(out)
		for i := range out {
			if !out[i].Timestamp.Before(since) {
				cut = i
				break
			}
		}
		out = out[cut:]
	}
	if count > 0 && len(out) > count {
		out = out[len(out)-count:]
	}
	return out
}

// AllowSend decides whether an assistant may post to a room unreviewed, or whether its
// words go into that room's composer as a draft instead.
func AllowSend(send []string, room RoomFacts) bool {
	return namesAny(nonEmpty(send), room)
}
