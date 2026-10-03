package daemon_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
)

// The daemon owns its session. A socket RPC that resumes or logs out would replace
// the client its sync loop, refresher and handlers share, with no lock between them.
// LoginMatrix alone may log in to Matrix: it hands a session only to a Matrix that has
// none, and saves it for the next start otherwise (cmd/kithd's matrix_test.go holds it
// to that). SignInSlack replaces one workspace's connection whole, under the adapter's
// lock, and nothing holds a workspace's client but that connection. A new sign-in RPC
// needs the same care, and a place in allowed.
func TestTheSocketCannotReplaceTheSession(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{"LoginMatrix": true, "SignInSlack": true}
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
