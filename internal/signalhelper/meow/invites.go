//go:build signal

package meow

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/web"
	"google.golang.org/protobuf/proto"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
)

// A signal.group invite link carries the group's master key and the link's password.
// The key opens what Signal shows anyone of the group (its join info); the password
// lets the account add itself, or ask the admins to, as Signal's apps do. signalmeow
// does neither, so the requests to Signal's groups service are made here.

// serverParamsBytes is Signal's server public params, a copy of signalmeow's own, which
// it keeps unexported: a join presents the account's profile key credential under them.
//
//go:embed prod-server-public-params.dat
var serverParamsBytes []byte

var serverParams = sync.OnceValues(func() (*libsignalgo.ServerPublicParams, error) {
	return libsignalgo.DeserializeServerPublicParams(serverParamsBytes)
})

var (
	errInviteGone = errors.New("signal: the invite link was reset or turned off")
	errBanned     = errors.New("signal: an admin removed this account from the group, so it cannot join by link")
	errNoGroup    = errors.New("signal: the invite link's group does not exist")
	errGroupEnded = errors.New("signal: the group was ended")
	errGroupBusy  = errors.New("signal: the group kept changing while joining; try again")
)

// joinTries is how often a join is sent: the group changing in between makes Signal
// refuse it (409), and it is made again from the new join info.
const joinTries = 3

// invite is an invite link's contents.
type invite struct {
	key      libsignalgo.GroupMasterKey
	password []byte
	secret   libsignalgo.GroupSecretParams
}

func readInvite(raw []byte) (invite, error) {
	var link signalpb.GroupInviteLink
	if err := proto.Unmarshal(raw, &link); err != nil {
		return invite{}, fmt.Errorf("signal: read the invite link: %w", err)
	}
	v1 := link.GetContentsV1()
	if len(v1.GetGroupMasterKey()) != libsignalgo.GroupMasterKeyLength || len(v1.GetInviteLinkPassword()) == 0 {
		return invite{}, errors.New("signal: the invite link names no group")
	}
	in := invite{key: libsignalgo.GroupMasterKey(v1.GetGroupMasterKey()), password: v1.GetInviteLinkPassword()}
	secret, err := libsignalgo.DeriveGroupSecretParamsFromMasterKey(in.key)
	if err != nil {
		return invite{}, fmt.Errorf("signal: the invite link's key: %w", err)
	}
	in.secret = secret
	return in, nil
}

// GroupByInvite is the group an invite link is to: as a member knows it, else as its
// join info shows it.
func (n *Network) GroupByInvite(ctx context.Context, req *signalv1.GroupByInviteRequest) (*signalv1.GroupByInviteResponse, error) {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return nil, err
	}
	in, err := readInvite(req.GetInvite())
	if err != nil {
		return nil, err
	}
	member, err := a.isMember(ctx, in)
	if err != nil {
		return nil, err
	}
	if member {
		g, gErr := a.memberGroup(ctx, in)
		if gErr != nil {
			return nil, gErr
		}
		return &signalv1.GroupByInviteResponse{Id: g.GetId(), Title: g.GetTitle(), Member: true}, nil
	}
	info, err := a.joinInfo(ctx, in)
	if err != nil {
		return nil, err
	}
	title, err := in.title(info)
	if err != nil {
		return nil, err
	}
	gid, err := in.key.GroupIdentifier()
	if err != nil {
		return nil, fmt.Errorf("signal: the invite link's group: %w", err)
	}
	return &signalv1.GroupByInviteResponse{
		Id: string(types.BytesToGroupIdentifier(gid)), Title: title, Members: info.GetMemberCount(),
		Approval:  info.GetAddFromInviteLink() == signalpb.AccessControl_ADMINISTRATOR,
		Requested: info.GetPendingAdminApproval(),
	}, nil
}

// JoinGroup joins the group an invite link is to, or asks its admins to let the account
// in; one the account is in, or already asked to join, is left as it is.
func (n *Network) JoinGroup(ctx context.Context, req *signalv1.JoinGroupRequest) (*signalv1.JoinGroupResponse, error) {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return nil, err
	}
	in, err := readInvite(req.GetInvite())
	if err != nil {
		return nil, err
	}
	member, err := a.isMember(ctx, in)
	if err != nil {
		return nil, err
	}
	if member {
		g, gErr := a.memberGroup(ctx, in)
		return &signalv1.JoinGroupResponse{Group: g}, gErr
	}
	for try := 1; ; try++ {
		res, changed, joinErr := a.joinOnce(ctx, in)
		if changed && try < joinTries {
			continue
		}
		return res, joinErr
	}
}

