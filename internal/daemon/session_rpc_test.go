package daemon_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
)

// The daemon owns its session: an RPC that resumes or logs out would replace the
// client its sync loop, refresher and handlers share, unlocked. Logins and SignOut go
// through each network's own LoginLeader, under that network's lock or login
// generation. A new session RPC needs the same care, and a place in allowed.
func TestTheSocketCannotReplaceTheSession(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{"LoginNetworks": true, "SignOut": true}
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
