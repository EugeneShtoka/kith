package daemon

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
)

// Identities lists the config's [[display.identity]] people, as the alias editor picks
// among them. The config is the bridge's: every profile is answered the same.
func (s *server) Identities(_ context.Context, _ *req[v1.IdentitiesRequest]) (*resp[v1.IdentitiesResponse], error) {
	snap, err := s.Config.Read()
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	out := &v1.IdentitiesResponse{}
	for _, id := range snap.Config.Display.Identities {
		out.Identities = append(out.Identities, &v1.Identity{Alias: id.Alias, Color: id.Color, Ids: id.IDs, Picture: id.Picture})
	}
	return connect.NewResponse(out), nil
}

// SetIdentity writes id into the identity named alias (config.MergeIdentity), with its
// color and picture, on the config as it is now: the merge is the same on any revision.
func (s *server) SetIdentity(ctx context.Context, r *req[v1.SetIdentityRequest]) (*resp[v1.SetIdentityResponse], error) {
	id, alias := r.Msg.GetId(), strings.TrimSpace(r.Msg.GetAlias())
	if id == "" || alias == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("daemon: a person needs an account and a name"))
	}
	snap, err := s.Config.Read()
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	cfg := snap.Config.Clone()
	cfg.Display = config.MergeIdentity(cfg.Display, alias, id, r.Msg.GetColor())
	for i := range cfg.Display.Identities {
		if cfg.Display.Identities[i].Alias == alias {
			cfg.Display.Identities[i].Picture = r.Msg.GetPicture()
		}
	}
	written, err := s.Config.Write(ctx, snap.Revision, cfg, s.Daemon.CheckConfig, s.Reload)
	_, failed := errors.AsType[unwritable](err)
	switch {
	case failed:
		return nil, rpcErr(err)
	case errors.Is(err, api.ErrConfigMoved):
		cerr := connect.NewError(connect.CodeAborted, err)
		cerr.Meta().Set(sentinelHeader, "config-moved")
		return nil, cerr
	case err != nil:
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.Streams.Configured(written)
	return connect.NewResponse(&v1.SetIdentityResponse{}), nil
}
