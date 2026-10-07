package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A channel the account left, here or on another device, may still be followed: the
// updates library keeps asking after every channel whose position it holds, and a
// public group's history stays readable to anyone. Its messages are not the account's
// to show. So a listing, which names every chat the account is in, settles it: a
// followed channel it does not name is forgotten (its position, so it is not followed
// on connecting again, and its rooms), and whatever is still heard from it is dropped.
// A listing that names it again (joined anew) takes it back.

// errNotOurs is a message of a channel the account is no longer in: dropped.
var errNotOurs = errors.New("telegram: a channel the account is not in")

// settleFollowed forgets the channels the account follows that a listing of its
// dialogs does not name, and takes back those it names.
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

// forgetChatRooms drops a chat's room, and its topics', from the cache.
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

// notOurs reports whether a room is of a channel the account is no longer in.
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
