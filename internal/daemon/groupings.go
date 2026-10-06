package daemon

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// A network's groupings (Telegram's folders) are copied into tags when the person asks,
// never synced: the preview says, per tag, what a copy would change among that
// account's rooms, and the copy writes what they chose into the config file, as any
// change to it is written.

// groupingSource is a backend whose networks may have groupings (route.Router).
type groupingSource interface {
	Groupings(ctx context.Context, network domain.Protocol, account string) (domain.RoomOwner, []domain.Grouping, error)
}

var errNoGroupings = errors.New("daemon: this daemon copies no groupings")

// groupingsNow is an account's groupings, the config, and every room's facts as the
// config places them: what a preview and a copy are judged on.
func (s *server) groupingsNow(ctx context.Context, network, account string) (domain.RoomOwner, []domain.Grouping, config.Snapshot, []domain.RoomFacts, error) {
	src, ok := s.Backend.(groupingSource)
	if !ok || s.Config == nil {
		return "", nil, config.Snapshot{}, nil, errNoGroupings
	}
	protocol, ok := domain.ProtocolNamed(network)
	if !ok {
		return "", nil, config.Snapshot{}, nil, fmt.Errorf("daemon: no network is called %q", network)
	}
	owner, groupings, err := src.Groupings(ctx, protocol, account)
	if err != nil {
		return "", nil, config.Snapshot{}, nil, err //nolint:wrapcheck // the network's own words
	}
	snap, err := s.Config.Read()
	if err != nil {
		return "", nil, config.Snapshot{}, nil, err
	}
	rooms, err := s.Backend.Rooms(ctx)
	if err != nil {
		return "", nil, config.Snapshot{}, nil, err //nolint:wrapcheck // the cache's own words
	}
	spaces, err := s.Backend.Spaces(ctx)
	if err != nil {
		return "", nil, config.Snapshot{}, nil, err //nolint:wrapcheck // as above
	}
	places := setup.PlacesOf(snap.Config)
	facts := make([]domain.RoomFacts, 0, len(rooms))
	for i := range rooms {
		facts = append(facts, places.Facts(rooms[i], domain.HoldersOf(rooms[i].ID, spaces)))
	}
	return owner, groupings, snap, facts, nil
}

// PreviewGroupings is what copying each of an account's groupings would change.
func (s *server) PreviewGroupings(ctx context.Context, r *req[v1.PreviewGroupingsRequest]) (*resp[v1.PreviewGroupingsResponse], error) {
	owner, groupings, snap, facts, err := s.groupingsNow(ctx, r.Msg.GetNetwork(), r.Msg.GetAccount())
	if errors.Is(err, errNoGroupings) {
		return nil, connect.NewError(connect.CodeUnimplemented, err)
	}
	if err != nil {
		return nil, rpcErr(err)
	}
	tags, _, err := setup.Tags(snap.Config)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	out := &v1.PreviewGroupingsResponse{}
	for _, d := range domain.DiffGroupings(groupings, tags, facts, owner) {
		out.Groupings = append(out.Groupings, &v1.GroupingDiff{
			Name: d.Grouping.Name, Rooms: ids(d.Grouping.Rooms), Left: d.Grouping.Left,
			Exists: d.Exists, Add: ids(d.Add), Remove: ids(d.Remove),
		})
	}
	return connect.NewResponse(out), nil
}

// ApplyGroupings copies an account's groupings into tags as chosen, judged on the
// groupings and the config as they are now, and writes the config as UpdateConfig does.
func (s *server) ApplyGroupings(ctx context.Context, r *req[v1.ApplyGroupingsRequest]) (*resp[v1.ApplyGroupingsResponse], error) {
	owner, groupings, snap, facts, err := s.groupingsNow(ctx, r.Msg.GetNetwork(), r.Msg.GetAccount())
	if errors.Is(err, errNoGroupings) {
		return nil, connect.NewError(connect.CodeUnimplemented, err)
	}
	if err != nil {
		return nil, rpcErr(err)
	}
	choices := make(map[string]domain.GroupingChoice, len(r.Msg.GetChoices()))
	for name, c := range r.Msg.GetChoices() {
		switch c {
		case v1.GroupingChoice_GROUPING_CHOICE_MERGE_IN:
			choices[name] = domain.MergeIn
		case v1.GroupingChoice_GROUPING_CHOICE_TAKE_NETWORKS:
			choices[name] = domain.TakeNetworks
		case v1.GroupingChoice_GROUPING_CHOICE_UNSPECIFIED:
		}
	}
	cfg, err := setup.ApplyGroupings(snap.Config, owner, groupings, choices, facts)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	written, err := s.Config.Write(ctx, snap.Revision, cfg, s.Daemon.CheckConfig, s.Reload)
	switch {
	case errors.Is(err, api.ErrConfigMoved):
		cerr := connect.NewError(connect.CodeAborted, err)
		cerr.Meta().Set(sentinelHeader, "config-moved")
		return nil, cerr
	case err != nil:
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.Streams.Configured(written)
	return connect.NewResponse(&v1.ApplyGroupingsResponse{}), nil
}

// ids is rooms as their IDs.
func ids(rooms []domain.RoomID) []string {
	out := make([]string, len(rooms))
	for i, r := range rooms {
		out[i] = string(r)
	}
	return out
}
