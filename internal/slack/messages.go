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

// messageID is a message's ID: Slack's ts names it within its conversation.
func messageID(team, channel, ts string) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolSlack, team, channel+"/"+ts))
}

// shown is the message subtypes kith shows: what people and bots wrote. The rest are
// notices (joins, topic changes) or edits and deletions of other messages.
var shown = map[string]bool{
	"": true, "bot_message": true, "me_message": true, "thread_broadcast": true, "file_share": true,
}

// tsTime parses a Slack ts ("1700000000.123456": seconds and microseconds).
func tsTime(ts string) time.Time {
	secs, micros, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}
	}
	us, _ := strconv.ParseInt((micros + "000000")[:6], 10, 64)
	return time.Unix(s, us*int64(time.Microsecond))
}

// incoming is a Slack message as kith keeps it; false for one kith does not show.
func incoming(channel string, m *slackgo.Msg, n names) (domain.Message, bool) {
	if !shown[m.SubType] || m.Timestamp == "" || m.Hidden {
		return domain.Message{}, false
	}
	r := render(messageText(m), n)
	body, named := r.body, false
	// The first loadable file is the attachment, each other loadable one gets its own
	// row (fileRows), and an unloadable one is named under the text.
	shown := firstLoadable(m.Files)
	for i := range m.Files {
		if i != shown && !loadable(&m.Files[i]) {
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

// messageText is a message's mrkdwn: its text, else its blocks (app/bot posts), else
// its attachments' fallbacks.
func messageText(m *slackgo.Msg) string {
	if m.Text != "" {
		return m.Text
	}
	if t := blockText(m.Blocks); t != "" {
		return t
	}
	return attachmentText(m.Attachments)
}

// attachmentText is the fallback text of each attachment (bot posts, unfurled links).
func attachmentText(attachments []slackgo.Attachment) string {
	var parts []string
	for i := range attachments {
		if t := cmpOr(attachments[i].Fallback, attachments[i].Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// labeled is a placeholder for what kith cannot show yet, followed by its own words.
func labeled(kind, words string) string {
	if words = strings.TrimSpace(words); words != "" {
		return "[" + kind + "] " + words
	}
	return "[" + kind + "]"
}

var userRefs = regexp.MustCompile(`<@([A-Z0-9]+)[|>]`)

// people is everyone msgs need named: senders and mentioned users.
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

// learnPeople looks up the users w does not know yet; on failure they stay unnamed
// (IDs show) and it is logged.
func (a *Adapter) learnPeople(ctx context.Context, w *workspace, users []string) {
	unknown := w.unnamed(users)
	for chunk := range slices.Chunk(unknown, usersPerLookup) {
		found, err := w.client.GetUsersInfoContext(ctx, chunk...)
		if err != nil {
			a.log.Warn("look up people's names failed", "account", w.account.Name, "count", len(chunk), "err", err)
			return
		}
		for i := range *found {
			w.knowUser(&(*found)[i])
		}
	}
	for _, u := range unknown {
		if name, ok := slackOwn[u]; ok {
			w.knowPerson(u, name)
		}
	}
	if len(unknown) > 0 {
		a.keepPeople(ctx, w)
	}
}

// usersPerPage is the users.list page size (Slack's maximum).
const usersPerPage = 200

// listEveryone reads every workspace member for the directory, so people are named
// before they speak and linked by number across networks. Rate limits are waited out
// per page.
func (a *Adapter) listEveryone(ctx context.Context, w *workspace) {
	users, err := w.client.GetUsersContext(ctx, slackgo.GetUsersOptionLimit(usersPerPage))
	if err != nil {
		a.log.Warn("list the workspace's members failed", "account", w.account.Name, "err", err)
		return
	}
	for i := range users {
		if !users[i].Deleted {
			w.knowUser(&users[i])
		}
	}
	a.keepPeople(ctx, w)
}

// keepPeople gives the directory the workspace's known users.
func (a *Adapter) keepPeople(ctx context.Context, w *workspace) {
	if a.cache == nil {
		return
	}
	names, links := w.directory()
	if err := a.cache.SetPeople(ctx, "slack:"+w.creds.Team, names, links); err != nil {
		a.log.Warn("keep who people are failed", "account", w.account.Name, "err", err)
	}
}

// arrived caches a live message and emits it. A conversation the cache lacks (new DM,
// just joined) becomes one of the account's rooms first and the listing is redone for
// its name.
func (a *Adapter) arrived(ctx context.Context, w *workspace, channel string, m *slackgo.Msg) {
	a.learnPeople(ctx, w, people([]slackgo.Msg{*m}))
	msg, ok := incoming(channel, m, w.names())
	if !ok {
		return
	}
	all := append([]domain.Message{msg}, fileRows(msg, m)...)
	if a.cache != nil {
		joined, ok := a.record(ctx, w, msg.RoomID, all)
		if !ok {
			return
		}
		for i := range all {
			a.keepFile(ctx, all[i], m)
		}
		a.heardOf(msg)
		// A newly heard room is read up to just before the message that revealed it.
		a.placeRead(ctx, msg.RoomID, msg.Timestamp.Add(-time.Millisecond))
		a.recount(ctx, msg.RoomID)
		a.wantRestOf(w, channel, m)
		if joined {
			go a.relist(context.WithoutCancel(ctx), w)
		}
	}
	for i := range all {
		emit(a, a.messages, all[i])
	}
}

// record adds the room to the account's rooms and caches msgs under listing, so no
// sweep falls between (see keptRooms). It reports whether the room is new and whether
// the messages were cached.
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

// heardOf reports each newly cached message to onCached.
func (a *Adapter) heardOf(msg domain.Message) {
	if a.onCached != nil {
		a.onCached(msg)
	}
}

// relist lists a workspace again after a message came from an unlisted conversation.
func (a *Adapter) relist(ctx context.Context, w *workspace) {
	if _, err := a.list(ctx, w); err != nil {
		a.log.Warn("list the workspace again failed", "account", w.account.Name, "err", err)
	}
}

// emit sends v without blocking; a client not reading misses it and catches up from the
// cache, as with the other adapters.
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
