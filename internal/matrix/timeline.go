package matrix

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// undecryptableBody stands in for an encrypted event we have no megolm session for,
// so undecryptable history stays visible instead of looking like an empty room.
const undecryptableBody = "🔒 Encrypted message — no decryption key"

// cacheMessages writes messages to the cache, plus their revisions when keepDeleted.
// Best-effort: every path that caches messages goes through here.
func (b *InProc) cacheMessages(ctx context.Context, roomID domain.RoomID, msgs []domain.Message) {
	if b.cache == nil || len(msgs) == 0 {
		return
	}
	save := b.cache.SaveMessages
	if b.keepDeleted {
		save = b.cache.SaveMessagesWithRevisions
	}
	b.warnIf(ctx, save(ctx, roomID, msgs), "cache messages", "room", roomID, "count", len(msgs))
}

// Timeline fetches a page of scrollback via /messages, oldest-first. At the start
// of a room's history, Next continues into its predecessor (see chainNext).
func (b *InProc) Timeline(ctx context.Context, roomID domain.RoomID, from string, limit int) (domain.TimelinePage, error) {
	roomID, from, depth := chainStep(roomID, from)
	resp, err := b.client.Messages(ctx, id.RoomID(roomID), from, "", mautrix.DirectionBackward, nil, limit)
	if err != nil {
		if depth > 0 {
			// An unreadable predecessor is the end of history, not an error.
			b.debugIf(ctx, err, "read predecessor timeline", "room", roomID)
			return domain.TimelinePage{}, nil
		}
		return domain.TimelinePage{}, fmt.Errorf("matrix: timeline: %w", err)
	}
	next := resp.End
	if len(resp.Chunk) == 0 {
		next = ""
	}
	if next == "" {
		next = b.chainNext(ctx, roomID, depth)
	}
	return b.pageFrom(ctx, roomID, resp.Chunk, next), nil
}

// chainPrefix marks a Next token we made up: "continue in this other room".
// Server tokens are opaque, so ours must be something no server would emit.
const chainPrefix = "kith:chain:"

// chainNext returns the token continuing scrollback into roomID's predecessor, or
// "" at the real end. The depth in the token bounds cycles a server could write.
func (b *InProc) chainNext(ctx context.Context, roomID domain.RoomID, depth int) string {
	if b.cache == nil || depth >= chainWalkLimit {
		return ""
	}
	up, known, err := b.cache.RoomUpgrade(ctx, roomID)
	if err != nil || !known || up.Predecessor == "" {
		return ""
	}
	return fmt.Sprintf("%s%d:%s", chainPrefix, depth+1, up.Predecessor)
}

// chainWalkLimit bounds how many upgrades one scrollback walks through.
const chainWalkLimit = 16

// chainStep decodes a chainNext token into (predecessor, "", depth); other tokens
// pass through at depth 0. Messages stay keyed to the room that holds them, since
// later edits and redactions are addressed to that room.
func chainStep(roomID domain.RoomID, from string) (domain.RoomID, string, int) {
	rest, ok := strings.CutPrefix(from, chainPrefix)
	if !ok {
		return roomID, from, 0
	}
	depthText, room, ok := strings.Cut(rest, ":")
	if !ok {
		return roomID, "", 0
	}
	depth, err := strconv.Atoi(depthText)
	if err != nil || room == "" {
		return roomID, "", 0
	}
	return domain.RoomID(room), "", depth
}

