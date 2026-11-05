package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetRoomName writes a room of the request's profile's name into [[display.name]] (an
// empty one removes it), as the TUI's rooms.name does, on the config as it is when it
// writes: the entry is the same on any revision, so a write lost to another writer is
// made again on theirs.
func (s *server) SetRoomName(ctx context.Context, r *req[v1.SetRoomNameRequest]) (*resp[v1.SetRoomNameResponse], error) {
	room := roomID(r.Msg.GetRoomId())
	joined, err := s.Backend.Rooms(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	if !slices.ContainsFunc(s.profileRooms(ctx, joined), func(r domain.Room) bool { return r.ID == room && !r.IsInvite() }) {
		return nil, notInProfile("room", string(room), s.requestProfile(ctx))
	}
	for range fileAttempts {
		snap, err := s.Config.Read()
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		cfg := snap.Config.Clone()
		cfg.Display.Names = config.SetName(cfg.Display.Names, string(room), strings.TrimSpace(r.Msg.GetName()))
		written, err := s.Config.Write(ctx, snap.Revision, cfg, s.Daemon.CheckConfig, s.Reload)
		if errors.Is(err, api.ErrConfigMoved) {
			continue
		}
		if err != nil {
			return nil, configWriteErr(err)
		}
		s.Streams.Configured(written)
		return connect.NewResponse(&v1.SetRoomNameResponse{}), nil
	}
	return nil, configWriteErr(api.ErrConfigMoved)
}
