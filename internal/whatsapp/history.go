package whatsapp

import (
	"context"
	"slices"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// onHistory caches the history WhatsApp sends a newly linked device, in chunks over
// the first minutes. It is history, not news: nothing is streamed to the clients or
// notified. Each conversation's read position comes from its unread count.
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
				byRoom[in.msg.RoomID] = append(byRoom[in.msg.RoomID], in)
			}
		}
		for room, msgs := range byRoom {
			a.recordHistory(ctx, account, client, conv, room, msgs)
		}
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}

// recordHistory caches one conversation's messages in one write, its room first,
// where no listing's sweep can come between (see saveListing).
func (a *Adapter) recordHistory(ctx context.Context, account Account, client *whatsmeow.Client, conv *waHistorySync.Conversation, room domain.RoomID, msgs []arrived) {
	slices.SortFunc(msgs, func(x, y arrived) int { return x.msg.Timestamp.Compare(y.msg.Timestamp) })
	batch := make([]domain.Message, len(msgs))
	for i := range msgs {
		batch[i] = msgs[i].msg
	}
	a.listing.Lock()
	a.heard[room] = time.Now()
	a.ensureRoom(ctx, account, client, msgs[0], conv.GetName())
	err := a.cache.SaveMessages(ctx, room, batch)
	a.listing.Unlock()
	if err != nil {
		a.log.Warn("cache history failed", "account", account.Name, "room", room, "count", len(batch), "err", err)
		return
	}
	a.placeRead(ctx, room, readFromHistory(batch, int(conv.GetUnreadCount())))
	if a.onChanged != nil {
		a.onChanged(room)
	}
	a.recount(ctx, room)
}

// readFromHistory is where a conversation was read up to, given how many of its
// newest messages WhatsApp says are unread: just after the last read one, or just
// before them all when every one is unread. msgs are oldest first, and not empty.
func readFromHistory(msgs []domain.Message, unread int) time.Time {
	switch {
	case unread <= 0:
		return msgs[len(msgs)-1].Timestamp
	case unread >= len(msgs):
		return msgs[0].Timestamp.Add(-time.Millisecond)
	}
	return msgs[len(msgs)-unread-1].Timestamp
}