// joinOnce sends the join made from the group's join info as it is now; changed is
// whether Signal refused it for the group having changed since.
func (a *account) joinOnce(ctx context.Context, in invite) (res *signalv1.JoinGroupResponse, changed bool, err error) {
	info, err := a.joinInfo(ctx, in)
	if err != nil {
		return nil, false, err
	}
	if info.GetPendingAdminApproval() {
		return &signalv1.JoinGroupResponse{Requested: true}, false, nil
	}
	actions, err := a.joinActions(ctx, in, info)
	if err != nil {
		return nil, false, err
	}
	body, err := proto.Marshal(actions)
	if err != nil {
		return nil, false, fmt.Errorf("signal: write the join: %w", err)
	}
	path := "/v2/groups/?inviteLinkPassword=" + base64.RawURLEncoding.EncodeToString(in.password)
	answered, err := a.askGroups(ctx, in, http.MethodPatch, path, body)
	if err != nil {
		return nil, false, err
	}
	if refused := answered.refusal(); refused != nil {
		return nil, answered.status == http.StatusConflict, refused
	}
	if len(actions.GetAddMembersPendingAdminApproval()) > 0 {
		return &signalv1.JoinGroupResponse{Requested: true}, false, nil
	}
	var signed signalpb.GroupChangeResponse
	if err = proto.Unmarshal(answered.body, &signed); err != nil {
		return nil, false, fmt.Errorf("signal: read Signal's answer to the join: %w", err)
	}
	g, err := a.memberGroup(ctx, in)
	if err != nil {
		return nil, false, err
	}
	a.tellJoined(ctx, in, types.GroupIdentifier(g.GetId()), actions.GetVersion(), signed.GetGroupChange())
	return &signalv1.JoinGroupResponse{Group: g}, false, nil
}

// joinActions is the change that adds the account to the group, or asks to, as the
// link allows: anyone with it joins, or the admins approve who does.
func (a *account) joinActions(ctx context.Context, in invite, info *signalpb.GroupJoinInfo) (*signalpb.GroupChange_Actions, error) {
	params, err := serverParams()
	if err != nil {
		return nil, fmt.Errorf("signal: the server's public params: %w", err)
	}
	credential, err := a.cli.FetchExpiringProfileKeyCredentialById(ctx, a.cli.Store.ACI)
	if err != nil {
		return nil, fmt.Errorf("signal: the account's profile key credential: %w", err)
	}
	presentation, err := in.secret.CreateExpiringProfileKeyCredentialPresentation(params, *credential)
	if err != nil {
		return nil, fmt.Errorf("signal: present the account to the group: %w", err)
	}
	actions := &signalpb.GroupChange_Actions{Version: info.GetVersion() + 1}
	switch info.GetAddFromInviteLink() {
	case signalpb.AccessControl_ANY:
		actions.AddMembers = []*signalpb.GroupChange_Actions_AddMemberAction{{
			Added: &signalpb.Member{Role: signalpb.Member_DEFAULT, Presentation: *presentation},
		}}
	case signalpb.AccessControl_ADMINISTRATOR:
		actions.AddMembersPendingAdminApproval = []*signalpb.GroupChange_Actions_AddMemberPendingAdminApprovalAction{{
			Added: &signalpb.MemberPendingAdminApproval{Presentation: *presentation},
		}}
	default:
		return nil, errInviteGone
	}
	return actions, nil
}

// tellJoined tells the group's members the account joined, with Signal's signed change,
// as Signal's apps do; a member not told learns it when the group next changes.
func (a *account) tellJoined(ctx context.Context, in invite, gid types.GroupIdentifier, revision uint32, change *signalpb.GroupChange) {
	changeBytes, err := proto.Marshal(change)
	if err == nil {
		var g *signalmeow.Group
		g, _, err = a.cli.RetrieveGroupByID(ctx, gid, revision)
		if err == nil {
			_, err = a.cli.SendGroupUpdate(ctx, g, &signalpb.GroupContextV2{
				MasterKey: in.key[:], Revision: &revision, GroupChange: changeBytes,
			}, nil)
		}
	}
	if err != nil {
		a.cli.Log.Warn().Err(err).Msg("the group's members could not be told of the join")
	}
}

