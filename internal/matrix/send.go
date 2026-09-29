package matrix

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Send posts a draft to a room.
func (b *InProc) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	content := buildMessage(draft, b.threadFallback(ctx, roomID, draft))
	// A caller-supplied txn ID makes a retried send idempotent on the homeserver.
	var opts []mautrix.ReqSendEvent
	if draft.TxnID != "" {
		opts = append(opts, mautrix.ReqSendEvent{TransactionID: draft.TxnID})
	}
	if err := b.sealed(ctx, roomID, func() error {
		_, err := b.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, &content, opts...)
		return err //nolint:wrapcheck // wrapped below, once for both refusal and failure
	}); err != nil {
		return fmt.Errorf("matrix: send message: %w", err)
	}
	// Rank addressed people higher in future mention dropdowns; best-effort.
	// Mentions is nil for messages without pills (see recordMentions).
	if b.cache != nil {
		b.debugIf(ctx, recordMentions(ctx, b.cache, roomID, &content), "record mentions", "room", roomID)
	}
	return nil
}

// recordMentions notes who a sent message addressed. content.Mentions may be nil;
// a panic here once killed the daemon after a scheduled send.
func recordMentions(ctx context.Context, rec *db.Cache, roomID domain.RoomID, content *event.MessageEventContent) error {
	if content == nil || content.Mentions == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	var errs []error
	for _, mention := range content.Mentions.UserIDs {
		errs = append(errs, rec.RecordMention(ctx, roomID, string(mention), now))
	}
	return errors.Join(errs...)
}

// threadFallback is what a thread reply's fallback m.in_reply_to points at: the
// newest cached message in the thread, else the root. Empty unless a plain thread send.
func (b *InProc) threadFallback(ctx context.Context, roomID domain.RoomID, draft domain.Draft) domain.EventID {
	if draft.ThreadRoot == "" || draft.ReplyTo != "" {
		return ""
	}
	if b.cache != nil {
		if latest, err := b.cache.LatestInThread(ctx, roomID, draft.ThreadRoot); err == nil && latest != "" {
			return latest
		}
	}
	return draft.ThreadRoot
}

// buildMessage turns a draft into event content. A mention travels three ways:
// m.mentions (drives the highlight), a matrix.to pill in formatted_body, and the
// plain name in body. The thread relation is set before the reply: SetThread writes
// a falling-back m.in_reply_to that a genuine SetReplyTo then overwrites.
func buildMessage(draft domain.Draft, fallback domain.EventID) event.MessageEventContent {
	msgType := event.MsgText
	if draft.Emote {
		msgType = event.MsgEmote
	}
	content := event.MessageEventContent{MsgType: msgType, Body: draft.Body}
	switch {
	case draft.ThreadRoot != "":
		content.RelatesTo = (&event.RelatesTo{}).SetThread(id.EventID(draft.ThreadRoot), id.EventID(fallback))
		if draft.ReplyTo != "" {
			content.RelatesTo.SetReplyTo(id.EventID(draft.ReplyTo))
		}
	case draft.ReplyTo != "":
		content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(id.EventID(draft.ReplyTo))
	}
	mentions := draft.LiveMentions()
	// No HTML at all for a message with neither formatting nor mentions.
	if html := messageHTML(draft, mentions); html != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = html
	}
	if len(mentions) > 0 {
		// People only: a room mention must not notify everybody in it.
		content.Mentions = &event.Mentions{UserIDs: make([]id.UserID, 0, len(mentions))}
		for _, mention := range mentions {
			if mention.Notifies() {
				content.Mentions.UserIDs = append(content.Mentions.UserIDs, id.UserID(mention.UserID))
			}
		}
	}
	// Edit last: SetEdit copies the built content into m.new_content (and clears the
	// outer m.mentions so a correction does not re-ping).
	if draft.Edits != "" {
		content.SetEdit(id.EventID(draft.Edits))
	}
	return content
}

// messageHTML is the formatted body for a draft, or "" when none is needed. A Plain
// draft still pills its mentions.
func messageHTML(draft domain.Draft, mentions []domain.Mention) string {
	if draft.Plain {
		if len(mentions) == 0 {
			return ""
		}
		return pillHTML(draft.Body, mentions)
	}
	return renderBody(draft.Body, mentions)
}

// pillHTML escapes body and wraps the first occurrence of each mention's name in a
// matrix.to anchor.
func pillHTML(body string, mentions []domain.Mention) string {
	segments := []string{body}
	for _, mention := range longestFirst(mentions) {
		segments = splitOnce(segments, mention)
	}
	var out strings.Builder
	for _, segment := range segments {
		if strings.HasPrefix(segment, "<a href=") {
			out.WriteString(segment) // already rendered, already escaped
			continue
		}
		out.WriteString(html.EscapeString(segment))
	}
	return out.String()
}

// splitOnce replaces the first literal occurrence of mention.Name across segments
// with an anchor segment, leaving already-rendered segments alone.
func splitOnce(segments []string, mention domain.Mention) []string {
	for i, segment := range segments {
		if strings.HasPrefix(segment, "<a href=") {
			continue
		}
		before, after, found := strings.Cut(segment, mention.Name)
		if !found {
			continue
		}
		anchor := fmt.Sprintf(`<a href="https://matrix.to/#/%s">%s</a>`,
			html.EscapeString(mention.Target()), html.EscapeString(mention.Name))
		out := make([]string, 0, len(segments)+2)
		out = append(out, segments[:i]...)
		out = append(out, before, anchor, after)
		out = append(out, segments[i+1:]...)
		return out
	}
	return segments
}

// SendReaction posts an m.reaction annotating target in roomID with key, encrypted
// when the room is: mautrix never encrypts reactions itself.
func (b *InProc) SendReaction(ctx context.Context, roomID domain.RoomID, target domain.EventID, key string) error {
	content := &event.ReactionEventContent{RelatesTo: event.RelatesTo{
		EventID: id.EventID(target),
		Type:    event.RelAnnotation,
		Key:     key,
	}}
	if err := b.sealed(ctx, roomID, func() error {
		evtType, payload, err := b.sealReaction(ctx, roomID, content)
		if err != nil {
			return err
		}
		_, err = b.client.SendMessageEvent(ctx, id.RoomID(roomID), evtType, payload)
		return err //nolint:wrapcheck // wrapped below, once for both refusal and failure
	}); err != nil {
		return fmt.Errorf("matrix: send reaction: %w", err)
	}
	return nil
}

// sealReaction encrypts a reaction for an encrypted room; the machine keeps its
// relation in the clear so servers can still aggregate it. Runs inside sealed, so
// client.Crypto is fixed and, when nil, the room is known to be unencrypted.
func (b *InProc) sealReaction(ctx context.Context, roomID domain.RoomID, content *event.ReactionEventContent) (event.Type, any, error) {
	if b.client.Crypto == nil || !b.roomEncrypted(ctx, roomID) {
		return event.EventReaction, content, nil
	}
	encrypted, err := b.client.Crypto.Encrypt(ctx, id.RoomID(roomID), event.EventReaction, content)
	if err != nil {
		return event.Type{}, nil, fmt.Errorf("encrypt reaction: %w", err)
	}
	return event.EventEncrypted, encrypted, nil
}
