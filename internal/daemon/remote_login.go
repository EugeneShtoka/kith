package daemon

import (
	"context"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
)

// The client's side of logins (api.Logins). Answers, secrets among them, travel in
// the request body over the 0600 socket, never on argv.

var _ api.Logins = (*Remote)(nil)

// LoginNetworks asks the daemon which networks it can log in to.
func (r *Remote) LoginNetworks(ctx context.Context) ([]api.LoginNetwork, error) {
	resp, err := call(ctx, "list the networks to log in to", r.c.LoginNetworks, &v1.LoginNetworksRequest{})
	if err != nil {
		return nil, err
	}
	return loginNetworksFromProto(resp.GetNetworks()), nil
}

// BeginLogin asks the daemon to start a login.
func (r *Remote) BeginLogin(ctx context.Context, network, account string) (api.LoginStep, error) {
	resp, err := call(ctx, "begin a login", r.c.BeginLogin, &v1.BeginLoginRequest{Network: network, Account: account})
	if err != nil {
		return api.LoginStep{}, err
	}
	return loginStepFromProto(resp.GetStep()), nil
}

// AnswerLogin answers a login's step.
func (r *Remote) AnswerLogin(ctx context.Context, login string, values map[string]string) (api.LoginStep, error) {
	resp, err := call(ctx, "log in", r.c.AnswerLogin, &v1.AnswerLoginRequest{Login: login, Values: values})
	if err != nil {
		return api.LoginStep{}, err
	}
	return loginStepFromProto(resp.GetStep()), nil
}

// CancelLogin ends a login.
func (r *Remote) CancelLogin(ctx context.Context, login string) error {
	_, err := call(ctx, "cancel a login", r.c.CancelLogin, &v1.CancelLoginRequest{Login: login})
	return err
}
