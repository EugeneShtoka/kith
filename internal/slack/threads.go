package slack

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A thread's replies are not in its conversation's history (but for one also sent to
// the channel); conversations.replies reads them, from the thread's start. A history
// page names, on each message a thread hangs off, its newest reply: a thread whose
// newest reply the cache lacks is read in the background, one at a time, so a thread
// shows (and counts) as it would have had kith been listening.
//
// Slack keeps a read position per thread, and says it when the thread is read
// (last_read on the root) and when it is read on another client (thread_marked, live);
// marking a thread read is kept here only, for no Slack client API slackgo knows
// moves it. A thread counts as unread only where you follow it — you
// spoke in it, or it named you — as Slack badges only the threads you follow. A thread
// is not read when its room is (Slack keeps the two apart), but marking rooms read
// reads their threads too (the thread floor).

const (
	// threadPage is how many replies one conversations.replies call asks for.
	threadPage = 200
	// threadPages bounds how much of one thread is read: threadPage×threadPages replies.
	threadPages = 20
)

// ListThreads is a room's cached threads, newest activity first, each with what is
// unread in it as the room's count has it.
func (a *Adapter) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	if a.cache == nil {
		return nil, nil
	}
	threads, err := a.cache.Threads(ctx, a.Me(), roomID)
	if err != nil {
		return nil, fmt.Errorf("slack: threads of %s: %w", roomID, err)
	}
	unread, err := a.cache.CountThreadUnread(ctx, a.Me(), roomID)
	if err != nil {
		return nil, fmt.Errorf("slack: count unread threads of %s: %w", roomID, err)
	}
	return domain.WithUnread(threads, domain.Unread{Threads: a.followed(ctx, roomID, unread)}), nil
}

// ThreadPage is a whole thread, its root first, read from Slack and cached: Slack pages
// a thread only from its start, so there is never an older page (Next is "").
func (a *Adapter) ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, _ string, limit int) (domain.TimelinePage, error) {
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return domain.TimelinePage{}, err
	}
	in, ts, ok := cutLast(domain.ParseID(string(root)).Native)
	if !ok || in != channel {
		return domain.TimelinePage{}, fmt.Errorf("slack: %s is not a message of %s", root, roomID)
	}
	msgs, reactions, err := a.readThread(ctx, w, channel, ts, limit)
	if err != nil {
		return domain.TimelinePage{}, err
	}
	return domain.TimelinePage{Messages: msgs, Reactions: reactions}, nil
}

// readThread reads a thread from Slack, page by page of size (threadPage when not
// positive), and caches it (cacheThread).
func (a *Adapter) readThread(ctx context.Context, w *workspace, channel, ts string, size int) ([]domain.Message, []domain.Reaction, error) {
	fetched := time.Now()
	replies, lastRead, err := a.fetchThread(ctx, w, channel, ts, size)
	if err != nil {
		return nil, nil, err
	}
	msgs, reactions := a.cacheThread(ctx, w, channel, ts, replies, lastRead, fetched)
	return msgs, reactions, nil
}

// fetchThread is a thread as Slack holds it, its root then its replies, oldest first,
// and how far Slack says you read it ("" when it does not say).
func (a *Adapter) fetchThread(ctx context.Context, w *workspace, channel, ts string, size int) ([]slackgo.Message, string, error) {
	if size <= 0 {
		size = threadPage
	}
	var replies []slackgo.Message
	seen := map[string]bool{}
	lastRead, cursor := "", ""
	for range threadPages {
		var resp *slackgo.GetConversationHistoryResponse
		err := waitingOut(ctx, func() (err error) {
			resp, err = w.client.GetConversationRepliesContext(ctx, &slackgo.GetConversationRepliesParameters{
				GetConversationHistoryParameters: slackgo.GetConversationHistoryParameters{ChannelID: channel, Cursor: cursor, Limit: size},
				Timestamp:                        ts,
			})
			if err != nil {
				return fmt.Errorf("slack: thread %s in %s: %w", ts, roomID(w.creds.Team, channel), err)
			}
			return nil
		})
		if err != nil {
			return nil, "", err
		}
		for i := range resp.Messages {
			m := resp.Messages[i]
			if m.Timestamp == ts {
				lastRead = cmp.Or(m.LastRead, lastRead)
				a.mu.Lock()
				a.threadsAsked[messageID(w.creds.Team, channel, ts)] = m.LatestReply
				a.mu.Unlock()
			}
			if !seen[m.Timestamp] { // each page may begin with the root again
				seen[m.Timestamp] = true
				replies = append(replies, m)
			}
		}
		if cursor = resp.ResponseMetaData.NextCursor; !resp.HasMore || cursor == "" {
			break
		}
	}
	return replies, lastRead, nil
}

