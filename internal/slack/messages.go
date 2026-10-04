package slack

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// messageID is a message: Slack's ts names it within its conversation.
func messageID(team, channel, ts string) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolSlack, team, channel+"/"+ts))
}

// shown is the message subtypes kith shows: what people (and their bots) wrote. The
// rest are notices (joins, topic changes) or changes to other messages (edits and
// deletions, which come later).
var shown = map[string]bool{
	"": true, "bot_message": true, "me_message": true, "thread_broadcast": true, "file_share": true,
}

// tsTime is the time a Slack ts ("1700000000.123456", seconds and microseconds) says.
func tsTime(ts string) time.Time {
	secs, micros, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}
	}
	us, _ := strconv.ParseInt((micros + "000000")[:6], 10, 64)
	return time.Unix(s, us*int64(time.Microsecond))
}

// incoming is a Slack message in a conversation as kith keeps it; false for one kith
// does not show.
func incoming(channel string, m *slackgo.Msg, n names) (domain.Message, bool) {
	if !shown[m.SubType] || m.Timestamp == "" || m.Hidden {
		return domain.Message{}, false
	}
	r := render(messageText(m), n)
	body, named := r.body, false
	// The first file kith can load is the attachment; any other is named under the text.
	shown := firstLoadable(m.Files)
	for i := range m.Files {
		if i != shown {
			body, named = strings.TrimSpace(body+"\n"+labeled("file", cmpOr(m.Files[i].Title, m.Files[i].Name))), true
		}
	}
	var media *domain.Media
	if shown >= 0 {
		media = fileMedia(&m.Files[shown])
		if body == "" {
			body = media.Name // a file with no words: its name, which reads as no caption
		}
	}
	if body == "" {
		return domain.Message{}, false
	}
	out := domain.Message{
		ID:        messageID(n.team, channel, m.Timestamp),
		RoomID:    roomID(n.team, channel),
		Body:      body,
		Timestamp: tsTime(m.Timestamp),
		Emote:     m.SubType == "me_message",
		Mentions:  r.mentions,
		Mentioned: r.mentioned && m.User != n.me,
		Media:     media,
	}
	if !named && body == r.body {
		out.Format = r.format // a file's label or name is not in the formatting's words
	}
	switch {
	case m.User != "":
		out.Sender, out.SenderName = personID(n.team, m.User), n.user(m.User)
	case m.BotID != "":
		out.Sender = personID(n.team, m.BotID)
		out.SenderName = m.Username
		if out.SenderName == "" && m.BotProfile != nil {
			out.SenderName = m.BotProfile.Name
		}
	}
	if m.Edited != nil {
		out.Edited, out.EditedAt = true, tsTime(m.Edited.Timestamp)
	}
	// A reply in a thread; one also sent to the channel reads in the channel.
	if m.ThreadTimestamp != "" && m.ThreadTimestamp != m.Timestamp && m.SubType != "thread_broadcast" {
		out.ThreadRoot = messageID(n.team, channel, m.ThreadTimestamp)
	}
	return out, true
}

// messageText is what a message says, as mrkdwn: its text, else its blocks (an app's
// or bot's post), else its attachments' fallbacks.
func messageText(m *slackgo.Msg) string {
	if m.Text != "" {
		return m.Text
	}
	if t := blockText(m.Blocks); t != "" {
		return t
	}
	return attachmentText(m.Attachments)
}

