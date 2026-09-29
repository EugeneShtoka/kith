package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// verificationKinds maps the domain enum to the wire enum, both directions.
var verificationKinds = map[domain.VerificationKind]v1.VerificationKind{
	domain.VerificationRequested: v1.VerificationKind_VERIFICATION_KIND_REQUESTED,
	domain.VerificationSAS:       v1.VerificationKind_VERIFICATION_KIND_SAS,
	domain.VerificationDone:      v1.VerificationKind_VERIFICATION_KIND_DONE,
	domain.VerificationCanceled:  v1.VerificationKind_VERIFICATION_KIND_CANCELED,
	domain.VerificationRestored:  v1.VerificationKind_VERIFICATION_KIND_RESTORED,
}

// VerificationKindToProto converts a flow stage; unknown kinds become UNSPECIFIED.
func VerificationKindToProto(k domain.VerificationKind) v1.VerificationKind {
	return verificationKinds[k]
}

// ProtoToVerificationKind converts a flow stage back; ok is false for anything
// unrecognized. It must not fall back to zero, which is VerificationRequested and
// would prompt the user to accept a flow nothing started.
func ProtoToVerificationKind(pb v1.VerificationKind) (domain.VerificationKind, bool) {
	for domainKind, wireKind := range verificationKinds {
		if wireKind == pb {
			return domainKind, true
		}
	}
	return 0, false
}

// VerificationToProto converts one verification step.
func VerificationToProto(v domain.Verification) *v1.Verification {
	return &v1.Verification{
		Kind:     VerificationKindToProto(v.Kind),
		TxnId:    v.TxnID,
		From:     v.From,
		Device:   v.Device,
		Emojis:   sasEmojisToProto(v.Emojis),
		Decimals: decimalsToProto(v.Decimals),
		Reason:   v.Reason,
	}
}

// ProtoToVerification converts one step back; ok is false for nil or an unknown
// kind, and the caller drops the step.
func ProtoToVerification(pb *v1.Verification) (domain.Verification, bool) {
	if pb == nil {
		return domain.Verification{}, false
	}
	kind, ok := ProtoToVerificationKind(pb.GetKind())
	if !ok {
		return domain.Verification{}, false
	}
	return domain.Verification{
		Kind:     kind,
		TxnID:    pb.GetTxnId(),
		From:     pb.GetFrom(),
		Device:   pb.GetDevice(),
		Emojis:   protoToSASEmojis(pb.GetEmojis()),
		Decimals: protoToDecimals(pb.GetDecimals()),
		Reason:   pb.GetReason(),
	}, true
}

// SAS emoji and decimals keep their order: order is the comparison.
func sasEmojisToProto(es []domain.SASEmoji) []*v1.SASEmoji {
	return mapSlice(es, func(e domain.SASEmoji) *v1.SASEmoji {
		return &v1.SASEmoji{Glyph: e.Glyph, Name: e.Name}
	})
}

func protoToSASEmojis(pb []*v1.SASEmoji) []domain.SASEmoji {
	return mapSlice(pb, func(e *v1.SASEmoji) domain.SASEmoji {
		return domain.SASEmoji{Glyph: e.GetGlyph(), Name: e.GetName()}
	})
}

func decimalsToProto(ds []int) []int64 {
	return mapSlice(ds, func(d int) int64 { return int64(d) })
}

func protoToDecimals(pb []int64) []int {
	return mapSlice(pb, func(d int64) int { return int(d) })
}
