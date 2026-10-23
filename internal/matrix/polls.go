package matrix

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Matrix polls follow MSC3381: an m.poll.start event asks a question; m.poll.response
// events relate to the start with the voter's chosen answer IDs; an m.poll.end from the
// start's sender closes voting. A voter's latest response wins; a response arriving
// after the end is dropped. The start's event ID is the poll's handle.

// pollMaxAnswers caps the answers one PollVoters walk reads; polls on Matrix are small
// and the client paginates past this if ever hit.
const pollMaxAnswers = 1000

// pollStartMessage builds the message carrying a poll from an m.poll.start event, with
// an empty Poll ready for responses to apply. ok is false for a start whose content did
// not parse.
func pollStartMessage(evt *event.Event) (domain.Message, bool) {
	content, ok := evt.Content.Parsed.(*event.PollStartEventContent)
	if !ok || content == nil {
		return domain.Message{}, false
	}
	return domain.Message{
		ID:        domain.EventID(evt.ID),
		RoomID:    domain.RoomID(evt.RoomID),
		Sender:    string(evt.Sender),
		Body:      "📊 " + content.PollStart.Question.GetText(),
		Timestamp: time.UnixMilli(evt.Timestamp),
		Poll:      pollFromStart(string(evt.ID), content.PollStart),
	}, true
}

// pollFromStart turns an m.poll.start into a Poll with no votes yet. id is the start
// event ID: a response or end names the start, so lookups key on it.
func pollFromStart(id string, start event.PollStart) *domain.Poll {
	p := &domain.Poll{
		ID:       id,
		Question: start.Question.GetText(),
		Multiple: start.MaxSelections > 1,
		Ballots:  map[string][]string{},
	}
	for _, a := range start.Answers {
		p.Options = append(p.Options, domain.PollOption{ID: a.ID, Text: a.GetText()})
	}
	return p
}

// pollMaxSelections is how many answers a voter may pick, from the start that produced
// the Poll (1 for single-choice, N for multiple).
func pollMaxSelections(p *domain.Poll) int {
	if p.Multiple {
		return len(p.Options)
	}
	return 1
}

// pollValidAnswers keeps only answers that name options the poll offers, in the order
// given, up to max; MSC3381 §Validity drops the rest. An empty return retracts the vote.
func pollValidAnswers(p *domain.Poll, answers []string, max int) []string {
	if max <= 0 {
		return nil
	}
	known := make(map[string]bool, len(p.Options))
	for _, o := range p.Options {
		known[o.ID] = true
	}
	seen := make(map[string]bool, max)
	kept := make([]string, 0, max)
	for _, a := range answers {
		if !known[a] || seen[a] {
			continue
		}
		seen[a] = true
		kept = append(kept, a)
		if len(kept) >= max {
			break
		}
	}
	return kept
}

// applyResponse records a voter's chosen answers in the poll's ballots at a round:
// a later round replaces an earlier one, an earlier one is dropped. The poll is
// mutated and re-tallied. Empty answers retracts the vote.
func applyResponse(p *domain.Poll, voter string, answers []string, round uint32, selves func(string) bool) {
	if p.Ballots == nil {
		p.Ballots = map[string][]string{}
	}
	if p.Rounds == nil {
		p.Rounds = map[string]uint32{}
	}
	if round > 0 && round < p.Rounds[voter] {
		return // a response the cache has already superseded
	}
	kept := pollValidAnswers(p, answers, pollMaxSelections(p))
	if len(kept) == 0 {
		delete(p.Ballots, voter)
	} else {
		p.Ballots[voter] = kept
	}
	if round > 0 {
		p.Rounds[voter] = round
	}
	p.Retally(selves)
}

// pollRound is the ordering stamp of a Matrix poll event: server-ordered seconds
// truncated into Rounds's uint32. 1 is the floor for a non-zero stamp so the "0 means
// no stamp" convention of applyResponse holds across every event we feed it.
func pollRound(tsMS int64) uint32 {
	if tsMS <= 0 {
		return 1
	}
	s := tsMS / 1000
	if s > int64(^uint32(0)) {
		return ^uint32(0)
	}
	if s < 1 {
		return 1
	}
	return uint32(s)
}

