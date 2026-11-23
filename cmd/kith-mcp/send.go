package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
)

// draftCap stops a never-sending loop from growing a draft forever.
const draftCap = 8000

// writeArgs is only what is said and where, never what happens.
type writeArgs struct {
	Room    string `json:"room"`
	Text    string `json:"text"`
	Thread  string `json:"thread"`
	ReplyTo string `json:"reply_to"`
	// At sends it then rather than now: RFC 3339 with offset.
	At string `json:"at"`
	// Urgent sends it outside the room's working hours too.
	Urgent bool `json:"urgent"`
}

// errLaterOnlyWhereSent refuses a delayed send to a draft-only room: a draft is
// reviewed before it goes, a scheduled message is not.
var errLaterOnlyWhereSent = errors.New("a message is sent later only where you may send; " +
	"this room is drafted into, so nothing was scheduled. Write it now as a draft, or tell them")

// aim is where in a room a message goes: a thread root and a message it answers,
// either empty; mentions are the people it names (mentionsIn).
type aim struct {
	thread, replyTo domain.EventID
	mentions        []domain.Mention
}

// errElsewhere refuses to add to a draft answering another message: the words would
// answer what they were not meant to.
var errElsewhere = errors.New("that composer already holds a draft answering another message, so " +
	"nothing was added to it. Tell them; it can go in once that draft is sent or cleared")

// shortDashes turns every dash longer than a hyphen into a hyphen.
var shortDashes = strings.NewReplacer("\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-")

// sendMessage is the send_message tool.
func (s *server) sendMessage(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[writeArgs](raw)
	if err != nil {
		return nil, err
	}
	text, later, err := in.checked(time.Now())
	if err != nil {
		return nil, err
	}
	if s.ledger == nil {
		return nil, errors.New("this server cannot write: no ledger, so nothing it did could be recorded")
	}
	readable, _, err := s.readableRooms(ctx)
	if err != nil {
		return nil, err
	}
	room, err := roomNamed(readable, in.Room)
	if err != nil {
		if errors.Is(err, errNoSuchRoom) {
			s.recordUnseenRoom(ctx, in.Room, text)
			if !s.readScope().Shares() {
				// The same whatever room was named, so it reveals nothing.
				return nil, fmt.Errorf("nothing was written: %w", errNothingShared)
			}
		}
		return nil, err
	}
	// Read scope, then write scope; `send` cannot widen either.
	if refused := s.writable(ctx, room); refused != nil {
		return nil, s.refuse(room, text, refused)
	}
	target, err := s.aimIn(ctx, room, in.Thread, in.ReplyTo)
	if err != nil {
		return nil, err
	}
	target.mentions = s.mentionsIn(ctx, room.ID, text)
	// No other session decides between the cooldown read and the send's record.
	release, err := s.ledger.Exclusive(ctx)
	if err != nil {
		return nil, fmt.Errorf("refusing to send what cannot be recorded: %w", err)
	}
	defer release()
	decision, err := s.howToWrite(ctx, room, in.Urgent)
	if err != nil {
		return nil, err
	}
	return s.writeBy(ctx, decision, room, text, target, later)
}

// writeBy sends, schedules or drafts the words as decided; asked is the time asked for
// (zero: now).
func (s *server) writeBy(ctx context.Context, decision writeChoice, room domain.Room, text string, target aim, asked time.Time) (any, error) {
	waiting := decision.waiting(time.Now())
	later, why := decision.within(asked)
	switch {
	case !later.IsZero() && !decision.send:
		return nil, s.refuse(room, text, fmt.Errorf("%w (%s)", errLaterOnlyWhereSent, decision.reason))
	case !later.IsZero() && waiting >= s.queueLimit:
		full := fmt.Sprintf("%d messages already wait to go out to this room and [agent.write] queue_limit is %d",
			waiting, s.queueLimit)
		if !asked.IsZero() {
			return nil, s.refuse(room, text, errors.New(full+", so nothing was scheduled. Tell them, or try again once some have gone out"))
		}
		return s.draftFor(ctx, room, text, full+", so this one waits in the composer", target)
	case !later.IsZero():
		return s.sendLater(ctx, room, text, target, later, why)
	case decision.send:
		return s.sendNow(ctx, room, text, target)
	}
	return s.draftFor(ctx, room, text, decision.reason, target)
}

