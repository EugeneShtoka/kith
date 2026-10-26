package daemon

import (
	"net/http"
	"slices"
	"testing"
	"time"

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

// A switch finding an attachment in its profile that was ready once and has ended since
// (another switch ended it) waits for the next to be ready, not for the gone one; and
// once the streams stop for good it waits for nothing.
func TestASwitchWaitsPastAnAttachmentAlreadyGone(t *testing.T) {
	t.Parallel()
	closed := func() chan struct{} { c := make(chan struct{}); close(c); return c }
	for _, stop := range []bool{false, true} {
		r := newRemote(http.DefaultClient)
		r.UseProfile("work")
		r.attempt = streamAttempt{profile: "work", end: func() {}, ready: closed(), ended: closed()}
		r.begun = make(chan struct{})
		returned := make(chan struct{})
		go func() { defer close(returned); _ = r.SwitchProfile("work") }()
		select {
		case <-returned:
			t.Fatal("the switch returned on an attachment already gone")
		case <-time.After(50 * time.Millisecond):
		}
		if stop {
			r.closeStreams()
		} else {
			close(r.begin(func() {}).ready)
		}
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Fatalf("stop %v: the switch never returned", stop)
		}
	}
}