// pollResponseTarget is the start a response or end names; "" when it names nothing.
func pollResponseTarget(evt *event.Event) domain.EventID {
	rel, ok := relatesTo(evt)
	if !ok || rel == nil {
		return ""
	}
	return domain.EventID(rel.EventID)
}

// relatesTo reads an event's m.relates_to, whatever its content type.
func relatesTo(evt *event.Event) (*event.RelatesTo, bool) {
	switch c := evt.Content.Parsed.(type) {
	case *event.PollResponseEventContent:
		return &c.RelatesTo, true
	case *event.MessageEventContent: // poll.end carries no decoded type here
		if c.RelatesTo != nil {
			return c.RelatesTo, true
		}
	}
	// Fall back to the raw content: m.poll.end has no registered Go type.
	if evt.Content.Raw == nil {
		return nil, false
	}
	raw, ok := evt.Content.Raw["m.relates_to"].(map[string]any)
	if !ok {
		return nil, false
	}
	eid, _ := raw["event_id"].(string)
	t, _ := raw["rel_type"].(string)
	return &event.RelatesTo{Type: event.RelationType(t), EventID: id.EventID(eid)}, eid != ""
}

// pollResponseAnswers is the answer IDs a response names, or nil if parsing failed or
// no answers were chosen (the voter retracted their vote).
func pollResponseAnswers(evt *event.Event) []string {
	content, ok := evt.Content.Parsed.(*event.PollResponseEventContent)
	if !ok || content == nil {
		return nil
	}
	return content.Response.Answers
}

// pollBackfill tracks poll starts whose responses are being fetched from /relations:
// one fetch per start per daemon run, deduped so a flurry of sync responses does not
// walk the same relations several times. writes orders poll read-modify-writes so a
// backfill cannot overwrite a live response that landed between its read and its
// write.
type pollBackfill struct {
	mu     sync.Mutex
	done   map[domain.EventID]bool // starts whose /relations fetch has started
	wg     sync.WaitGroup
	writes sync.Mutex // held across read-modify-write of a cached poll
}

// claim returns true the first time startID is seen; subsequent callers skip the fetch.
func (b *pollBackfill) claim(startID domain.EventID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done == nil {
		b.done = map[domain.EventID]bool{}
	}
	if b.done[startID] {
		return false
	}
	b.done[startID] = true
	return true
}

// forget drops a start from the done set, used by tests to re-fetch.
func (b *pollBackfill) forget(startID domain.EventID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.done, startID)
}

// onPollStart caches a live m.poll.start, emits its message, and backfills responses.
func (b *InProc) onPollStart(ctx context.Context, evt *event.Event) {
	msg, ok := pollStartMessage(evt)
	if !ok {
		return
	}
	msg.Mentioned = mentionsMe(evt, b.client.UserID)
	msg.SenderName = b.senderName(ctx, msg.RoomID, msg.Sender)
	if b.cache != nil {
		b.cacheMessages(ctx, msg.RoomID, []domain.Message{msg})
		if b.onCached != nil {
			b.onCached(msg)
		}
		b.settleReceipts(ctx, msg.RoomID)
		b.recount(ctx, msg.RoomID)
	}
	emit(&b.out, b.out.msgs, msg)
	b.scheduleBackfill(ctx, msg.RoomID, msg.ID)
}

// onPollResponse applies a live m.poll.response to a cached poll and emits a change.
// An unknown start is dropped: it will be caught when the start is scrolled into and
// its /relations fetched.
func (b *InProc) onPollResponse(ctx context.Context, evt *event.Event) {
	target := pollResponseTarget(evt)
	if target == "" || b.cache == nil {
		return
	}
	b.pollBackfill.writes.Lock()
	defer b.pollBackfill.writes.Unlock()
	msg, ok, err := b.cache.MessageByID(ctx, domain.RoomID(evt.RoomID), target)
	b.warnIf(ctx, err, "read a poll for a response", "room", evt.RoomID, "event", target)
	if !ok || msg.Poll == nil || msg.Poll.Closed {
		return
	}
	poll := clonePoll(msg.Poll)
	applyResponse(&poll, string(evt.Sender), pollResponseAnswers(evt), pollRound(evt.Timestamp), b.isSelf)
	b.warnIf(ctx, b.cache.SetPoll(ctx, msg.RoomID, msg.ID, poll), "cache a poll response", "room", msg.RoomID, "event", msg.ID)
	b.changed(msg.RoomID)
}