// checked returns the text with long dashes made hyphens, and when it goes (zero: now).
func (in writeArgs) checked(now time.Time) (string, time.Time, error) {
	text := shortDashes.Replace(strings.TrimSpace(in.Text))
	if text == "" {
		return "", time.Time{}, errors.New("send_message needs something to say")
	}
	later, err := sendTime(in.At, now)
	return text, later, err
}

// sendTime is when a message goes: zero for now, else the given time, which must be
// ahead of now.
func sendTime(at string, now time.Time) (time.Time, error) {
	if at = strings.TrimSpace(at); at == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return time.Time{}, fmt.Errorf("at %q is not an RFC 3339 time with its offset, such as 2026-10-12T09:00:00+03:00", at)
	}
	if !t.After(now) {
		return time.Time{}, fmt.Errorf("at %s has passed; leave it out to send now", at)
	}
	return t.UTC(), nil
}

// sendLater hands the message to the daemon's queue for its time; why, when set, says
// why then.
func (s *server) sendLater(ctx context.Context, room domain.Room, text string, target aim, at time.Time, why string) (any, error) {
	if _, err := s.backend.Schedule(ctx, domain.ScheduledMessage{
		RoomID: room.ID, Body: text, Mentions: target.mentions, TxnID: domain.NewTxnID(), At: at,
		Written: time.Now().UTC(), ThreadRoot: target.thread, ReplyTo: target.replyTo,
	}); err != nil {
		return nil, fmt.Errorf("scheduling it: %w", err)
	}
	return s.wrote(room, agent.Scheduled, text, at, why+"it goes out at "+at.Local().Format(time.RFC3339)+"; the daemon sends it, and the client lists it under :scheduled until then")
}

// aimIn resolves thread and reply targets within this room's cached history (already
// in read scope), so another room's event is simply not found. A thread named by a
// reply is its root; a reply to a threaded message goes in that thread.
func (s *server) aimIn(ctx context.Context, room domain.Room, thread, replyTo string) (aim, error) {
	var out aim
	if replyTo = strings.TrimSpace(replyTo); replyTo != "" {
		msg, err := s.messageIn(ctx, room, replyTo)
		if err != nil {
			return aim{}, err
		}
		out.replyTo, out.thread = msg.ID, msg.ThreadRoot
	}
	if thread = strings.TrimSpace(thread); thread != "" {
		msg, err := s.messageIn(ctx, room, thread)
		if err != nil {
			return aim{}, err
		}
		root := cmp.Or(msg.ThreadRoot, msg.ID)
		// The root itself is answered in its thread too.
		if out.replyTo != "" && out.thread != root && out.replyTo != root {
			return aim{}, fmt.Errorf("%s is not in that thread; pass the thread's root, or only reply_to", replyTo)
		}
		out.thread = root
	}
	return out, nil
}

// mentionsIn are the room's people text names as @name, by any name they go by here
// (kithd's, as the room shows it, or the network's), the longest name at each @. The
// mention's name keeps its @, so the text goes out as written and each network turns
// it into its own mention. A failed read sends the text as it is.
func (s *server) mentionsIn(ctx context.Context, room domain.RoomID, text string) []domain.Mention {
	if !strings.Contains(text, "@") {
		return nil
	}
	members, err := s.backend.MentionCandidates(ctx, room, 0)
	if err != nil {
		s.log.Warn("reading the room's members failed; @names go out as text", "room", room, "err", err)
		return nil
	}
	var out []domain.Mention
	for at := strings.IndexByte(text, '@'); at >= 0; {
		best, who := "", ""
		if at == 0 || !wordRune(text[:at], true) {
			for _, m := range members {
				for _, name := range []string{m.ShownAs, m.KnownAs, m.DisplayName} {
					rest, ok := strings.CutPrefix(text[at+1:], name)
					if ok && name != "" && len(name) > len(best) && (rest == "" || !wordRune(rest, false)) {
						best, who = name, m.UserID
					}
				}
			}
		}
		if best != "" {
			out = append(out, domain.Mention{UserID: who, Name: "@" + best})
		}
		next := strings.IndexByte(text[at+1:], '@')
		if next < 0 {
			break
		}
		at += 1 + next
	}
	return out
}

