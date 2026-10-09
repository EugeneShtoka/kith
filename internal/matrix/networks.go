package matrix

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The network behind a Matrix room a bridge keeps is the bridge's, which the room's
// m.bridge state says (Room.Network). A room never changes network, so each room's is
// read once and kept: a background reading, asked for at start and after a room
// refresh, reads every joined room not read yet. It is one /sync filtered to the
// bridge state alone, not a request per room.

// networkFilter has a sync carry only the rooms' bridge state.
const networkFilter = `{"presence":{"types":[]},"account_data":{"types":[]},` +
	`"room":{"state":{"types":["m.bridge","uk.half-shot.bridge"]},"timeline":{"limit":0},` +
	`"ephemeral":{"types":[]},"account_data":{"types":[]}}}`

// roomNetworks is the asking for and the running of that reading: one at a time.
type roomNetworks struct {
	wanted, reading atomic.Bool
}

// wantNetworks asks for a reading: rooms not read yet may be in the cache.
func (b *InProc) wantNetworks() { b.networks.wanted.Store(true) }

// readNetworksIfWanted starts the reading asked for, unless one runs; ctx is the sync
// loop's, which bounds it, and Stop waits for it.
func (b *InProc) readNetworksIfWanted(ctx context.Context) {
	if b.cache == nil || !b.networks.wanted.Load() || !b.networks.reading.CompareAndSwap(false, true) {
		return
	}
	b.networks.wanted.Store(false)
	b.fetches.wg.Go(func() {
		defer b.networks.reading.Store(false)
		b.readRoomNetworks(ctx)
	})
}

// readRoomNetworks reads and keeps the network of each joined room not read yet; a
// room with no bridge state is Matrix's. A failure is tried again at the next asking.
func (b *InProc) readRoomNetworks(ctx context.Context) {
	unknown, err := b.cache.RoomsOfUnknownNetwork(ctx, domain.MatrixRooms)
	if err != nil || len(unknown) == 0 {
		b.warnIf(ctx, err, "list the rooms of unknown network")
		return
	}
	resp, err := b.client.FullSyncRequest(ctx, mautrix.ReqSync{FilterID: networkFilter, FullState: true})
	if err != nil {
		b.warnIf(ctx, err, "read the rooms' networks")
		b.wantNetworks()
		return
	}
	networks := make(map[domain.RoomID]domain.Protocol, len(unknown))
	for _, room := range unknown {
		joined := resp.Rooms.Join[id.RoomID(room)]
		if joined == nil {
			continue // not in this reading: read again next time
		}
		networks[room] = bridgeNetwork(joined.State.Events)
	}
	if err := b.cache.KeepRoomNetworks(ctx, networks); err != nil {
		b.warnIf(ctx, err, "keep the rooms' networks")
		return
	}
	if len(networks) > 0 && b.onRoomsStale != nil {
		b.onRoomsStale()
	}
}

// bridgeNetwork is the network a room's bridge state names, Matrix for a room no
// bridge keeps.
func bridgeNetwork(state []*event.Event) domain.Protocol {
	for _, evt := range state {
		if evt == nil || (evt.Type != event.StateBridge && evt.Type != event.StateHalfShotBridge) {
			continue
		}
		var info struct {
			Bot      string `json:"bridgebot"`
			Protocol struct {
				ID string `json:"id"`
			} `json:"protocol"`
		}
		if json.Unmarshal(evt.Content.VeryRaw, &info) != nil {
			continue
		}
		if p := domain.BridgedBy(info.Bot, info.Protocol.ID); p != "" {
			return p
		}
	}
	return domain.ProtocolMatrix
}