// onPollEnd closes a cached poll when its start's sender sends the end.
func (b *InProc) onPollEnd(ctx context.Context, evt *event.Event) {
	target := pollResponseTarget(evt)
	if target == "" || b.cache == nil {
		return
	}
	b.pollBackfill.writes.Lock()
	defer b.pollBackfill.writes.Unlock()
	msg, ok, err := b.cache.MessageByID(ctx, domain.RoomID(evt.RoomID), target)
	b.warnIf(ctx, err, "read a poll for an end", "room", evt.RoomID, "event", target)
	if !ok || msg.Poll == nil || msg.Poll.Closed {
		return
	}
	if string(evt.Sender) != msg.Sender {
		return // only the start's sender ends it
	}
	poll := clonePoll(msg.Poll)
	poll.Closed = true
	b.warnIf(ctx, b.cache.SetPoll(ctx, msg.RoomID, msg.ID, poll), "close a poll", "room", msg.RoomID, "event", msg.ID)
	b.changed(msg.RoomID)
}

// clonePoll deep-copies the Poll fields a write-side mutates: Options, Ballots and
// Rounds. The caller still gets its own value by passing *p rather than p.
func clonePoll(src *domain.Poll) domain.Poll {
	out := *src
	out.Options = slices.Clone(src.Options)
	out.Ballots = cloneBallots(src.Ballots)
	out.Rounds = cloneRounds(src.Rounds)
	return out
}

// cloneBallots copies the map so a mutation of the write-side does not reach a reader
// holding the loaded copy.
func cloneBallots(src map[string][]string) map[string][]string {
	if src == nil {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(src))
	for k, v := range src {
		out[k] = slices.Clone(v)
	}
	return out
}

// cloneRounds copies the per-voter round stamps for a write-side.
func cloneRounds(src map[string]uint32) map[string]uint32 {
	if src == nil {
		return map[string]uint32{}
	}
	out := make(map[string]uint32, len(src))
	maps.Copy(out, src)
	return out
}

// scheduleBackfill fetches a poll's /relations once, applying every response in order
// and the end if any. It is a no-op after the first call for a start.
func (b *InProc) scheduleBackfill(ctx context.Context, roomID domain.RoomID, startID domain.EventID) {
	if b.cache == nil || b.client == nil || !b.pollBackfill.claim(startID) {
		return
	}
	b.pollBackfill.wg.Go(func() {
		if err := b.backfillPollRelations(ctx, roomID, startID); err != nil {
			b.warnIf(ctx, err, "backfill poll relations", "room", roomID, "event", startID)
		}
	})
}

// backfillPollRelations walks /relations for a poll start and applies the resulting
// responses and end to the cached poll. The fetch runs without the write lock; the
// apply takes it so a live response arriving mid-fetch is not overwritten (the live
// write replays into the merged state on the re-read).
func (b *InProc) backfillPollRelations(ctx context.Context, roomID domain.RoomID, startID domain.EventID) error {
	events, err := b.fetchPollRelations(ctx, roomID, startID)
	if err != nil {
		return err
	}
	b.pollBackfill.writes.Lock()
	defer b.pollBackfill.writes.Unlock()
	msg, ok, err := b.cache.MessageByID(ctx, roomID, startID)
	if err != nil {
		return fmt.Errorf("read poll start: %w", err)
	}
	if !ok || msg.Poll == nil {
		return nil
	}
	poll := clonePoll(msg.Poll)
	applyEvents(&poll, msg.Sender, events, b.isSelf)
	if err := b.cache.SetPoll(ctx, roomID, startID, poll); err != nil {
		return fmt.Errorf("cache poll backfill: %w", err)
	}
	b.changed(roomID)
	return nil
}

