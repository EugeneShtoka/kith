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