// memberGroup is the group as its members know it, its key kept as every group's is.
func (a *account) memberGroup(ctx context.Context, in invite) (*signalv1.Group, error) {
	gid, err := a.cli.StoreMasterKey(ctx, types.SerializedGroupMasterKey(base64.StdEncoding.EncodeToString(in.key[:])))
	if err != nil {
		return nil, fmt.Errorf("signal: keep the group's key: %w", err)
	}
	g, _, err := a.cli.RetrieveGroupByID(ctx, gid, 0)
	if err != nil {
		return nil, fmt.Errorf("signal: fetch the group: %w", err)
	}
	out := &signalv1.Group{Id: string(gid), Title: g.Title, Timer: g.DisappearingMessagesDuration}
	for _, m := range g.Members {
		out.Members = append(out.Members, m.ACI.String())
	}
	return out, nil
}

// isMember is whether the account is in the group: Signal shows the group itself only
// to its members.
func (a *account) isMember(ctx context.Context, in invite) (bool, error) {
	res, err := a.askGroups(ctx, in, http.MethodGet, "/v2/groups", nil)
	if err != nil {
		return false, err
	}
	if res.status == http.StatusForbidden {
		return false, nil
	}
	return true, res.refusal()
}

// joinInfo is what Signal shows of the group to anyone holding the link.
func (a *account) joinInfo(ctx context.Context, in invite) (*signalpb.GroupJoinInfo, error) {
	res, err := a.askGroups(ctx, in, http.MethodGet, "/v2/groups/join/"+base64.RawURLEncoding.EncodeToString(in.password), nil)
	if err != nil {
		return nil, err
	}
	if refused := res.refusal(); refused != nil {
		return nil, refused
	}
	var info signalpb.GroupJoinInfo
	if err := proto.Unmarshal(res.body, &info); err != nil {
		return nil, fmt.Errorf("signal: read the group's join info: %w", err)
	}
	return &info, nil
}

// title is the group's name in its join info, which the link's key opens.
func (in invite) title(info *signalpb.GroupJoinInfo) (string, error) {
	plain, err := in.secret.DecryptBlobWithPadding(info.GetTitle())
	if err != nil {
		return "", fmt.Errorf("signal: open the group's name: %w", err)
	}
	var blob signalpb.GroupAttributeBlob
	if err := proto.Unmarshal(plain, &blob); err != nil {
		return "", fmt.Errorf("signal: read the group's name: %w", err)
	}
	return strings.TrimSpace(blob.GetTitle()), nil
}

// answer is the groups service's answer to one request.
type answer struct {
	status int
	reason string // X-Signal-Forbidden-Reason, on a 403
	body   []byte
}

// askGroups sends one request to Signal's groups service, authorized for the group the
// link's key is to.
func (a *account) askGroups(ctx context.Context, in invite, method, path string, body []byte) (answer, error) {
	auth, err := a.cli.GetAuthorizationForToday(ctx, in.key)
	if err != nil {
		return answer{}, fmt.Errorf("signal: authorize for the group: %w", err)
	}
	resp, err := web.SendHTTPRequest(ctx, web.StorageHostname, method, path, &web.HTTPReqOpt{
		Username: &auth.Username, Password: &auth.Password, ContentType: web.ContentTypeProtobuf, Body: body,
	})
	if err != nil {
		web.CloseBody(resp)
		return answer{}, fmt.Errorf("signal: ask the groups service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return answer{}, fmt.Errorf("signal: read the groups service's answer: %w", err)
	}
	return answer{status: resp.StatusCode, reason: resp.Header.Get("X-Signal-Forbidden-Reason"), body: out}, nil
}

// refusal is what went wrong, by the answer's status; nil for none.
func (r answer) refusal() error {
	switch {
	case r.status == http.StatusOK:
		return nil
	case r.status == http.StatusForbidden && r.reason == "banned":
		return errBanned
	case r.status == http.StatusForbidden:
		return errInviteGone
	case r.status == http.StatusNotFound:
		return errNoGroup
	case r.status == http.StatusLocked:
		return errGroupEnded
	case r.status == http.StatusConflict:
		return errGroupBusy
	}
	return fmt.Errorf("signal: the groups service answered %d", r.status)
}
