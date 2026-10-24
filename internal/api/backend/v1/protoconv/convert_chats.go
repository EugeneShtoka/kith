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

// AccountsToProto converts the accounts calls pick from.
func AccountsToProto(accounts []domain.Account) []*v1.Account {
	return mapSlice(accounts, func(a domain.Account) *v1.Account {
		return &v1.Account{Network: string(a.Network), On: string(a.On), Name: a.Name, Default: a.Default}
	})
}

// ProtoToAccounts converts accounts back.
func ProtoToAccounts(pbs []*v1.Account) []domain.Account {
	return mapSlice(pbs, func(pb *v1.Account) domain.Account {
		return domain.Account{
			Network: domain.Protocol(pb.GetNetwork()), On: domain.RoomOwner(pb.GetOn()),
			Name: pb.GetName(), Default: pb.GetDefault(),
		}
	})
}

// LinkTargetToProto converts the chat a link names.
func LinkTargetToProto(t domain.LinkTarget) *v1.ResolveLinkResponse {
	return &v1.ResolveLinkResponse{Room: RoomToProto(t.Room), Join: t.Join, About: t.About}
}

// ProtoToLinkTarget converts it back.
func ProtoToLinkTarget(pb *v1.ResolveLinkResponse) domain.LinkTarget {
	if pb == nil {
		return domain.LinkTarget{}
	}
	return domain.LinkTarget{Room: ProtoToRoom(pb.GetRoom()), Join: pb.GetJoin(), About: pb.GetAbout()}
}
