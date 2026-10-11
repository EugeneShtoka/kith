package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// gotd keeps polling every channel it holds a pts for, even one the account left, and
// public groups stay readable. A listing settles membership: an unlisted channel's pts
// and rooms are forgotten and its messages dropped; relisting (rejoining) restores it.

// errNotOurs is a message from a channel the account left.
var errNotOurs = errors.New("telegram: a channel the account is not in")

// settleFollowed forgets followed channels the listing omits and restores listed ones.
func (a *Adapter) settleFollowed(ctx context.Context, self int64, elems []dialog) {
	listed := map[int64]bool{}
	for _, e := range elems {
		if p, ok := e.peer.(*tg.InputPeerChannel); ok {
			listed[p.ChannelID] = true
		}
	}
	if a.store == nil {
		return
	}
	followed, err := a.store.channelPositions(ctx, self)
	if err != nil {
		a.log.Warn("read the channels followed failed", "err", err)
		return
	}
	var gone []int64
	for _, p := range followed {
		if !listed[p.channel] {
			gone = append(gone, p.channel)
		}
	}
	a.mu.Lock()
	notIn := a.notMember[self]
	if notIn == nil {
		notIn = map[int64]bool{}
		a.notMember[self] = notIn
	}
	for channel := range listed {
		delete(notIn, channel)
	}
	for _, channel := range gone {
		notIn[channel] = true
	}
	a.mu.Unlock()
	for _, channel := range gone {
		if err := a.store.ForgetChannel(ctx, self, channel); err != nil {
			a.log.Warn("stop following a channel left failed", "channel", channel, "err", err)
		}
		a.forgetChatRooms(ctx, roomID(self, -(channelMark+channel)))
	}
}

// forgetChatRooms drops a chat's room and its topic rooms from the cache.
func (a *Adapter) forgetChatRooms(ctx context.Context, room domain.RoomID) {
	if a.cache == nil {
		return
	}
	gone := []domain.RoomID{room}
	topics := a.cachedTopicRooms(ctx, room)
	for i := range topics {
		gone = append(gone, topics[i].ID)
	}
	if err := a.cache.ForgetRooms(ctx, gone); err != nil {
		a.log.Warn("forget a chat's rooms failed", "room", room, "err", err)
	}
}

// notOurs reports whether room belongs to a channel the account left.
func (a *Adapter) notOurs(self int64, room domain.RoomID) bool {
	native, _, _ := strings.Cut(domain.ParseID(string(room)).Native, topicSep)
	chat, err := strconv.ParseInt(native, 10, 64)
	if err != nil || chat > -channelMark {
		return false // not a channel
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.notMember[self][-chat-channelMark]
}