// wordRune reports whether the last (or first) rune of s is a letter or digit: a name
// that runs on into more of a word is not the name.
func wordRune(s string, last bool) bool {
	r, _ := utf8.DecodeRuneInString(s)
	if last {
		r, _ = utf8.DecodeLastRuneInString(s)
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// messageIn is the cached message with this event ID in room.
func (s *server) messageIn(ctx context.Context, room domain.Room, event string) (domain.Message, error) {
	eventID := domain.TypedEventID(room.ID, event)
	msgs, err := s.backend.MessagesAround(ctx, room.ID, eventID, 0, 0)
	if err != nil {
		return domain.Message{}, fmt.Errorf("reading the message: %w", err)
	}
	for i := range msgs {
		if msgs[i].ID == eventID {
			return msgs[i], nil
		}
	}
	return domain.Message{}, fmt.Errorf("%s is not a message in this room's cached history", event)
}

// writeChoice is the answer to "send or draft, and why", and for a send, the working
// hours it waits for (nil: any time) and when messages go out to the room, earliest
// first, which it keeps a cooldown from.
type writeChoice struct {
	send     bool
	reason   string
	hours    *domain.WorkingHours
	outs     []time.Time
	cooldown time.Duration
}

// waiting is how many messages to the room have yet to go out.
func (c writeChoice) waiting(now time.Time) int {
	return len(c.outs) - sort.Search(len(c.outs), func(i int) bool { return c.outs[i].After(now) })
}

// howToWrite reads the policy, the working hours unless urgent, then when messages go
// out to the room. An unwritable ledger refuses the send: the cooldown is counted from it.
func (s *server) howToWrite(ctx context.Context, room domain.Room, urgent bool) (writeChoice, error) {
	facts, placeErr := s.factsOf(ctx, room)
	if placeErr != nil && (domain.NamesAPlace(s.send) || !urgent && s.hoursNameAPlace()) {
		return writeChoice{reason: "which spaces or tags this room is in could not be read, so the words wait in its composer"}, nil
	}
	if !domain.AllowSend(s.send, facts) {
		return writeChoice{reason: "this room is not one [agent.write] send names, so the words wait in its composer"}, nil
	}
	if err := s.ledger.Writable(); err != nil {
		return writeChoice{}, fmt.Errorf("refusing to send what cannot be recorded: %w", err)
	}
	var hours *domain.WorkingHours
	if h, ok := domain.HoursFor(s.hours, facts); ok && !urgent {
		hours = &h
	}
	entries, err := s.ledger.Entries()
	if err != nil {
		return writeChoice{}, fmt.Errorf("reading the ledger the cooldown is counted from: %w", err)
	}
	return writeChoice{send: true, hours: hours, outs: agent.Outs(entries, room.ID), cooldown: s.cooldown}, nil
}

// within moves a send at later (zero: now) to the first time a cooldown away from every
// other message to the room and inside its working hours, and says why; anything else
// stays as it is.
func (c writeChoice) within(later time.Time) (time.Time, string) {
	if !c.send {
		return later, ""
	}
	now := time.Now()
	at := cmp.Or(later, now)
	var rest, closed string
	for moved := true; moved; {
		moved = false
		for _, out := range c.outs {
			if out.After(at.Add(-c.cooldown)) && out.Before(at.Add(c.cooldown)) {
				verb := "went"
				if out.After(now) {
					verb = "goes"
				}
				rest = fmt.Sprintf("a message %s out to this room at %s and [agent.write] cooldown is %s, so this one waits for the room to rest; ",
					verb, out.Local().Format(time.RFC3339), human.Duration(c.cooldown))
				at, moved = out.Add(c.cooldown), true
			}
		}
		if c.hours == nil {
			break
		}
		from := at.Local() // the hours are on this machine's clock
		if opens := c.hours.Next(from); opens.After(from) {
			closed = "it is outside this room's working hours ([agent.write] hours), so it waits for them to open; "
			at, moved = opens, true
		}
	}
	if rest == "" && closed == "" {
		return later, ""
	}
	return at.UTC(), rest + closed
}

// hoursNameAPlace reports whether which working hours apply rests on a room's spaces
// or tags.
func (s *server) hoursNameAPlace() bool {
	return slices.ContainsFunc(s.hours, func(h domain.WorkingHours) bool { return domain.NamesAPlace([]string{h.Match}) })
}

// sendNow posts the message. A failure goes to the daemon's queue under the same txn ID
// (no duplicate); a draft is the last resort.
func (s *server) sendNow(ctx context.Context, room domain.Room, text string, target aim) (any, error) {
	draft := domain.Draft{Body: text, Mentions: target.mentions, TxnID: domain.NewTxnID(),
		ThreadRoot: target.thread, ReplyTo: target.replyTo}
	sendErr := s.backend.Send(ctx, room.ID, draft)
	if sendErr == nil {
		return s.wrote(room, agent.Sent, text, time.Time{}, "")
	}
	// The daemon's error stays in the log; the assistant gets what happened, not internals.
	s.log.Warn("send failed; queueing", "room", room.ID, "err", sendErr)
	now := time.Now().UTC()
	_, queueErr := s.backend.Schedule(ctx, domain.ScheduledMessage{
		RoomID: room.ID, Body: text, Mentions: target.mentions, TxnID: draft.TxnID, At: now,
		Written: now, ThreadRoot: target.thread, ReplyTo: target.replyTo,
	})
	if queueErr == nil {
		return s.wrote(room, agent.Queued, text, time.Time{}, "the homeserver would not take it yet; the daemon is retrying it")
	}
	s.log.Warn("queueing failed; drafting", "room", room.ID, "err", queueErr)
	return s.draftFor(ctx, room, text, "the send failed and the queue would not take it either", target)
}

// draftAttempts bounds rebuilds when someone wrote the draft between read and write;
// draftBackoff pauses before the second (doubled before the third) so a typist can
// pause.
const (
	draftAttempts = 3
	draftBackoff  = 100 * time.Millisecond
)

// Drafts refused on their merits, recorded as refusals (an unreachable daemon is not).
var (
	errMidEdit = errors.New("that room's composer is mid-edit, and appending to an edit would change " +
		"a message they are already correcting. Tell them, and try again when they have finished")
	errDraftFull = errors.New("that room's draft is already long; it wants a person before it " +
		"wants more words")
	errDraftChanging = errors.New("that room's draft kept changing while this was being added, so " +
		"none of it was touched and nothing of theirs was lost. Try again in a moment")
)

// draftFor adds the words to a room's composer, never removing any. The append is
// written only over the draft it was built on, so words saved in between (the TUI's
// debounced save) are kept and it is rebuilt on them.
func (s *server) draftFor(ctx context.Context, room domain.Room, text, why string, target aim) (any, error) {
	pause := draftBackoff
	for attempt := range draftAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("writing the draft: %w", ctx.Err())
			case <-time.After(pause):
			}
			pause *= 2
		}
		appended, saved, err := s.appendDraft(ctx, room, text, target)
		if errors.Is(err, errMidEdit) || errors.Is(err, errDraftFull) || errors.Is(err, errElsewhere) {
			return nil, s.refuse(room, text, err)
		}
		if err != nil {
			return nil, err
		}
		if !saved {
			continue
		}
		if appended {
			why = strings.TrimSpace(why + " — appended after what was already written there")
		}
		return s.wrote(room, agent.Drafted, text, time.Time{}, why)
	}
	return nil, s.refuse(room, text, errDraftChanging)
}