// pageFrom turns one backward chunk (newest-first) into an oldest-first page,
// decrypted, converted and written to the cache. Shared by /messages and /relations.
func (b *InProc) pageFrom(ctx context.Context, roomID domain.RoomID, chunk []*event.Event, next string) domain.TimelinePage {
	msgs := make([]domain.Message, 0, len(chunk))
	// Undecryptable placeholders are not cached: a cache hit would pin them unreadable
	// after the session arrives.
	cacheable := make([]domain.Message, 0, len(chunk))
	var reactions []domain.Reaction
	// Attachment sources are keyed to the cached message, so save them after it.
	var attachments []*event.Event
	for _, c := range slices.Backward(chunk) {
		evt := b.hydrate(ctx, roomID, c)
		if evt.Type == event.EventReaction {
			if r, ok := toReaction(evt); ok {
				r.RoomID = roomID
				reactions = append(reactions, r)
			}
			continue
		}
		msg, undecrypted, ok := b.fetchedMessage(ctx, roomID, evt)
		if !ok {
			continue // a non-message event (state change, redaction, …)
		}
		if msg.Media != nil {
			attachments = append(attachments, evt)
		}
		msgs = append(msgs, msg)
		if !undecrypted {
			cacheable = append(cacheable, msg)
		}
	}
	if b.cache != nil && len(cacheable) > 0 {
		b.cacheMessages(ctx, roomID, cacheable)
		for _, evt := range attachments {
			b.saveMediaSource(ctx, roomID, evt)
		}
	}
	if b.cache != nil && len(reactions) > 0 {
		b.warnIf(ctx, b.cache.SaveReactions(ctx, reactions), "cache reactions", "room", roomID, "count", len(reactions))
	}
	b.restoreKept(ctx, roomID, msgs)
	return domain.TimelinePage{Messages: msgs, Reactions: reactions, Next: next}
}

// hydrate readies a server-fetched event: /messages and friends omit room_id and
// deliver content unparsed, and both decryption and conversion need them.
func (b *InProc) hydrate(ctx context.Context, roomID domain.RoomID, evt *event.Event) *event.Event {
	evt.RoomID = id.RoomID(roomID)
	parse(evt)
	return b.decryptEvent(ctx, evt)
}

// fetchedMessage converts a hydrated event, substituting a visible placeholder for
// one we could not decrypt (undecrypted: never cache it, or it pins the message
// unreadable after its session arrives). ok is false for non-message events.
func (b *InProc) fetchedMessage(ctx context.Context, roomID domain.RoomID, evt *event.Event) (msg domain.Message, undecrypted, ok bool) {
	msg, ok = toDomainMessage(evt)
	if !ok {
		if evt.Type != event.EventEncrypted {
			return domain.Message{}, false, false
		}
		msg = domain.Message{
			ID:        domain.EventID(evt.ID),
			Sender:    string(evt.Sender),
			Body:      undecryptableBody,
			Timestamp: time.UnixMilli(evt.Timestamp),
		}
		undecrypted = true
	}
	msg.RoomID = roomID
	msg.Mentioned = mentionsMe(evt, b.client.UserID)
	msg.SenderName = b.senderName(ctx, roomID, msg.Sender)
	return msg, undecrypted, true
}

// restoreKept restores the kept text of redacted messages in a server-fetched page:
// the server has stripped it, but [display.deleted] keep may have cached it.
func (b *InProc) restoreKept(ctx context.Context, roomID domain.RoomID, msgs []domain.Message) {
	if b.cache == nil {
		return
	}
	for i := range msgs {
		if !msgs[i].Redacted || msgs[i].Body != "" {
			continue
		}
		kept, ok, err := b.cache.Message(ctx, roomID, msgs[i].ID)
		if err != nil || !ok || kept.Body == "" {
			continue
		}
		msgs[i].Body, msgs[i].Format = kept.Body, kept.Format
	}
}

// MessageHistory is every version of one message, oldest first, merged from the
// cache (kept versions) and the server (every m.replace, but no redacted original).
// Fetched on demand only.
func (b *InProc) MessageHistory(
	ctx context.Context, roomID domain.RoomID, eventID domain.EventID,
) ([]domain.Revision, domain.Deletion, error) {
	var kept []domain.Revision
	if b.cache != nil {
		var err error
		kept, err = b.cache.Revisions(ctx, roomID, eventID)
		// The server's copy is still fetched; only kept history is missing.
		b.warnIf(ctx, err, "read cached revisions", "room", roomID, "event", eventID)
	}
	fetched, deletion := b.fetchRevisions(ctx, roomID, eventID)
	// Cache fetched versions only when this account keeps history.
	if b.keepDeleted && b.cache != nil && len(fetched) > 0 {
		b.warnIf(ctx, b.cache.SaveRevisionsFor(ctx, roomID, eventID, fetched), "cache fetched revisions", "room", roomID, "event", eventID)
	}
	return mergeRevisions(kept, fetched), deletion, nil
}

