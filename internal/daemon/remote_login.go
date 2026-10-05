package daemon

import (
	"context"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
)

// LoginMatrix asks the daemon to log its config's Matrix user in (api.MatrixLogin).
// The password travels in the request body over the 0600 socket, never on argv.
func (r *Remote) LoginMatrix(ctx context.Context, password string) (api.MatrixLoggedIn, error) {
	resp, err := call(ctx, "log in to Matrix", r.c.LoginMatrix, &v1.LoginMatrixRequest{Password: password})
	if err != nil {
		return api.MatrixLoggedIn{}, err
	}
	return api.MatrixLoggedIn{UserID: resp.GetUserId(), DeviceID: resp.GetDeviceId(), Started: resp.GetStarted()}, nil
}

// SignInSlack asks the daemon to sign a workspace in (api.SlackSignIn). The token and
// cookie travel in the request body over the 0600 socket, never on argv.
func (r *Remote) SignInSlack(ctx context.Context, account, token, cookie string) (api.SlackSignedIn, error) {
	resp, err := call(ctx, "sign in to Slack", r.c.SignInSlack, &v1.SignInSlackRequest{Account: account, Token: token, Cookie: cookie})
	if err != nil {
		return api.SlackSignedIn{}, err
	}
	return api.SlackSignedIn{Workspace: resp.GetWorkspace(), User: resp.GetUser()}, nil
}

// SendTelegramCode asks the daemon to have Telegram send a login code
// (api.TelegramLogin). The app's hash travels in the request body over the 0600
// socket, never on argv.
func (r *Remote) SendTelegramCode(ctx context.Context, account string, app api.TelegramApp) (api.TelegramCodeSent, error) {
	resp, err := call(ctx, "send a Telegram login code", r.c.SendTelegramCode,
		&v1.SendTelegramCodeRequest{Account: account, ApiId: int32(app.ID), ApiHash: app.Hash}) //nolint:gosec // an api_id is a small positive number
	if err != nil {
		return api.TelegramCodeSent{}, err
	}
	return api.TelegramCodeSent{Via: resp.GetVia()}, nil
}

// SignInTelegram asks the daemon to finish a Telegram login (api.TelegramLogin). The
// code and password travel in the request body over the 0600 socket.
func (r *Remote) SignInTelegram(ctx context.Context, account, code, password string) (api.TelegramSignedIn, error) {
	resp, err := call(ctx, "log in to Telegram", r.c.SignInTelegram,
		&v1.SignInTelegramRequest{Account: account, Code: code, Password: password})
	if err != nil {
		return api.TelegramSignedIn{}, err
	}
	return api.TelegramSignedIn{Name: resp.GetName(), ID: resp.GetId()}, nil
}
