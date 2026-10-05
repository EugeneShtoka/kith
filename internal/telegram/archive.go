package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Telegram's Archived folder is a chat's own fact, which every client of the account
// sees: a listing says which folder each chat is in, a move on any client arrives as
// an update, and kith moves one with folders.editPeerFolders. A listing is fetched,
// then written: a chat moved live in between keeps where it was moved to.

// listedArchive is a listing's archived chats fetched then, less those moved live
// since. Caller holds listing.
func (a *Adapter) listedArchive(listed map[domain.RoomID]bool, fetched time.Time) map[domain.RoomID]bool {
	out := make(map[domain.RoomID]bool, len(listed))
	for room, archived := range listed {
		if moved, ok := a.moved[room]; ok && !moved.Before(fetched) {
			continue
		}
		out[room] = archived
	}
	return out
}

// movedLive keeps a chat moved into the Archived folder, or out, on any client.
func (a *Adapter) movedLive(ctx context.Context, room domain.RoomID, archived bool) {
	if a.cache == nil {
		return
	}
	a.listing.Lock()
	a.moved[room] = time.Now()
	err := a.cache.SetArchived(ctx, map[domain.RoomID]bool{room: archived})
	a.listing.Unlock()
	if err != nil {
		a.log.Warn("keep a chat's folder failed", "room", room, "err", err)
		return
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}

// folderPeers hands chats moved between folders to movedLive.
func (a *Adapter) folderPeers(ctx context.Context, self int64, peers []tg.FolderPeer) {
	for _, p := range peers {
		if chat, ok := markedPeer(p.Peer); ok {
			a.movedLive(ctx, roomID(self, chat), p.FolderID == archiveFolder)
		}
	}
}

// SetArchived moves a chat into Telegram's Archived folder, or back to the main list,
// on every client of the account, and here.
func (a *Adapter) SetArchived(ctx context.Context, roomID domain.RoomID, archived bool) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	folder := 0
	if archived {
		folder = archiveFolder
	}
	res, err := ch.conn.client.API().FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{{Peer: ch.peer, FolderID: folder}})
	if err != nil {
		return fmt.Errorf("telegram: move %s to folder %d: %w", roomID, folder, err)
	}
	if ch.conn.live != nil {
		_ = ch.conn.live.manager.Handle(ctx, res)
	}
	a.movedLive(ctx, roomID, archived)
	return nil
}
