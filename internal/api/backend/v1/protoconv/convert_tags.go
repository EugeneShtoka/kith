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

// PersonVerdictsToProto converts person verdicts.
func PersonVerdictsToProto(verdicts []domain.PersonVerdict) []*v1.PersonVerdict {
	return mapSlice(verdicts, PersonVerdictToProto)
}

// PersonVerdictToProto converts one person verdict.
func PersonVerdictToProto(v domain.PersonVerdict) *v1.PersonVerdict {
	return &v1.PersonVerdict{
		Person:   v.Person,
		Tag:      v.Tag,
		Rule:     int32(v.Rule), // #nosec G115 -- a small enum
		Filter:   v.Filter,
		AtUnixMs: unixMillis(v.At),
		Released: v.Released,
	}
}

// ProtoToPersonVerdict converts one person verdict back; nil reads as no mark.
func ProtoToPersonVerdict(pb *v1.PersonVerdict) domain.PersonVerdict {
	if pb == nil {
		return domain.PersonVerdict{}
	}
	return domain.PersonVerdict{
		Person:   pb.GetPerson(),
		Tag:      pb.GetTag(),
		Rule:     domain.MoveRule(pb.GetRule()),
		Filter:   pb.GetFilter(),
		Released: pb.GetReleased(),
		At:       fromUnixMillis(pb.GetAtUnixMs()),
	}
}

// ProtoToPersonVerdicts converts a list back.
func ProtoToPersonVerdicts(pbs []*v1.PersonVerdict) []domain.PersonVerdict {
	return mapSlice(pbs, ProtoToPersonVerdict)
}

// PersonCandidatesToProto converts people rescan candidates.
func PersonCandidatesToProto(cs []domain.PersonCandidate) []*v1.PersonCandidate {
	return mapSlice(cs, func(c domain.PersonCandidate) *v1.PersonCandidate {
		return &v1.PersonCandidate{Verdict: PersonVerdictToProto(c.Verdict), Sample: MessageToProto(c.Sample)}
	})
}

// ProtoToPersonCandidates converts a list back.
func ProtoToPersonCandidates(pbs []*v1.PersonCandidate) []domain.PersonCandidate {
	return mapSlice(pbs, func(pb *v1.PersonCandidate) domain.PersonCandidate {
		return domain.PersonCandidate{Verdict: ProtoToPersonVerdict(pb.GetVerdict()), Sample: ProtoToMessage(pb.GetSample())}
	})
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
