package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// VerdictsToProto converts tag verdicts. The instant is milliseconds, matching the
// at_ms of the account data it mirrors.
func VerdictsToProto(verdicts []domain.TagVerdict) []*v1.TagVerdict {
	return mapSlice(verdicts, VerdictToProto)
}

// VerdictToProto converts one verdict.
func VerdictToProto(v domain.TagVerdict) *v1.TagVerdict {
	return &v1.TagVerdict{
		RoomId:   string(v.Room),
		Tag:      v.Tag,
		Rule:     int32(v.Rule), // #nosec G115 -- a small enum
		Filter:   v.Filter,
		AtUnixMs: unixMillis(v.At),
		Released: v.Released,
	}
}

// ProtoToVerdict converts one verdict back; nil reads as no move, the safe direction.
func ProtoToVerdict(pb *v1.TagVerdict) domain.TagVerdict {
	if pb == nil {
		return domain.TagVerdict{}
	}
	return domain.TagVerdict{
		Room:     domain.RoomID(pb.GetRoomId()),
		Tag:      pb.GetTag(),
		Rule:     domain.MoveRule(pb.GetRule()),
		Filter:   pb.GetFilter(),
		Released: pb.GetReleased(),
		At:       fromUnixMillis(pb.GetAtUnixMs()),
	}
}

// ProtoToVerdicts converts a list back.
func ProtoToVerdicts(pbs []*v1.TagVerdict) []domain.TagVerdict {
	return mapSlice(pbs, ProtoToVerdict)
}

// CandidatesToProto converts rescan candidates.
func CandidatesToProto(cs []domain.MoveCandidate) []*v1.MoveCandidate {
	return mapSlice(cs, func(c domain.MoveCandidate) *v1.MoveCandidate {
		return &v1.MoveCandidate{Verdict: VerdictToProto(c.Verdict), Sample: MessageToProto(c.Sample)}
	})
}

// ProtoToCandidate converts one candidate back.
func ProtoToCandidate(pb *v1.MoveCandidate) domain.MoveCandidate {
	return domain.MoveCandidate{Verdict: ProtoToVerdict(pb.GetVerdict()), Sample: ProtoToMessage(pb.GetSample())}
}

// ProtoToCandidates converts a list back.
func ProtoToCandidates(pbs []*v1.MoveCandidate) []domain.MoveCandidate {
	return mapSlice(pbs, ProtoToCandidate)
}