// fetchPollRelations reads every m.reference child of start, oldest first, parsing each
// as it comes.
func (b *InProc) fetchPollRelations(ctx context.Context, roomID domain.RoomID, startID domain.EventID) ([]*event.Event, error) {
	resp, err := b.client.GetRelations(ctx, id.RoomID(roomID), id.EventID(startID), &mautrix.ReqGetRelations{
		RelationType: event.RelReference,
		Dir:          mautrix.DirectionForward,
		Limit:        pollMaxAnswers,
	})
	if err != nil {
		return nil, fmt.Errorf("get relations: %w", err)
	}
	for _, evt := range resp.Chunk {
		evt.RoomID = id.RoomID(roomID)
		parse(evt)
	}
	return resp.Chunk, nil
}

// applyEvents applies a chronological list of poll responses and ends to the poll.
// starter is the start's sender (only they close the poll). Later events win on ties.
func applyEvents(p *domain.Poll, starter string, events []*event.Event, selves func(string) bool) {
	type stamped struct {
		at      int64
		seq     int
		evt     *event.Event
		isResp  bool
		isEnd   bool
		answers []string
	}
	kept := make([]stamped, 0, len(events))
	for i, evt := range events {
		switch evt.Type {
		case event.EventUnstablePollResponse:
			kept = append(kept, stamped{at: evt.Timestamp, seq: i, evt: evt, isResp: true, answers: pollResponseAnswers(evt)})
		case event.EventUnstablePollEnd:
			if string(evt.Sender) != starter {
				continue
			}
			kept = append(kept, stamped{at: evt.Timestamp, seq: i, evt: evt, isEnd: true})
		}
	}
	slices.SortStableFunc(kept, func(a, b stamped) int {
		if a.at != b.at {
			if a.at < b.at {
				return -1
			}
			return 1
		}
		return a.seq - b.seq
	})
	closedAt := int64(-1)
	for _, k := range kept {
		if k.isEnd && (closedAt < 0 || k.at < closedAt) {
			closedAt = k.at
		}
	}
	for _, k := range kept {
		if closedAt >= 0 && k.at > closedAt {
			continue // after the end is dropped (MSC3381)
		}
		if k.isResp {
			applyResponse(p, string(k.evt.Sender), k.answers, pollRound(k.at), selves)
		}
	}
	if closedAt >= 0 {
		p.Closed = true
	}
}

// isSelf names whether an MXID is this user: the account, or any of its configured
// identity MXIDs.
func (b *InProc) isSelf(mxid string) bool { return slices.Contains(b.me(), mxid) }

// VotePoll sends an m.poll.response. No options retracts the vote (an empty answers
// list). The response is encrypted when the room is.
func (b *InProc) VotePoll(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, options []string) error {
	content := &event.PollResponseEventContent{
		RelatesTo: event.RelatesTo{Type: event.RelReference, EventID: id.EventID(eventID)},
		Response:  event.PollResponse{Answers: slices.Clone(options)},
	}
	if err := b.sealed(ctx, roomID, func() error {
		_, err := b.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventUnstablePollResponse, content)
		return err //nolint:wrapcheck // wrapped once below
	}); err != nil {
		return fmt.Errorf("matrix: vote in %s: %w", roomID, refusal(err))
	}
	return nil
}

// CreatePoll sends an m.poll.start. multiple lets a voter pick several answers.
func (b *InProc) CreatePoll(ctx context.Context, roomID domain.RoomID, question string, options []string, multiple bool) (domain.EventID, error) {
	if question == "" {
		return "", fmt.Errorf("matrix: create poll: a question is needed")
	}
	if len(options) < 2 {
		return "", fmt.Errorf("matrix: create poll: at least two answers are needed")
	}
	answers := make([]event.PollOption, 0, len(options))
	for i, text := range options {
		answers = append(answers, event.PollOption{
			ID:             fmt.Sprintf("%d", i),
			MSC1767Message: event.MSC1767Message{Text: text},
		})
	}
	maxSel := 1
	if multiple {
		maxSel = len(options)
	}
	content := &event.PollStartEventContent{
		PollStart: event.PollStart{
			Kind:          "org.matrix.msc3381.poll.disclosed",
			MaxSelections: maxSel,
			Question:      event.MSC1767Message{Text: question},
			Answers:       answers,
		},
	}
	var resp *mautrix.RespSendEvent
	if err := b.sealed(ctx, roomID, func() error {
		var err error
		resp, err = b.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventUnstablePollStart, content)
		return err //nolint:wrapcheck // wrapped once below
	}); err != nil {
		return "", fmt.Errorf("matrix: create poll in %s: %w", roomID, refusal(err))
	}
	return domain.EventID(resp.EventID), nil
}

