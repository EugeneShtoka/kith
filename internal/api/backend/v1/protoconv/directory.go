package protoconv

import (
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// DirectoryToProto converts the directory to the rows it was built from.
func DirectoryToProto(d domain.Directory) *v1.DirectoryResponse {
	return &v1.DirectoryResponse{
		Names: mapSlice(d.Names(), func(n domain.PersonName) *v1.PersonName {
			return &v1.PersonName{Source: n.Source, Id: n.ID, Name: n.Name, Rank: int32(n.Rank)} // #nosec G115 -- a rank is 0..2
		}),
		Links: mapSlice(d.Links(), func(l domain.PersonLink) *v1.PersonLink {
			return &v1.PersonLink{Source: l.Source, Id: l.ID, Other: l.Other}
		}),
	}
}

// ProtoToDirectory builds the directory from the rows sent.
func ProtoToDirectory(pb *v1.DirectoryResponse) domain.Directory {
	return domain.NewDirectory(
		mapSlice(pb.GetNames(), func(n *v1.PersonName) domain.PersonName {
			return domain.PersonName{Source: n.GetSource(), ID: n.GetId(), Name: n.GetName(), Rank: domain.NameRank(n.GetRank())}
		}),
		mapSlice(pb.GetLinks(), func(l *v1.PersonLink) domain.PersonLink {
			return domain.PersonLink{Source: l.GetSource(), ID: l.GetId(), Other: l.GetOther()}
		}),
	)
}