// attachmentText is what a message made only of attachments (a bot's, a link
// unfurled) says: each one's fallback text.
func attachmentText(attachments []slackgo.Attachment) string {
	var parts []string
	for i := range attachments {
		if t := cmpOr(attachments[i].Fallback, attachments[i].Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// labeled is a placeholder for what kith cannot show yet, with its own words after.
func labeled(kind, words string) string {
	if words = strings.TrimSpace(words); words != "" {
		return "[" + kind + "] " + words
	}
	return "[" + kind + "]"
}

// userRefs finds the people a text mentions.
var userRefs = regexp.MustCompile(`<@([A-Z0-9]+)[|>]`)

// people is everyone messages need named: their senders and whom they mention.
func people(msgs []slackgo.Msg) []string {
	var users []string
	add := func(u string) {
		if u != "" && !slices.Contains(users, u) {
			users = append(users, u)
		}
	}
	for i := range msgs {
		add(msgs[i].User)
		for _, m := range userRefs.FindAllStringSubmatch(messageText(&msgs[i]), -1) {
			add(m[1])
		}
	}
	return users
}

// usersPerLookup bounds one users.info call.
const usersPerLookup = 50

// learnPeople asks Slack the names of the people w does not know yet among users.
// A lookup that fails leaves them unnamed (their IDs show), logged.
func (a *Adapter) learnPeople(ctx context.Context, w *workspace, users []string) {
	unknown := w.unnamed(users)
	for chunk := range slices.Chunk(unknown, usersPerLookup) {
		found, err := w.client.GetUsersInfoContext(ctx, chunk...)
		if err != nil {
			a.log.Warn("look up people's names failed", "account", w.account.Name, "count", len(chunk), "err", err)
			return
		}
		for i := range *found {
			w.knowPerson((*found)[i].ID, userName((*found)[i]))
		}
	}
	for _, u := range unknown {
		if name, ok := slackOwn[u]; ok {
			w.knowPerson(u, name)
		}
	}
}

// arrived caches one message heard live and hands it to the clients. A conversation
// the cache does not know yet (a DM just begun, a channel just joined) is made one of
// the account's rooms first, and the listing asked again for its name.
func (a *Adapter) arrived(ctx context.Context, w *workspace, channel string, m *slackgo.Msg) {
	a.learnPeople(ctx, w, people([]slackgo.Msg{*m}))
	msg, ok := incoming(channel, m, w.names())
	if !ok {
		return
	}
	if a.cache != nil {
		joined, ok := a.record(ctx, w, msg.RoomID, []domain.Message{msg})
		if !ok {
			return
		}
		a.keepFile(ctx, msg, m)
		a.heardOf(msg)
		// A room first heard of is read up to just before what made it known.
		a.placeRead(ctx, msg.RoomID, msg.Timestamp.Add(-time.Millisecond))
		a.recount(ctx, msg.RoomID)
		a.wantRestOf(w, channel, m)
		if joined {
			go a.relist(context.WithoutCancel(ctx), w)
		}
	}
	emit(a, a.messages, msg)
}

// record caches messages of one room, the room made one of the account's first, where
// no listing's sweep can come between the two (see keptRooms). It reports whether the
// room was not joined before, and whether the messages were cached.
func (a *Adapter) record(ctx context.Context, w *workspace, room domain.RoomID, msgs []domain.Message) (joined, ok bool) {
	a.listing.Lock()
	a.heard[room] = time.Now()
	n, err := a.cache.JoinRooms(ctx, domain.AccountRooms(domain.ProtocolSlack, w.creds.Team), []domain.RoomID{room})
	if err == nil {
		save := a.cache.SaveMessages
		if a.keepsDeleted() {
			save = a.cache.SaveMessagesWithRevisions // an edit keeps what it replaced
		}
		err = save(ctx, room, msgs)
	}
	if err == nil {
		a.heardReplies(msgs)
	}
	a.listing.Unlock()
	if err != nil {
		a.log.Warn("cache messages failed", "account", w.account.Name, "room", room, "count", len(msgs), "err", err)
		return false, false
	}
	return n > 0, true
}

// heardOf tells who hears new messages cached (word completion) of one.
func (a *Adapter) heardOf(msg domain.Message) {
	if a.onCached != nil {
		a.onCached(msg)
	}
}

// relist lists a workspace again, for a conversation a message came to first.
func (a *Adapter) relist(ctx context.Context, w *workspace) {
	if _, err := a.list(ctx, w); err != nil {
		a.log.Warn("list the workspace again failed", "account", w.account.Name, "err", err)
	}
}

// emit hands v to a stream without blocking: a client that is not reading misses it,
// as with the other adapters (it reads the cache when it catches up).
func emit[T any](a *Adapter, ch chan T, v T) {
	a.streamMu.RLock()
	defer a.streamMu.RUnlock()
	if a.closed {
		return
	}
	select {
	case ch <- v:
	default:
	}
}
