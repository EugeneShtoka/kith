//go:build signal

package meow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
)

// profileFresh is how old a person's cached profile may be when their picture is asked.
const profileFresh = time.Hour

// Picture is a person's or a group's picture as a connected account sees it, its bytes
// only when its version is not the one kept. A person's is their Signal profile's
// (decrypted with the profile key their messages taught the account), else the photo
// the phone's contact has; a group's is its own.
func (n *Network) Picture(ctx context.Context, req *signalv1.PictureRequest) (*signalv1.PictureResponse, error) {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return nil, err
	}
	if req.GetGroup() != "" {
		return a.groupPicture(ctx, req.GetGroup(), req.GetKept())
	}
	aci, err := uuid.Parse(req.GetPerson())
	if err != nil {
		return nil, fmt.Errorf("signal: %q names no person: %w", req.GetPerson(), err)
	}
	return a.personPicture(ctx, aci, req.GetKept())
}

func (a *account) groupPicture(ctx context.Context, id, kept string) (*signalv1.PictureResponse, error) {
	g, _, err := a.cli.RetrieveGroupByID(ctx, types.GroupIdentifier(id), 0)
	if err != nil {
		return nil, fmt.Errorf("signal: read the group %s: %w", id, err)
	}
	if g.AvatarPath == "" || g.AvatarPath == kept {
		return &signalv1.PictureResponse{Version: g.AvatarPath}, nil
	}
	data, err := a.cli.DownloadGroupAvatar(ctx, g.AvatarPath, g.GroupMasterKey)
	if err != nil {
		return nil, fmt.Errorf("signal: download the picture of the group %s: %w", id, err)
	}
	return &signalv1.PictureResponse{Version: g.AvatarPath, Data: data}, nil
}

func (a *account) personPicture(ctx context.Context, aci uuid.UUID, kept string) (*signalv1.PictureResponse, error) {
	r, err := a.cli.ContactByACIWithRefreshAfter(ctx, aci, profileFresh)
	if err != nil {
		return nil, fmt.Errorf("signal: read %s's profile: %w", aci, err)
	}
	if r == nil {
		return nil, errors.New("signal: no such person")
	}
	switch path := r.Profile.AvatarPath; {
	case path != "" && path != "clear": // "clear": the profile removed its picture
		if path == kept {
			return &signalv1.PictureResponse{Version: path}, nil
		}
		data, err := a.cli.DownloadUserAvatar(ctx, path, r.Profile.Key)
		if err != nil {
			return nil, fmt.Errorf("signal: download %s's picture: %w", aci, err)
		}
		return &signalv1.PictureResponse{Version: path, Data: data}, nil
	case r.ContactAvatar.Hash != "" && len(r.ContactAvatar.Image) > 0:
		version := "hash:" + r.ContactAvatar.Hash
		if version == kept {
			return &signalv1.PictureResponse{Version: version}, nil
		}
		return &signalv1.PictureResponse{Version: version, Data: r.ContactAvatar.Image}, nil
	}
	return &signalv1.PictureResponse{}, nil
}
