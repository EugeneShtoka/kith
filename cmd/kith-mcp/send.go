package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// draftCap stops a never-sending loop from growing a draft forever.
const draftCap = 8000

// writeArgs is deliberately only what is said, never what happens.
type writeArgs struct {
	Room string `json:"room"`
	Text string `json:"text"`
}

// sendMessage is the tool.
func (s *server) sendMessage(ctx context.Context, raw json.RawMessage) (any, error) {
	in, err := args[writeArgs](raw)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, errors.New("send_message needs something to say")
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
				// Says the same whatever room was named, so it reveals nothing.
				return nil, fmt.Errorf("nothing was written: %w", errNothingShared)
			}
		}
		return nil, err
	}
	// Read scope, then write scope; `send` cannot widen either.
	if refused := s.writable(ctx, room); refused != nil {
		return nil, s.refuse(room, text, refused)
	}
	// From the cooldown read to the send's record, no other session decides.
	release, err := s.ledger.Exclusive(ctx)
	if err != nil {
		return nil, fmt.Errorf("refusing to send what cannot be recorded: %w", err)
	}
	defer release()
	decision, err := s.howToWrite(ctx, room)
	if err != nil {
		return nil, err
	}
	if decision.send {
		return s.sendNow(ctx, room, text)
	}
	return s.draftFor(ctx, room, text, decision.reason)
}

// writeChoice is the answer to "send or draft, and why".
type writeChoice struct {
	send   bool
	reason string
}

// howToWrite reads the policy and the rail, in that order.
// An unwritable ledger refuses the send: the cooldown is counted from it.
func (s *server) howToWrite(ctx context.Context, room domain.Room) (writeChoice, error) {
	facts, placeErr := s.factsOf(ctx, room)
	if placeErr != nil && domain.NamesAPlace(s.send) {
		return writeChoice{reason: "which spaces this room is in could not be read, so the words wait in its composer"}, nil
	}
	if !domain.AllowSend(s.send, facts) {
		return writeChoice{reason: "this room is not one [agent.write] send names, so the words wait in its composer"}, nil
	}
	if err := s.ledger.Writable(); err != nil {
		return writeChoice{}, fmt.Errorf("refusing to send what cannot be recorded: %w", err)
	}
	if s.cooldown <= 0 {
		return writeChoice{send: true}, nil
	}
	entries, err := s.ledger.Entries()
	if err != nil {
		return writeChoice{}, fmt.Errorf("reading the ledger the cooldown is counted from: %w", err)
	}
	last, ever := agent.LastOut(entries, room.ID)
	if rest := time.Since(last); ever && rest < s.cooldown {
		return writeChoice{reason: fmt.Sprintf(
			"a message went to this room %s ago and [agent.write] cooldown is %s, so this one waits in the composer",
			round(rest), s.cooldown)}, nil
	}
	return writeChoice{send: true}, nil
}

// sendNow posts the message, and keeps it when the homeserver will not take it.
// A failure goes to the daemon's queue under the same txn ID (so it cannot duplicate);
// a draft is the last resort.
func (s *server) sendNow(ctx context.Context, room domain.Room, text string) (any, error) {
	draft := domain.Draft{Body: text, TxnID: domain.NewTxnID()}
	sendErr := s.backend.Send(ctx, room.ID, draft)
	if sendErr == nil {
		return s.wrote(room, agent.Sent, text, "")
	}
	// The daemon's error text stays in the log: the reason is for the assistant, and
	// names what happened, not the internals it happened in.
	s.logger().Warn("send failed; queueing", "room", room.ID, "err", sendErr)
	now := time.Now().UTC()
	_, queueErr := s.backend.Schedule(ctx, domain.ScheduledMessage{
		RoomID: room.ID, Body: text, TxnID: draft.TxnID, At: now, Written: now,
	})
	if queueErr == nil {
		return s.wrote(room, agent.Queued, text, "the homeserver would not take it yet; the daemon is retrying it")
	}
	s.logger().Warn("queueing failed; drafting", "room", room.ID, "err", queueErr)
	return s.draftFor(ctx, room, text, "the send failed and the queue would not take it either")
}

// draftAttempts bounds how often an append is rebuilt because someone wrote the
// draft between its read and its write; draftBackoff is the pause before the second
// (doubled before the third), so a person typing gets a moment to pause.
const (
	draftAttempts = 3
	draftBackoff  = 100 * time.Millisecond
)

