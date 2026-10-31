package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Filing a room for a client that holds neither the config nor a way to write it (the
// desktop): kithd offers the places in the TUI's order (FilingPlaces) and files the room
// itself (FileRoom), judging the tags on the config as it is when it writes. Every room,
// tag and space named is checked against the request's profile first.

// fileAttempts bounds FileRoom's rounds against a config that moved under it; each round
// lost means another writer (a client, a hand edit) landed first, and the next is judged
// on what it wrote.
const fileAttempts = 8

// FilingPlaces is where a room of the request's profile can be filed.
func (s *server) FilingPlaces(ctx context.Context, r *req[v1.FilingPlacesRequest]) (*resp[v1.FilingPlacesResponse], error) {
	name := s.requestProfile(ctx)
	room := roomID(r.Msg.GetRoomId())
	joined, err := s.Backend.Rooms(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	if !slices.ContainsFunc(s.profileRooms(ctx, joined), func(r domain.Room) bool { return r.ID == room && !r.IsInvite() }) {
		return nil, notInProfile("room", string(room), name)
	}
	cfg, ok := s.Notifications.profileRail(name)
	if !ok {
		return nil, rpcErr(fmt.Errorf("%w: %q", api.ErrUnknownProfile, name))
	}
	rail, err := s.rail(ctx, name)
	if err != nil {
		return nil, rpcErr(err)
	}
	spaces, err := s.Backend.Spaces(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	tags := cfg.places.Tags
	var held []bool
	if at := slices.IndexFunc(rail.Rooms, func(r domain.ServedRoom) bool { return r.Facts.ID == string(room) }); at >= 0 {
		held = rail.Rooms[at].Tags(tags).In
	}
	places, ok := domain.FilingPlaces(room, domain.FilingRail{
		Rows: rail.Rows, Spaces: s.profileSpaces(ctx, spaces), Tags: tags, Held: held, Homes: cfg.places.Order,
		Priority: cfg.places.Order.Priority, SpaceLabels: cfg.places.Order.Labels, Named: r.Msg.GetFilingSpaces(),
	})
	return connect.NewResponse(&v1.FilingPlacesResponse{Places: pc.FilingPlacesToProto(places), Unmatched: !ok}), nil
}

// FileRoom files a room of the request's profile into or out of tags and spaces: the
// tags written into the config first (nothing else happens if that is refused), then the
// network's archive, the releases, and the spaces.
func (s *server) FileRoom(ctx context.Context, r *req[v1.FileRoomRequest]) (*resp[v1.FileRoomResponse], error) {
	id := roomID(r.Msg.GetRoomId())
	filed, err := s.fileInTags(ctx, s.requestProfile(ctx), id, r.Msg)
	if err != nil {
		return nil, err
	}
	out := &v1.FileRoomResponse{}
	if filed.archive {
		if err := s.Backend.SetRoomArchived(ctx, id, filed.archived); err != nil {
			out.ArchiveFailed = err.Error()
		}
	}
	out.ReleaseFailed = s.release(ctx, id, filed.leaving)
	out.Spaces = s.fileInSpaces(ctx, id, r.Msg.GetSpaces())
	return connect.NewResponse(out), nil
}

// notInProfile refuses a room, tag or space the request's profile does not hold.
func notInProfile(what, name, profile string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon: the profile %s holds no %s %s", profile, what, name))
}

// tagsFiled is what filing a room into tags came to: the tags it left, as configured,
// and whether the network is asked to archive the room (archived) or unarchive it.
type tagsFiled struct {
	leaving  []string
	archive  bool
	archived bool
}

// fileInTags writes the tag filings into the config, each round judged on the file and
// the profile's rooms as they are then, until one lands before any other writer's. Each
// round refuses a room or space the profile does not hold (now) before anything is
// written.
func (s *server) fileInTags(ctx context.Context, profile string, id domain.RoomID, asked *v1.FileRoomRequest) (tagsFiled, error) {
	for range fileAttempts {
		var snap config.Snapshot
		if len(asked.GetTags()) > 0 {
			var err error
			if snap, err = s.Config.Read(); err != nil {
				return tagsFiled{}, connect.NewError(connect.CodeFailedPrecondition, err)
			}
		}
		in, room, err := s.filingScope(ctx, profile, id, asked.GetSpaces())
		if err != nil || len(asked.GetTags()) == 0 {
			return tagsFiled{}, err
		}
		every, err := s.allRooms(ctx)
		if err != nil {
			return tagsFiled{}, rpcErr(err)
		}
		cfg, filed, err := s.filedConfig(snap.Config, profile, room, in, every, asked.GetTags())
		if err != nil || filed == nil {
			return tagsFiled{}, err // nil: every tag holds the room as asked already
		}
		written, err := s.Config.Write(ctx, snap.Revision, cfg, s.Daemon.CheckConfig, s.Reload)
		if errors.Is(err, api.ErrConfigMoved) {
			continue
		}
		if err != nil {
			return tagsFiled{}, configWriteErr(err)
		}
		s.Streams.Configured(written)
		return *filed, nil
	}
	return tagsFiled{}, configWriteErr(api.ErrConfigMoved)
}

// filingScope is what the profile holds now, and the room id among it; a room or a
// space the profile does not hold is refused.
func (s *server) filingScope(ctx context.Context, profile string, id domain.RoomID, spaces []*v1.SpaceFiling) (railRead, domain.Room, error) {
	in, err := s.railInputs(ctx)
	if err != nil {
		return railRead{}, domain.Room{}, rpcErr(err)
	}
	at := slices.IndexFunc(in.rooms, func(r domain.Room) bool { return r.ID == id && !r.IsInvite() })
	if at < 0 {
		return railRead{}, domain.Room{}, notInProfile("room", string(id), profile)
	}
	for _, f := range spaces {
		if !slices.ContainsFunc(in.spaces, func(sp domain.Space) bool { return string(sp.ID) == f.GetSpaceId() }) {
			return railRead{}, domain.Room{}, notInProfile("space", f.GetSpaceId(), profile)
		}
	}
	return in, in.rooms[at], nil
}

// configWriteErr is a refused config write as UpdateConfig answers it: the daemon's
// failure, the file moved meanwhile (ABORTED), or a config it would not run with.
func configWriteErr(err error) error {
	if _, failed := errors.AsType[unwritable](err); failed {
		return rpcErr(err)
	}
	if errors.Is(err, api.ErrConfigMoved) {
		cerr := connect.NewError(connect.CodeAborted, err)
		cerr.Meta().Set(sentinelHeader, "config-moved")
		return cerr
	}
	return connect.NewError(connect.CodeInvalidArgument, err)
}

// kithdRooms is every room and space kithd holds, whatever the profile: an entry naming
// the room filed and rooms of other profiles too must keep those.
type kithdRooms struct {
	rooms  []domain.Room
	spaces []domain.Space
}

func (s *server) allRooms(ctx context.Context) (kithdRooms, error) {
	joined, err := s.Backend.Rooms(ctx)
	if err != nil {
		return kithdRooms{}, fmt.Errorf("read the rooms: %w", err)
	}
	invites, err := s.Backend.CachedInvites(ctx)
	if err != nil {
		return kithdRooms{}, fmt.Errorf("read the invitations: %w", err)
	}
	spaces, err := s.Backend.Spaces(ctx)
	if err != nil {
		return kithdRooms{}, fmt.Errorf("read the spaces: %w", err)
	}
	return kithdRooms{rooms: slices.Concat(joined, invites), spaces: spaces}, nil
}

// filedConfig is cfg with room filed as asked, judged by the profile's view of cfg on the
// rooms of in, and what that asks besides; nil when every tag holds it as asked already.
// A tag the profile does not have refuses the whole filing.
func (s *server) filedConfig(cfg config.Config, profile string, room domain.Room, in railRead, every kithdRooms, asked []*v1.TagFiling) (config.Config, *tagsFiled, error) {
	view := setup.ProfileConfig(cfg, profile)
	tags, _, err := setup.Tags(view)
	if err != nil {
		return config.Config{}, nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	archives, err := setup.Archives(view, tags)
	if err != nil {
		return config.Config{}, nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	source, err := setup.UnreadSource(cfg.Display.Unread)
	if err != nil {
		return config.Config{}, nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	places, rooms := s.railRooms(setup.PlacesOf(view), source != config.UnreadNotifications, in)
	at := slices.IndexFunc(rooms, func(r domain.RailRoom) bool { return r.ID == room.ID })
	facts, raw := rooms[at].Facts, rooms[at].State
	filings, err := changing(tags, tags.Judge(facts, raw).In, asked, profile)
	if err != nil || len(filings) == 0 {
		return cfg, nil, err
	}
	filed := &tagsFiled{}
	network := domain.NetworkOf(string(room.ID))
	if f, followed, ok := domain.ArchiveFiling(tags, setup.Mirrored(view, tags), archives, network, filings); ok {
		filed.archive, filed.archived = true, f.In
		if followed && room.Archived != f.In {
			// Where kith follows the archive the network decides: the lists only lose
			// what contradicts the room as the network will hold it.
			room.Archived = f.In
			facts = places.Facts(room, domain.HoldersOf(room.ID, in.spaces))
		}
	}
	others := make([]domain.RoomFacts, len(every.rooms))
	for i := range every.rooms {
		others[i] = places.Facts(every.rooms[i], domain.HoldersOf(every.rooms[i].ID, every.spaces))
	}
	silenced := tags.Judge(facts, raw).Silenced
	state := func(i int) domain.RoomState { return tags.StateIn(i, raw, silenced) }
	for _, f := range filings {
		if !f.In {
			filed.leaving = append(filed.leaving, tags.At(f.Tag).Name)
		}
	}
	return setup.FileTags(cfg, tags, facts, state, filings, others), filed, nil
}

// changing is the filings asked whose tag does not hold the room as asked (held), in the
// order asked, a tag asked twice as asked last; a tag the profile does not have refuses
// them all.
func changing(tags domain.TagSet, held []bool, asked []*v1.TagFiling, profile string) ([]domain.TagFiling, error) {
	wanted := make(map[int]bool, len(asked))
	var order []int
	for _, a := range asked {
		i, ok := tags.Index(a.GetTag())
		if !ok {
			return nil, notInProfile("tag", a.GetTag(), profile)
		}
		if _, seen := wanted[i]; !seen {
			order = append(order, i)
		}
		wanted[i] = a.GetIn()
	}
	var filings []domain.TagFiling
	for _, i := range order {
		if held[i] != wanted[i] {
			filings = append(filings, domain.TagFiling{Tag: i, In: wanted[i]})
		}
	}
	return filings, nil
}

// release records the release of each tag of leaving a filter moved room into, so no
// filter moves it back; the excluded entry holds it out meanwhile. It answers why one
// was not recorded, "" when none failed.
func (s *server) release(ctx context.Context, room domain.RoomID, leaving []string) string {
	if len(leaving) == 0 {
		return ""
	}
	verdicts, err := s.Backend.TagVerdicts(ctx)
	if err != nil {
		return err.Error()
	}
	var failed []string
	released := false
	for _, tag := range leaving {
		moved := slices.ContainsFunc(verdicts, func(v domain.TagVerdict) bool {
			return v.Room == room && v.In() && domain.SameTag(v.Tag, tag)
		})
		if !moved {
			continue
		}
		if err := s.Backend.MarkTag(ctx, domain.TagVerdict{Room: room, Tag: tag, Released: true, At: time.Now()}); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		released = true
	}
	if released {
		s.Notifications.TagMarked(ctx)
	}
	return strings.Join(failed, "; ")
}

// fileInSpaces files room into or out of each space asked, in order, a refusal not
// stopping the rest, and reads the spaces again once any landed.
func (s *server) fileInSpaces(ctx context.Context, room domain.RoomID, asked []*v1.SpaceFiling) []*v1.SpaceFiled {
	out := make([]*v1.SpaceFiled, 0, len(asked))
	landed := false
	for _, f := range asked {
		space := domain.SpaceID(f.GetSpaceId())
		var err error
		if f.GetIn() {
			err = s.Backend.AddToSpace(ctx, space, room)
		} else {
			err = s.Backend.RemoveFromSpace(ctx, space, room)
		}
		filed := &v1.SpaceFiled{SpaceId: string(space), Outcome: v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_FILED}
		switch {
		case errors.Is(err, api.ErrNoSpaceParent):
			filed.Outcome = v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_PARENT_KEPT
		case err != nil:
			filed.Outcome, filed.Error = v1.SpaceFiledOutcome_SPACE_FILED_OUTCOME_REFUSED, err.Error()
		}
		landed = landed || err == nil || errors.Is(err, api.ErrNoSpaceParent)
		out = append(out, filed)
	}
	if landed {
		if _, err := s.Backend.RefreshSpaces(ctx); err == nil {
			s.Notifications.rails.bump() // the cache's spaces were written again
		}
	}
	return out
}
