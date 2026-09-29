package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// SpamToProto converts spam verdicts. The instant is milliseconds, matching the
// at_ms of the account data it mirrors.
func SpamToProto(verdicts []domain.SpamVerdict) []*v1.SpamVerdict {
	return mapSlice(verdicts, func(v domain.SpamVerdict) *v1.SpamVerdict {
		return &v1.SpamVerdict{
			RoomId:   string(v.Room),
			Rule:     int32(v.Rule), // #nosec G115 -- a small enum
			Filter:   v.Filter,
			AtUnixMs: unixMillis(v.At),
			Released: v.Released,
		}
	})
}

// ProtoToSpam converts one verdict back; nil reads as "not spam", the safe direction.
func ProtoToSpam(pb *v1.SpamVerdict) domain.SpamVerdict {
	if pb == nil {
		return domain.SpamVerdict{}
	}
	return domain.SpamVerdict{
		Room:     domain.RoomID(pb.GetRoomId()),
		Rule:     domain.SpamRule(pb.GetRule()),
		Filter:   pb.GetFilter(),
		Released: pb.GetReleased(),
		At:       fromUnixMillis(pb.GetAtUnixMs()),
	}
}

// ProtoToSpamList converts a list back.
func ProtoToSpamList(pbs []*v1.SpamVerdict) []domain.SpamVerdict {
	return mapSlice(pbs, ProtoToSpam)
}