// appendDraft adds text to the draft of the room's main timeline or of the thread aimed
// at, each its own, once, over the draft it read; saved is false when the draft changed
// in between.
func (s *server) appendDraft(ctx context.Context, room domain.Room, text string, target aim) (appended, saved bool, err error) {
	held, err := s.draftIn(ctx, room.ID, target.thread)
	if err != nil {
		return false, false, err
	}
	over := held
	// Appending to an edit in progress would alter the message being corrected.
	if held.Editing != "" || held.EditSaved != "" {
		return false, false, errMidEdit
	}
	// An empty composer takes these words' reply target; a held draft keeps its own and
	// takes them only when it matches.
	switch {
	case held.Empty():
		held.ReplyTo = target.replyTo
	case target.replyTo != "" && held.ReplyTo != target.replyTo:
		return false, false, errElsewhere
	}
	if n := len([]rune(held.Body)) + len([]rune(text)); n > draftCap {
		return false, false, fmt.Errorf("%w (%d characters with this addition)", errDraftFull, n)
	}
	appended = held.Body != ""
	body := text
	if appended {
		// Their words first, a blank line between.
		body = held.Body + "\n\n" + text
	}
	held.RoomID, held.Body = room.ID, body
	held.Mentions = slices.Concat(held.Mentions, target.mentions)
	held.Author, held.Updated = s.whoever(), time.Now()
	saved, err = s.backend.ReplaceDraft(ctx, held, over)
	if err != nil {
		// The write may have landed with its answer lost; retrying would append twice. Stored
		// as written, or with more after it, means it landed.
		if now, rerr := s.draftIn(ctx, room.ID, target.thread); rerr == nil && landed(now, held) {
			return appended, true, nil
		}
		return false, false, fmt.Errorf("writing the draft: %w", err)
	}
	return appended, saved, nil
}

