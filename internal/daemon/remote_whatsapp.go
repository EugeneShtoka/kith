package daemon

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
)

// PairWhatsApp asks the daemon to link a WhatsApp account (api.WhatsAppLink): code
// hears the pairing code as soon as the daemon has it, and the account's ID comes
// back once the phone accepted.
func (r *Remote) PairWhatsApp(ctx context.Context, account string, code func(string) error) (string, error) {
	st, err := r.c.PairWhatsApp(ctx, connect.NewRequest(&v1.PairWhatsAppRequest{Account: account}))
	if err != nil {
		return "", callErr("pair WhatsApp", err)
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		switch event := st.Msg().GetEvent().(type) {
		case *v1.PairWhatsAppResponse_Code:
			if err := code(event.Code); err != nil {
				return "", err
			}
		case *v1.PairWhatsAppResponse_Linked:
			return event.Linked, nil
		}
	}
	if err := st.Err(); err != nil {
		return "", callErr("pair WhatsApp", err)
	}
	return "", errors.New("daemon: pairing ended before the phone answered")
}