// PollVoters names who chose what in a poll by walking its /relations. Matrix polls
// name every voter (there is no anonymous mode); the poll's sender is included.
func (b *InProc) PollVoters(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.PollVoters, error) {
	msg, ok, err := b.pollStart(ctx, roomID, eventID)
	if err != nil {
		return domain.PollVoters{}, err
	}
	if !ok {
		return domain.PollVoters{}, fmt.Errorf("matrix: %s asks no poll", eventID)
	}
	events, err := b.fetchPollRelations(ctx, roomID, eventID)
	if err != nil {
		return domain.PollVoters{}, fmt.Errorf("matrix: read who voted in %s: %w", eventID, err)
	}
	tally := tallyVotes(msg.Sender, msg.Poll, events)
	out := domain.PollVoters{Voters: make([]domain.PollVoter, 0, len(tally.order))}
	for _, voter := range tally.order {
		out.Voters = append(out.Voters, domain.PollVoter{
			ID:      voter,
			Name:    b.senderName(ctx, roomID, voter),
			Options: tally.answers[voter],
		})
	}
	return out, nil
}

// pollStart loads a cached poll's start message, falling back to a server fetch so a
// poll outside the cache window still names its voters.
func (b *InProc) pollStart(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, bool, error) {
	if b.cache != nil {
		if msg, ok, err := b.cache.MessageByID(ctx, roomID, eventID); err == nil && ok && msg.Poll != nil {
			return msg, true, nil
		} else if err != nil {
			return domain.Message{}, false, fmt.Errorf("matrix: read cached poll %s: %w", eventID, err)
		}
	}
	evt, err := b.client.GetEvent(ctx, id.RoomID(roomID), id.EventID(eventID))
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("matrix: fetch poll %s: %w", eventID, err)
	}
	evt = b.hydrate(ctx, roomID, evt)
	if evt.Type != event.EventUnstablePollStart {
		return domain.Message{}, false, nil
	}
	msg, ok := pollStartMessage(evt)
	if !ok {
		return domain.Message{}, false, nil
	}
	return msg, true, nil
}

// voteTally is applyEvents' answers plus the voter order they were first seen in.
type voteTally struct {
	answers map[string][]string
	order   []string
}

// tallyVotes applies the chronological responses+ends to a fresh copy of start's poll,
// and returns who chose what, in first-seen voter order.
func tallyVotes(starter string, start *domain.Poll, events []*event.Event) voteTally {
	poll := clonePoll(start)
	poll.Ballots = map[string][]string{}
	poll.Rounds = map[string]uint32{}
	order := make([]string, 0, len(events))
	seen := map[string]bool{}
	// First-seen order comes from a chronological pass.
	type tsEvt struct {
		at  int64
		seq int
		evt *event.Event
	}
	ordered := make([]tsEvt, 0, len(events))
	for i, e := range events {
		if e.Type != event.EventUnstablePollResponse {
			continue
		}
		ordered = append(ordered, tsEvt{at: e.Timestamp, seq: i, evt: e})
	}
	slices.SortStableFunc(ordered, func(a, b tsEvt) int {
		if a.at != b.at {
			if a.at < b.at {
				return -1
			}
			return 1
		}
		return a.seq - b.seq
	})
	for _, o := range ordered {
		s := string(o.evt.Sender)
		if !seen[s] {
			seen[s] = true
			order = append(order, s)
		}
	}
	applyEvents(&poll, starter, events, func(string) bool { return false })
	return voteTally{answers: poll.Ballots, order: order}
}