// landed reports whether the stored draft is this write, possibly with more typed
// after it.
func landed(stored, wrote domain.StoredDraft) bool {
	if stored.Body == wrote.Body {
		return stored.Updated.UnixMilli() == wrote.Updated.UnixMilli() && stored.Author == wrote.Author
	}
	rest, ok := strings.CutPrefix(stored.Body, wrote.Body)
	return ok && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\n"))
}

// draftIn is the current draft of a room's thread (its main timeline for none), or the
// empty one.
func (s *server) draftIn(ctx context.Context, room domain.RoomID, thread domain.EventID) (domain.StoredDraft, error) {
	drafts, err := s.backend.Drafts(ctx)
	if err != nil {
		return domain.StoredDraft{}, fmt.Errorf("reading what is already in the composer: %w", err)
	}
	for i := range drafts {
		if drafts[i].RoomID == room && drafts[i].ThreadRoot == thread {
			return drafts[i], nil
		}
	}
	return domain.StoredDraft{RoomID: room, ThreadRoot: thread}, nil
}

// wrote records what happened, then says it; due is when a scheduled message goes out.
func (s *server) wrote(room domain.Room, outcome, text string, due time.Time, why string) (any, error) {
	out := map[string]any{
		"action": outcome,
		"room":   viewOf(&room),
	}
	if why != "" {
		out["reason"] = why
	}
	if !due.IsZero() {
		out["at"] = due.Local().Format(time.RFC3339)
	}
	if outcome == agent.Drafted {
		out["note"] = "it is in that room's composer, marked as drafted by " + s.whoever() +
			", and shows in the client's Drafts group. A person sends it, or does not."
	}
	if err := s.ledger.Append(agent.Entry{
		Room: room.ID, Name: room.DisplayName(), Author: s.whoever(),
		Outcome: outcome, Reason: why, Text: text, Due: due,
	}); err != nil {
		s.log.Error("agent ledger append failed", "room", room.ID, "outcome", outcome, "err", err)
		out["ledger_error"] = err.Error() + " — tell them: what you write is supposed to be recorded"
	}
	return out, nil
}

// refuse records a write the scope did not allow and returns the refusal to say.
func (s *server) refuse(room domain.Room, text string, why error) error {
	if err := s.ledger.Append(agent.Entry{
		Room: room.ID, Name: room.DisplayName(), Author: s.whoever(),
		Outcome: agent.Refused, Reason: why.Error(), Text: text,
	}); err != nil {
		s.log.Error("agent ledger append failed", "room", room.ID, "outcome", agent.Refused, "err", err)
		return fmt.Errorf("nothing was written: %w (and recording the refusal failed: %w)", why, err)
	}
	return fmt.Errorf("nothing was written: %w", why)
}

// recordUnseenRoom ledgers a write aimed at a room the assistant cannot see: under the
// room if it exists outside `[agent.read]`, else under the name asked. Both append, so
// timing cannot tell a withheld room from a missing one.
func (s *server) recordUnseenRoom(ctx context.Context, want, text string) {
	entry := agent.Entry{
		Name: want, Author: s.whoever(), Outcome: agent.Refused, Text: text,
		Reason: "no room by that name is visible to the assistant",
	}
	if rooms, err := s.rooms(ctx); err == nil {
		if room, nerr := roomNamed(rooms, want); nerr == nil {
			if why := s.allowed(ctx, room); why != nil {
				entry.Room, entry.Name, entry.Reason = room.ID, room.DisplayName(), why.Error()
			}
		}
	}
	if err := s.ledger.Append(entry); err != nil {
		s.log.Error("agent ledger append failed", "room", entry.Room, "outcome", agent.Refused, "err", err)
	}
}

// whoever is the name a write is recorded under: the MCP client's own, or "agent".
func (s *server) whoever() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == "" {
		return domain.DraftAgent
	}
	return s.client
}

// nameClient records the client's self-reported name, sanitized to one bounded line.
func (s *server) nameClient(name, version string) {
	clean := strings.Join(strings.Fields(strings.Map(printable, name)), " ")
	if clean == "" {
		return
	}
	if version = strings.Join(strings.Fields(strings.Map(printable, version)), " "); version != "" {
		clean += " " + version
	}
	if len([]rune(clean)) > clientNameCap {
		clean = string([]rune(clean)[:clientNameCap])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = clean
}

const clientNameCap = 40

// printable maps control characters to spaces, so a name cannot inject lines.
func printable(r rune) rune {
	if r < ' ' || r == 0x7f {
		return ' '
	}
	return r
}