// fetchRevisions asks the server for the original (the first version) and every
// replacement. Failures yield what was gathered so far.
func (b *InProc) fetchRevisions(
	ctx context.Context, roomID domain.RoomID, eventID domain.EventID,
) ([]domain.Revision, domain.Deletion) {
	out := make([]domain.Revision, 0, 4)
	var deletion domain.Deletion
	if original, err := b.client.GetEvent(ctx, id.RoomID(roomID), id.EventID(eventID)); err == nil {
		original.RoomID = id.RoomID(roomID)
		parse(original)
		// A redacted event carries its redaction in unsigned.redacted_because, the only
		// source for when it was deleted.
		if because := original.Unsigned.RedactedBecause; because != nil {
			parse(because)
			deletion = domain.Deletion{
				At:     eventTime(because.Timestamp),
				By:     string(because.Sender),
				Reason: redactionReason(because),
			}
		}
		original = b.decryptEvent(ctx, original)
		if body := original.Content.AsMessage().Body; body != "" {
			out = append(out, domain.Revision{
				ID:     eventID,
				Body:   body,
				Format: formatting(original.Content.AsMessage()),
				At:     time.UnixMilli(original.Timestamp),
			})
		}
	}
	resp, err := b.client.GetRelations(ctx, id.RoomID(roomID), id.EventID(eventID), &mautrix.ReqGetRelations{
		RelationType: event.RelReplace,
		Dir:          mautrix.DirectionForward,
	})
	if err != nil {
		b.warnIf(ctx, err, "fetch edit history", "room", roomID, "event", eventID)
		return out, deletion
	}
	for _, evt := range resp.Chunk {
		evt = b.hydrate(ctx, roomID, evt)
		content := evt.Content.AsMessage()
		body := editBody(content)
		if body == "" {
			continue // a redacted edit: it happened, and what it said is gone
		}
		out = append(out, domain.Revision{
			ID:     domain.EventID(evt.ID),
			Body:   body,
			Format: formatting(content),
			At:     time.UnixMilli(evt.Timestamp),
		})
	}
	return out, deletion
}

// mergeRevisions unions two lists by event ID, oldest first; kept wins ties.
func mergeRevisions(kept, fetched []domain.Revision) []domain.Revision {
	seen := make(map[domain.EventID]bool, len(kept)+len(fetched))
	out := make([]domain.Revision, 0, len(kept)+len(fetched))
	for _, list := range [][]domain.Revision{kept, fetched} {
		for _, rev := range list {
			if seen[rev.ID] {
				continue
			}
			seen[rev.ID] = true
			out = append(out, rev)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].ID < out[j].ID
		}
		return out[i].At.Before(out[j].At)
	})
	return out
}

// FetchEvent fetches one event from the homeserver (e.g. a thread root older than the
// cached window) and caches it, best-effort.
func (b *InProc) FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	evt, err := b.client.GetEvent(ctx, id.RoomID(roomID), id.EventID(eventID))
	if err != nil {
		return domain.Message{}, fmt.Errorf("matrix: fetch event %s: %w", eventID, err)
	}
	evt = b.hydrate(ctx, roomID, evt)
	msg, undecrypted, ok := b.fetchedMessage(ctx, roomID, evt)
	if !ok {
		return domain.Message{}, fmt.Errorf("matrix: fetch event %s: not a message event", eventID)
	}
	if b.cache != nil && !undecrypted {
		b.cacheMessages(ctx, roomID, []domain.Message{msg})
		if msg.Media != nil {
			b.saveMediaSource(ctx, roomID, evt)
		}
	}
	one := []domain.Message{msg}
	b.restoreKept(ctx, roomID, one)
	return one[0], nil
}

// decryptEvent decrypts an m.room.encrypted scrollback event when possible, else
// returns it unchanged. Live events are decrypted by the sync loop instead.
func (b *InProc) decryptEvent(ctx context.Context, evt *event.Event) *event.Event {
	helper := b.cryptoHelper()
	if helper == nil || evt.Type != event.EventEncrypted {
		return evt
	}
	if decrypted, err := helper.Decrypt(ctx, evt); err == nil {
		return decrypted
	}
	return evt
}
