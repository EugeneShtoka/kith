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

// Thread replies are not in conversation history (except ones also sent to the
// channel); conversations.replies reads them from the start. History names each
// root's newest reply; a thread whose newest reply the cache lacks is read in the
// background, one at a time.
//
// Slack keeps a read position per thread (last_read on the root; thread_marked live).
// Marking a thread read stays local: slackgo knows no API that moves it. A thread
// counts as unread only if followed (you spoke in it or were named), as Slack badges.
// Reading a room does not read its threads, but marking rooms read does (thread floor).

const (
	// threadPage is the conversations.replies page size.
	threadPage = 200
	// threadPages bounds one thread's reading to threadPage×threadPages replies.
	threadPages = 20
)

// ListThreads is a room's cached threads, newest activity first, with unread counts as
// the room's count has them.
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

// ThreadPage is a whole thread, root first, read from Slack and cached. Slack pages a
// thread only from its start, so Next is always "".
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

// readThread reads a thread in pages of size (threadPage when not positive) and
// caches it.
func (a *Adapter) readThread(ctx context.Context, w *workspace, channel, ts string, size int) ([]domain.Message, []domain.Reaction, error) {
	fetched := time.Now()
	replies, lastRead, err := a.fetchThread(ctx, w, channel, ts, size)
	if err != nil {
		return nil, nil, err
	}
	msgs, reactions := a.cacheThread(ctx, w, channel, ts, replies, lastRead, fetched)
	return msgs, reactions, nil
}

// fetchThread is a thread from Slack, root then replies oldest first, and its
// last_read ("" when absent).
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

// cacheThread caches a thread like a history page (cachePage); being complete, it also
// marks missing replies deleted (settleDeletions) and moves the read position to Slack's.
func (a *Adapter) cacheThread(ctx context.Context, w *workspace, channel, ts string, replies []slackgo.Message, lastRead string, fetched time.Time) ([]domain.Message, []domain.Reaction) {
	newestFirst := slices.Clone(replies)
	slices.Reverse(newestFirst) // as cachePage takes a page: as history answers it
	msgs, reactions := a.cachePage(ctx, w, channel, newestFirst, fetched)
	a.settleDeletions(ctx, w, channel, ts, replies, fetched)
	a.threadReadTo(ctx, w.creds.Team, channel, ts, lastRead)
	a.recount(ctx, roomID(w.creds.Team, channel))
	return msgs, reactions
}

// settleDeletions marks deleted each cached reply missing from Slack's copy. Only
// replies cached before the fetch began are judged; newer ones may postdate it.
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

// keepReplies is how long a reply's cache time is remembered: longer than one thread
// read can take (threadPages calls, each waiting out a rate limit).
const keepReplies = 2 * threadPages * longestWait

// heardReplies records when each reply in msgs was cached, forgetting those older than
// keepReplies.
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

// heldBefore reports whether a reply was cached before at; one from an earlier run or
// older than keepReplies was.
func (a *Adapter) heldBefore(id domain.EventID, at time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cached, noted := a.repliesCached[id]
	return !noted || cached.Before(at)
}

// repliesBehind reports whether m roots a thread whose newest reply the cache lacks.
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

// wantThread queues a thread to be read up to latest unless already read or queued that
// far this run: a reply also sent to the channel is not cached as the thread's, so the
// cache alone would ask forever. Starts the reader if idle.
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

// wantRestOf queues a live reply's thread the first time this run, to fetch replies sent
// before kith listened.
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

type threadWant struct {
	w           *workspace
	channel, ts string
}

// readWanted reads queued threads in order until none is left. Threads of a closed
// workspace are skipped; a failed one is retried when a page next names it.
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

// threadMarked is a thread read on another client of yours; slackgo lacks the event, so
// it arrives raw.
type threadMarked struct {
	Type         string `json:"type"`
	Subscription struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		LastRead string `json:"last_read"`
	} `json:"subscription"`
}

// onUnmapped handles live events slackgo does not know: thread_marked moves the
// thread's read position.
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

// threadReadTo moves a thread's read position forward to lastRead (no-op when empty).
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

// MarkThreadRead moves a thread's read position to eventID, locally.
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

// ThreadParticipant reports whether you sent a thread's root or any reply in it.
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

// followed is a room's unread threads you follow: ones you spoke in or that name you
// (which on Slack follows a thread for good).
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
