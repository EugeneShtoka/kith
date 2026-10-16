package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ChatOffersToProto converts the kinds of chat accounts can make.
func ChatOffersToProto(offers []domain.ChatOffer) []*v1.ChatOffer {
	return mapSlice(offers, ChatOfferToProto)
}

// ChatOfferToProto converts one offer.
func ChatOfferToProto(o domain.ChatOffer) *v1.ChatOffer {
	return &v1.ChatOffer{
		On:         string(o.On),
		Account:    o.Account,
		Kind:       v1.ChatKind(o.Kind), // #nosec G115 -- a small enum
		Label:      o.Label,
		Detail:     o.Detail,
		MinMembers: int32(o.MinMembers), // #nosec G115 -- a handful of people
	}
}

// ProtoToChatOffers converts offers back.
func ProtoToChatOffers(pbs []*v1.ChatOffer) []domain.ChatOffer {
	return mapSlice(pbs, ProtoToChatOffer)
}

// ProtoToChatOffer converts one offer back.
func ProtoToChatOffer(pb *v1.ChatOffer) domain.ChatOffer {
	if pb == nil {
		return domain.ChatOffer{}
	}
	return domain.ChatOffer{
		On:         domain.RoomOwner(pb.GetOn()),
		Account:    pb.GetAccount(),
		Kind:       domain.ChatKind(pb.GetKind()),
		Label:      pb.GetLabel(),
		Detail:     pb.GetDetail(),
		MinMembers: int(pb.GetMinMembers()),
	}
}