// cacheThread caches a thread fetched at fetched (oldest first) as history, as a page
// of its conversation is (cachePage); being the whole thread, it also says which
// replies went (settleDeletions). The thread's read position moves to Slack's.
func (a *Adapter) cacheThread(ctx context.Context, w *workspace, channel, ts string, replies []slackgo.Message, lastRead string, fetched time.Time) ([]domain.Message, []domain.Reaction) {
	newestFirst := slices.Clone(replies)
	slices.Reverse(newestFirst) // as cachePage takes a page: as history answers it
	msgs, reactions := a.cachePage(ctx, w, channel, newestFirst, fetched)
	a.settleDeletions(ctx, w, channel, ts, replies, fetched)
	a.threadReadTo(ctx, w.creds.Team, channel, ts, lastRead)
	a.recount(ctx, roomID(w.creds.Team, channel))
	return msgs, reactions
}

// settleDeletions marks deleted each cached reply of the thread that Slack's reading
// of it lacks: one deleted while kith was not listening. Only a reply kith held before
// the reading was asked for is judged — one cached since (live, sent, or by another
// reading) may be newer than the reading, which could not have had it.
func (a *Adapter) settleDeletions(ctx context.Context, w *workspace, channel, ts string, replies []slackgo.Message, fetched time.Time) {
	if a.cache == nil {
		return
	}
	room, root := roomID(w.creds.Team, channel), messageID(w.creds.Team, channel, ts)
	has := map[domain.EventID]bool{}
	for i := range replies {
		has[messageID(w.creds.Team, channel, replies[i].Timestamp)] = true
	}
	cached, err := a.cache.ThreadMessages(ctx, room, root, threadPage*threadPages)
	if err != nil {
		a.log.Warn("read a thread's cached replies failed", "room", room, "err", err)
		return
	}
	for i := range cached {
		m := cached[i]
		if m.ID == root || m.Redacted || has[m.ID] || !a.heldBefore(m.ID, fetched) {
			continue
		}
		a.markDeleted(ctx, room, m.ID, "", fetched)
	}
}

// keepReplies are how long when a reply was cached is remembered: longer than a
// reading of a thread can take (threadPages calls, each waiting out a rate limit).
const keepReplies = 2 * threadPages * longestWait

// heardReplies notes when each thread reply in msgs was cached, forgetting those
// cached longer ago than keepReplies.
func (a *Adapter) heardReplies(msgs []domain.Message) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, at := range a.repliesCached {
		if now.Sub(at) > keepReplies {
			delete(a.repliesCached, id)
		}
	}
	for i := range msgs {
		if msgs[i].ThreadRoot != "" {
			a.repliesCached[msgs[i].ID] = now
		}
	}
}

// heldBefore reports whether a cached reply was cached before at: one cached in an
// earlier run, or longer ago than keepReplies, was.
func (a *Adapter) heldBefore(id domain.EventID, at time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cached, noted := a.repliesCached[id]
	return !noted || cached.Before(at)
}

// repliesBehind reports whether m starts a thread whose newest reply the cache lacks.
func (a *Adapter) repliesBehind(ctx context.Context, team, channel string, m *slackgo.Msg) bool {
	if a.cache == nil || m.ReplyCount == 0 || m.LatestReply == "" || m.ThreadTimestamp != m.Timestamp {
		return false
	}
	latest, err := a.cache.LatestInThread(ctx, roomID(team, channel), messageID(team, channel, m.Timestamp))
	if err != nil {
		return false
	}
	_, cached, ok := cutLast(domain.ParseID(string(latest)).Native)
	return !ok || tsTime(m.LatestReply).After(tsTime(cached))
}

// wantThread queues a thread to be read in the background, up to its newest reply
// latest, unless it was read (or queued) up to that reply already this run: a reply
// also sent to the channel is not cached as the thread's, so the cache alone would
// ask forever. The reader runs while there is any.
func (a *Adapter) wantThread(w *workspace, channel, ts, latest string) {
	root := messageID(w.creds.Team, channel, ts)
	a.mu.Lock()
	if a.threadsAsked[root] == latest {
		a.mu.Unlock()
		return
	}
	a.threadsAsked[root] = latest
	a.threadQueue = append(a.threadQueue, threadWant{w: w, channel: channel, ts: ts})
	start := !a.readingThreads
	a.readingThreads = true
	ctx := a.run
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if start {
		go a.readWanted(ctx)
	}
}

// wantRestOf queues the thread a live reply is in to be read, the first time one of
// its replies arrives this run: what it said before kith listened is not cached.
func (a *Adapter) wantRestOf(w *workspace, channel string, m *slackgo.Msg) {
	if m.ThreadTimestamp == "" || m.ThreadTimestamp == m.Timestamp {
		return
	}
	a.mu.Lock()
	_, asked := a.threadsAsked[messageID(w.creds.Team, channel, m.ThreadTimestamp)]
	a.mu.Unlock()
	if !asked {
		a.wantThread(w, channel, m.ThreadTimestamp, m.Timestamp)
	}
}

