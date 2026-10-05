package daemon_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
)

// The daemon owns its session. A socket RPC that resumes or logs out would replace
// the client its sync loop, refresher and handlers share, with no lock between them.
// A login goes through BeginLogin and AnswerLogin, which hand each network its own
// login (api.LoginLeader): a network replaces an account's session itself, under its
// own lock (Matrix only on a Matrix that has none, saving it for the next start
// otherwise; the others by login generation), and nothing else holds that client.
// LoginNetworks only lists. A new RPC named for a session needs the same care, and a
// place in allowed.
func TestTheSocketCannotReplaceTheSession(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{"LoginNetworks": true}
	handler := reflect.TypeFor[backendv1connect.BackendServiceHandler]()
	for method := range handler.Methods() {
		name := method.Name
		for _, banned := range []string{"Login", "Resume", "Logout", "SignIn", "SignOut"} {
			if strings.HasPrefix(name, banned) && !allowed[name] {
				t.Errorf("BackendService serves %s: the session belongs to the daemon, never replaced over the socket", name)
			}
		}
	}
}