// The drafts refused on their merits, recorded as refusals (a failure to reach the
// daemon is not one).
var (
	errMidEdit = errors.New("that room's composer is mid-edit, and appending to an edit would change " +
		"a message they are already correcting. Tell them, and try again when they have finished")
	errDraftFull = errors.New("that room's draft is already long; it wants a person before it " +
		"wants more words")
	errDraftChanging = errors.New("that room's draft kept changing while this was being added, so " +
		"none of it was touched and nothing of theirs was lost. Try again in a moment")
)

// draftFor puts the words in a room's composer, without ever taking any out. The
// append is written only over the draft it was built on, so words someone saves in
// between (the TUI's debounced save) are never dropped: it is rebuilt on theirs.
func (s *server) draftFor(ctx context.Context, room domain.Room, text, why string) (any, error) {
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
		appended, saved, err := s.appendDraft(ctx, room, text)
		if errors.Is(err, errMidEdit) || errors.Is(err, errDraftFull) {
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
		return s.wrote(room, agent.Drafted, text, why)
	}
	return nil, s.refuse(room, text, errDraftChanging)
}

// appendDraft adds text to the room's draft once, over the draft it read. saved is
// false when the draft changed in between.
func (s *server) appendDraft(ctx context.Context, room domain.Room, text string) (appended, saved bool, err error) {
	held, err := s.draftIn(ctx, room.ID)
	if err != nil {
		return false, false, err
	}
	over := held
	// Appending to an edit in progress would alter a message they are correcting.
	if held.Editing != "" || held.EditSaved != "" {
		return false, false, errMidEdit
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
	held.Author, held.Updated = s.whoever(), time.Now()
	saved, err = s.backend.ReplaceDraft(ctx, held, over)
	if err != nil {
		// The write may have landed with its answer lost: trying again would append
		// the same words twice. Stored as written, or with more after it, it landed.
		if now, rerr := s.draftIn(ctx, room.ID); rerr == nil && landed(now, held) {
			return appended, true, nil
		}
		return false, false, fmt.Errorf("writing the draft: %w", err)
	}
	return appended, saved, nil
}

// landed reports whether the stored draft is this write, or this write with more after
// it (the person went on typing): either way, it was stored.
func landed(stored, wrote domain.StoredDraft) bool {
	if stored.Body == wrote.Body {
		return stored.Updated.UnixMilli() == wrote.Updated.UnixMilli() && stored.Author == wrote.Author
	}
	rest, ok := strings.CutPrefix(stored.Body, wrote.Body)
	return ok && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\n"))
}

// draftIn is a room's current draft, or the empty one.
func (s *server) draftIn(ctx context.Context, room domain.RoomID) (domain.StoredDraft, error) {
	drafts, err := s.backend.Drafts(ctx)
	if err != nil {
		return domain.StoredDraft{}, fmt.Errorf("reading what is already in the composer: %w", err)
	}
	for i := range drafts {
		if drafts[i].RoomID == room {
			return drafts[i], nil
		}
	}
	return domain.StoredDraft{RoomID: room}, nil
}

// wrote records what happened and says it, in that order.
func (s *server) wrote(room domain.Room, outcome, text, why string) (any, error) {
	out := map[string]any{
		"action": outcome,
		"room":   viewOf(&room),
	}
	if why != "" {
		out["reason"] = why
	}
	if outcome == agent.Drafted {
		out["note"] = "it is in that room's composer, marked as drafted by " + s.whoever() +
			", and shows in the client's Drafts group. A person sends it, or does not."
	}
	if err := s.ledger.Append(agent.Entry{
		Room: room.ID, Name: room.DisplayName(), Author: s.whoever(),
		Outcome: outcome, Reason: why, Text: text,
	}); err != nil {
		s.logger().Error("agent ledger append failed", "room", room.ID, "outcome", outcome, "err", err)
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
		s.logger().Error("agent ledger append failed", "room", room.ID, "outcome", agent.Refused, "err", err)
		return fmt.Errorf("nothing was written: %w (and recording the refusal failed: %w)", why, err)
	}
	return fmt.Errorf("nothing was written: %w", why)
}

// recordUnseenRoom puts a write aimed at a room the assistant cannot see in the
// ledger, so the user sees the attempt: under the room, when the name is one outside
// `[agent.read]`, else under the name asked for. Both cases append, so the time the
// refusal takes does not tell a withheld room from a missing one; the assistant is
// told only that no such room is visible.
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
		s.logger().Error("agent ledger append failed", "room", entry.Room, "outcome", agent.Refused, "err", err)
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

// clientNameCap bounds the kept client name.
const clientNameCap = 40

// printable maps control characters to spaces, so a name cannot inject lines.
func printable(r rune) rune {
	if r < ' ' || r == 0x7f {
		return ' '
	}
	return r
}

// round is a duration said the way a sentence says one.
func round(d time.Duration) time.Duration {
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}
