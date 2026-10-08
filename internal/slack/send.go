package slack

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Send posts a text message: Markdown as mrkdwn, mentions as Slack's own; a draft that
// edits a message replaces it (changes.go). A draft written in a thread is posted in
// it; a reply, in the thread of the message it answers — Slack has no other kind. The message is cached and streamed
// here; Slack's echo of it over the websocket is the same message again.
func (a *Adapter) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return err
	}
	if draft.Edits != "" {
		return a.edit(ctx, w, roomID, channel, draft)
	}
	text := composed(draft, w.creds.Team)
	options := []slackgo.MsgOption{slackgo.MsgOptionText(text, false)}
	thread := a.threadOf(ctx, w, roomID, channel, cmp.Or(draft.ThreadRoot, draft.ReplyTo))
	if thread != "" {
		options = append(options, slackgo.MsgOptionTS(thread))
	}
	_, ts, err := w.client.PostMessageContext(ctx, channel, options...)
	if err != nil {
		return fmt.Errorf("slack: send to %s: %w", roomID, err)
	}
	// The draft names whom it mentions; Slack is asked only for whom it does not.
	for _, mention := range draft.LiveMentions() {
		if user, ok := userOf(mention.UserID, w.creds.Team); ok {
			w.knowPerson(user, strings.TrimPrefix(mention.Name, "@"))
		}
	}
	m := slackgo.Msg{User: w.creds.User, Text: text, Timestamp: ts, ThreadTimestamp: thread}
	a.learnPeople(ctx, w, people([]slackgo.Msg{m}))
	sent, ok := incoming(channel, &m, w.names())
	if !ok {
		return nil
	}
	if a.cache != nil {
		if _, ok := a.record(ctx, w, roomID, []domain.Message{sent}); ok {
			a.heardOf(sent)
			a.countMentions(ctx, sent)
		}
	}
	emit(a, a.messages, sent)
	return nil
}

// threadOf is the ts of the thread a reply to replyTo goes in: the thread replyTo is
// in, or the one it starts. "" when there is nothing of this conversation's to answer.
func (a *Adapter) threadOf(ctx context.Context, w *workspace, roomID domain.RoomID, channel string, replyTo domain.EventID) string {
	target := domain.ParseID(string(replyTo))
	if replyTo == "" || target.Network != domain.ProtocolSlack || target.Account != w.creds.Team {
		return ""
	}
	in, ts, ok := cutLast(target.Native)
	if !ok || in != channel {
		return ""
	}
	if a.cache != nil {
		if original, found, err := a.cache.MessageByID(ctx, roomID, replyTo); err == nil && found && original.ThreadRoot != "" {
			if _, root, ok := cutLast(domain.ParseID(string(original.ThreadRoot)).Native); ok {
				return root
			}
		}
	}
	return ts
}

// cutLast splits a message's native ID, "C…/ts", into its conversation and ts.
func cutLast(native string) (channel, ts string, ok bool) {
	native, _, _ = strings.Cut(native, fileSep) // a file's row is its message
	for i := len(native) - 1; i >= 0; i-- {
		if native[i] == '/' {
			return native[:i], native[i+1:], i > 0 && i < len(native)-1
		}
	}
	return "", "", false
}

// countMentions records who our message mentions, for the dropdown's ranking.
func (a *Adapter) countMentions(ctx context.Context, sent domain.Message) {
	for _, m := range sent.Mentions {
		if m.UserID == "" {
			continue
		}
		if err := a.cache.RecordMention(ctx, sent.RoomID, m.UserID, sent.Timestamp.UnixMilli()); err != nil {
			a.log.Warn("record a mention failed", "room", sent.RoomID, "err", err)
		}
	}
}

// FetchEvent is a cached message, else the one Slack has under its ts.
func (a *Adapter) FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	if a.cache != nil {
		if msg, ok, err := a.cache.MessageByID(ctx, roomID, eventID); err == nil && ok {
			return msg, nil
		}
	}
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return domain.Message{}, err
	}
	in, ts, ok := cutLast(domain.ParseID(string(eventID)).Native)
	if !ok || in != channel {
		return domain.Message{}, fmt.Errorf("slack: %s is not a message of %s", eventID, roomID)
	}
	var resp *slackgo.GetConversationHistoryResponse
	err = waitingOut(ctx, func() (err error) {
		resp, err = w.client.GetConversationHistoryContext(ctx, &slackgo.GetConversationHistoryParameters{
			ChannelID: channel, Latest: ts, Oldest: ts, Inclusive: true, Limit: 1,
		})
		if err != nil {
			return fmt.Errorf("slack: fetch %s: %w", eventID, err)
		}
		return nil
	})
	if err != nil {
		return domain.Message{}, err
	}
	for i := range resp.Messages {
		m := resp.Messages[i].Msg
		a.learnPeople(ctx, w, people([]slackgo.Msg{m}))
		if msg, ok := incoming(channel, &m, w.names()); ok && msg.ID == eventID {
			return msg, nil
		}
	}
	return domain.Message{}, fmt.Errorf("slack: %s is not in %s's history (a thread reply, or deleted)", eventID, roomID)
}