// threadWant is a thread queued to be read.
type threadWant struct {
	w           *workspace
	channel, ts string
}

// readWanted reads the queued threads, oldest queued first, until none is left. A
// thread of a workspace let go since is skipped; one that fails is asked for again
// when a page next names it.
func (a *Adapter) readWanted(ctx context.Context) {
	for {
		a.mu.Lock()
		if len(a.threadQueue) == 0 {
			a.readingThreads = false
			a.mu.Unlock()
			return
		}
		want := a.threadQueue[0]
		a.threadQueue = a.threadQueue[1:]
		a.mu.Unlock()
		select {
		case <-want.w.done:
			continue
		default:
		}
		if _, _, err := a.readThread(ctx, want.w, want.channel, want.ts, threadPage); err != nil {
			a.log.Warn("read a thread failed", "account", want.w.account.Name, "channel", want.channel, "err", err)
			a.mu.Lock()
			delete(a.threadsAsked, messageID(want.w.creds.Team, want.channel, want.ts))
			a.mu.Unlock()
		}
	}
}

// threadMarked is a thread read on some client of yours, as the websocket says it;
// slackgo knows no such event, so it arrives raw.
type threadMarked struct {
	Type         string `json:"type"`
	Subscription struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		LastRead string `json:"last_read"`
	} `json:"subscription"`
}

// onUnmapped handles a live event slackgo does not know: a thread read elsewhere moves
// its read position here.
func (a *Adapter) onUnmapped(ctx context.Context, w *workspace, raw json.RawMessage) {
	var e threadMarked
	if json.Unmarshal(raw, &e) != nil || e.Type != "thread_marked" || a.cache == nil {
		return
	}
	s := e.Subscription
	if s.Channel == "" || s.ThreadTS == "" {
		return
	}
	a.threadReadTo(ctx, w.creds.Team, s.Channel, s.ThreadTS, s.LastRead)
	a.recount(ctx, roomID(w.creds.Team, s.Channel))
}

// threadReadTo moves a thread's read position to Slack's, lastRead (none when empty);
// an older one leaves it.
func (a *Adapter) threadReadTo(ctx context.Context, team, channel, thread, lastRead string) {
	read := tsTime(lastRead)
	if a.cache == nil || read.IsZero() {
		return
	}
	room := roomID(team, channel)
	err := a.cache.SaveThreadRead(ctx, room, messageID(team, channel, thread), messageID(team, channel, lastRead), read.UnixMilli())
	if err != nil {
		a.log.Warn("save a thread's read position failed", "room", room, "err", err)
	}
}

// MarkThreadRead moves a thread's read position to eventID, here.
func (a *Adapter) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, _ bool) error {
	if a.cache == nil {
		return nil
	}
	ts, ok, err := a.cache.MessageTS(ctx, roomID, eventID)
	if err != nil {
		return fmt.Errorf("slack: when %s was sent: %w", eventID, err)
	}
	if !ok {
		return nil
	}
	if err := a.cache.SaveThreadRead(ctx, roomID, root, eventID, ts); err != nil {
		return fmt.Errorf("slack: mark a thread of %s read: %w", roomID, err)
	}
	a.recount(ctx, roomID)
	return nil
}

// ThreadParticipant reports whether we sent a thread's root or any reply in it.
func (a *Adapter) ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool {
	if a.cache == nil || root == "" {
		return false
	}
	spoke, err := a.cache.SpokeInThread(ctx, roomID, root, a.Me())
	if err != nil {
		a.log.Warn("read thread participation failed", "room", roomID, "err", err)
		return false
	}
	return spoke
}

// followed is the unread threads of a room that you follow: those you spoke in, or
// that name you (which, on Slack, follows a thread for good).
func (a *Adapter) followed(ctx context.Context, room domain.RoomID, threads []domain.ThreadUnread) []domain.ThreadUnread {
	return slices.DeleteFunc(threads, func(t domain.ThreadUnread) bool {
		if t.Mentions > 0 || a.ThreadParticipant(ctx, room, t.Root) {
			return false
		}
		named, err := a.cache.NamedInThread(ctx, room, t.Root)
		if err != nil {
			a.log.Warn("read mentions in a thread failed", "room", room, "err", err)
		}
		return !named
	})
}

// readThreadsTo moves a room's thread floor to at: every thread read up to it.
func (a *Adapter) readThreadsTo(ctx context.Context, room domain.RoomID, event domain.EventID, at time.Time) {
	if err := a.cache.SaveThreadRead(ctx, room, "", event, at.UnixMilli()); err != nil {
		a.log.Warn("move a thread floor failed", "room", room, "err", err)
	}
}
