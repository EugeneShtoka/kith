package daemon

import (
	"slices"
	"testing"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
)

// Every BackendService call is classified for profiles, and nothing else is: a call
// added without a decision would be refused by profileGate at runtime.
func TestEveryCallIsClassifiedForProfiles(t *testing.T) {
	t.Parallel()
	methods := v1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	var names []string
	for i := range methods.Len() {
		name := string(methods.Get(i).Name())
		names = append(names, name)
		if reaches[name] == 0 {
			t.Errorf("%s is not in reaches: decide whether a profile narrows it", name)
		}
	}
	for name := range reaches {
		if !slices.Contains(names, name) {
			t.Errorf("reaches names %s, which the service does not have", name)
		}
	}
}
