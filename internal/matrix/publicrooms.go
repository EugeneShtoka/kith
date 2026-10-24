package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// publicRoomsPageSize caps one directory page: 50 is small enough to arrive in
// one sync tick on a busy homeserver and large enough to fill a chooser without
// scrolling in from empty.
const publicRoomsPageSize = 50

// PublicRooms lists one page of the Matrix public rooms directory. server is the
// homeserver to search (empty: own); since carries over from a previous call's
// PublicRoomsPage.NextSince to fetch the next page. The chooser filters its own
// rows by substring — the mautrix client's PublicRooms is GET-only and does not
// pass a search body.
func (b *InProc) PublicRooms(ctx context.Context, on domain.RoomOwner, server, since string) (domain.PublicRoomsPage, error) {
	if err := b.mine(on); err != nil {
		return domain.PublicRoomsPage{}, err
	}
	req := &mautrix.ReqPublicRooms{
		Limit:  publicRoomsPageSize,
		Server: server,
		Since:  since,
	}
	resp, err := b.client.PublicRooms(ctx, req)
	if err != nil {
		return domain.PublicRoomsPage{}, fmt.Errorf("matrix: list public rooms: %w", err)
	}
	page := domain.PublicRoomsPage{
		NextSince:     resp.NextBatch,
		TotalEstimate: resp.TotalRoomCountEstimate,
	}
	page.Rooms = make([]domain.PublicRoom, 0, len(resp.Chunk))
	for _, info := range resp.Chunk {
		if info == nil {
			continue
		}
		page.Rooms = append(page.Rooms, domain.PublicRoom{
			ID:            b.ids.room(info.RoomID),
			Alias:         string(info.CanonicalAlias),
			Name:          info.Name,
			Topic:         info.Topic,
			Members:       info.NumJoinedMembers,
			WorldReadable: info.WorldReadable,
			Encrypted:     info.Encryption != "",
		})
	}
	return page, nil
}
