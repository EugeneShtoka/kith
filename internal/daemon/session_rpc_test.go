package daemon_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
)

// The daemon owns its session. A socket RPC that logs in or resumes would replace
// the client its sync loop, refresher and handlers share, with no lock between them.
func TestTheSocketCannotReplaceTheSession(t *testing.T) {
	t.Parallel()

	handler := reflect.TypeFor[backendv1connect.BackendServiceHandler]()
	for method := range handler.Methods() {
		name := method.Name
		for _, banned := range []string{"Login", "Resume", "Logout"} {
			if strings.HasPrefix(name, banned) {
				t.Errorf("BackendService serves %s: the session belongs to the daemon's own startup", name)
			}
		}
	}
}
