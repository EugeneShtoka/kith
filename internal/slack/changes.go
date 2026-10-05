package slack

import (
	"context"
	"fmt"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An edit replaces what a message says, a deletion takes it away; both arrive as
// messages about another one (message_changed, message_deleted). The cache folds an
// edit only when it is newer than the version it shows, so an older copy (a history
// page read before the edit) never undoes it.

// keepDeletedIf sets [display.deleted] keep: whether a deleted message's words stay in
// the cache, and an edited one's earlier versions. Called at startup, as the other
// adapters' is.
func (a *Adapter) keepDeletedIf(keep bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keepDeleted = keep
}

// keepsDeleted is [display.deleted] keep.
func (a *Adapter) keepsDeleted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keepDeleted
}

// onEdited folds a live edit onto the message it changes. A change that is no edit
// (a link unfurled under it) leaves what it says alone.
func (a *Adapter) onEdited(ctx context.Context, w *workspace, e *slackgo.MessageEvent) {
	sub := e.SubMessage
	if sub == nil || sub.Edited == nil {
		return
	}
	a.learnPeople(ctx, w, people([]slackgo.Msg{*sub}))
	msg, ok := incoming(e.Channel, sub, w.names())
	if !ok {
		return
	}
	msg.Edited = true
	msg.RevisionID = messageID(w.creds.Team, e.Channel, e.Timestamp) // the change's own ts
	if a.cache != nil {
		if _, ok := a.record(ctx, w, msg.RoomID, []domain.Message{msg}); ok {
			a.keepFile(ctx, msg, sub) // a file deleted from the message takes its source
			if a.onChanged != nil {
				a.onChanged(msg.RoomID)
			}
		}
	}
	emit(a, a.messages, msg)
}

// onDeleted marks a message deleted live, keeping its words only under [display.deleted]
// keep.
func (a *Adapter) onDeleted(ctx context.Context, w *workspace, e *slackgo.MessageEvent) {
	if e.DeletedTimestamp == "" {
		return
	}
	by := ""
	if e.PreviousMessage != nil && e.PreviousMessage.User != "" {
		by = personID(w.creds.Team, e.PreviousMessage.User)
	}
	a.markDeleted(ctx, roomID(w.creds.Team, e.Channel), messageID(w.creds.Team, e.Channel, e.DeletedTimestamp), by, tsTime(e.Timestamp))
}

// markDeleted marks a message deleted in the cache and tells the clients.
func (a *Adapter) markDeleted(ctx context.Context, room domain.RoomID, id domain.EventID, by string, at time.Time) {
	keep := a.keepsDeleted()
	gone := domain.Message{ID: id, RoomID: room, Redacted: true, RedactedBy: by, RedactedAt: at}
	if a.cache != nil {
		if err := a.cache.MarkRedacted(ctx, room, id, by, "", at, keep); err != nil {
			a.log.Warn("mark a message deleted failed", "room", room, "err", err)
		}
		if keep {
			if kept, ok, err := a.cache.MessageByID(ctx, room, id); err == nil && ok {
				gone.Body, gone.Format = kept.Body, kept.Format
			}
		}
		if a.onChanged != nil {
			a.onChanged(room)
		}
		a.recount(ctx, room)
	}
	emit(a, a.messages, gone)
}

// Redact deletes a message on Slack (yours, or another's where you may), and here.
func (a *Adapter) Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, _ string) error {
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return err
	}
	in, ts, ok := cutLast(domain.ParseID(string(eventID)).Native)
	if !ok || in != channel {
		return fmt.Errorf("slack: %s is not a message of %s", eventID, roomID)
	}
	if _, _, err := w.client.DeleteMessageContext(ctx, channel, ts); err != nil {
		return fmt.Errorf("slack: delete in %s: %w", roomID, err)
	}
	a.markDeleted(ctx, roomID, eventID, personID(w.creds.Team, w.creds.User), time.Now())
	return nil
}

// edit sends draft as the new version of the message it edits, and folds it in here
// (Slack's echo of it is the same version again).
func (a *Adapter) edit(ctx context.Context, w *workspace, roomID domain.RoomID, channel string, draft domain.Draft) error {
	in, ts, ok := cutLast(domain.ParseID(string(draft.Edits)).Native)
	if !ok || in != channel {
		return fmt.Errorf("slack: %s is not a message of %s", draft.Edits, roomID)
	}
	text := composed(draft, w.creds.Team)
	if _, _, _, err := w.client.UpdateMessageContext(ctx, channel, ts, slackgo.MsgOptionText(text, false)); err != nil {
		return fmt.Errorf("slack: edit in %s: %w", roomID, err)
	}
	now := time.Now()
	m := slackgo.Msg{User: w.creds.User, Text: text, Timestamp: ts, Edited: &slackgo.Edited{User: w.creds.User, Timestamp: slackTS(now)}}
	edited, ok := incoming(channel, &m, w.names())
	if !ok {
		return nil
	}
	edited.Edited, edited.RevisionID = true, messageID(w.creds.Team, channel, slackTS(now))
	if a.cache != nil {
		if _, ok := a.record(ctx, w, roomID, []domain.Message{edited}); ok && a.onChanged != nil {
			a.onChanged(roomID)
		}
	}
	emit(a, a.messages, edited)
	return nil
}

// slackTS is a time as a Slack ts.
func slackTS(t time.Time) string {
	return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/int(time.Microsecond))
}

// MessageHistory is a message's earlier versions as the cache kept them (under
// [display.deleted] keep); Slack keeps none to ask for.
func (a *Adapter) MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	if a.cache == nil {
		return nil, domain.Deletion{}, nil
	}
	revisions, err := a.cache.Revisions(ctx, roomID, eventID)
	if err != nil {
		return nil, domain.Deletion{}, fmt.Errorf("slack: versions of %s: %w", eventID, err)
	}
	return revisions, domain.Deletion{}, nil
}
