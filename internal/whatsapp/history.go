package whatsapp

import (
	"context"
	"math"
	"slices"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// onHistory caches the history chunks sent to a newly linked device. Nothing is
// streamed or notified; read positions come from each chat's unread count.
func (a *Adapter) onHistory(ctx context.Context, account Account, client *whatsmeow.Client, e *events.HistorySync) {
	if a.cache == nil || e.Data == nil {
		return
	}
	for _, conv := range e.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		byRoom := map[domain.RoomID][]arrived{}
		for _, hm := range conv.GetMessages() {
			evt, err := client.ParseWebMessage(chat, hm.GetMessage())
			if err != nil {
				continue
			}
			if in, ok := a.convert(ctx, account, client, evt); ok {
				// msgOrderID increases with the conversation, newest highest (WhatsApp
				// Web resumes chunks by it).
				in.msg.Seq = int64(min(hm.GetMsgOrderID(), math.MaxInt64))
				byRoom[in.msg.RoomID] = append(byRoom[in.msg.RoomID], in)
			}
		}
		for room, msgs := range byRoom {
			a.recordHistory(ctx, account, client, room, msgs, conv.GetName(), int(conv.GetUnreadCount()))
		}
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}

// recordHistory caches one conversation's messages after its room, in one write under
// listing. name may be ""; unread counts the newest unread messages.
func (a *Adapter) recordHistory(ctx context.Context, account Account, client *whatsmeow.Client, room domain.RoomID, msgs []arrived, name string, unread int) {
	slices.SortStableFunc(msgs, func(x, y arrived) int { return domain.CompareMessages(x.msg, y.msg) })
	batch := make([]domain.Message, len(msgs))
	for i := range msgs {
		batch[i] = msgs[i].msg
	}
	a.listing.Lock()
	a.heard[room] = time.Now()
	a.ensureRoom(ctx, account, client, msgs[0], name)
	err := a.cache.SaveMessages(ctx, room, batch)
	a.listing.Unlock()
	if err != nil {
		a.log.Warn("cache history failed", "account", account.Name, "room", room, "count", len(batch), "err", err)
		return
	}
	for i := range msgs {
		a.keepSource(ctx, msgs[i].msg, msgs[i].source)
		a.keepQuote(ctx, room, msgs[i].quoted)
	}
	a.placeRead(ctx, room, readFromHistory(batch, unread))
	if a.onChanged != nil {
		a.onChanged(room)
	}
	a.recount(ctx, room)
}

// readFromHistory is the read position given the unread count: just after the last
// read message, or just before all when all are unread. msgs: oldest first, non-empty.
func readFromHistory(msgs []domain.Message, unread int) time.Time {
	switch {
	case unread <= 0:
		return msgs[len(msgs)-1].Timestamp
	case unread >= len(msgs):
		return msgs[0].Timestamp.Add(-time.Millisecond)
	}
	return msgs[len(msgs)-unread-1].Timestamp
}
