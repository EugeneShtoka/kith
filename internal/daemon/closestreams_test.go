package daemon

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// closeStreams ends every stream the Remote holds, found by reflection.
func TestCloseStreamsEndsEveryStream(t *testing.T) {
	t.Parallel()

	// The real constructor, so every declared stream is seen.
	r := newRemote(nil)
	r.closeStreams()

	value := reflect.ValueOf(r).Elem()
	found := 0
	for i := range value.NumField() {
		field := value.Field(i)
		if field.Kind() != reflect.Pointer || field.IsNil() {
			continue
		}
		elem := field.Elem()
		if elem.Kind() != reflect.Struct || !strings.HasPrefix(elem.Type().Name(), "stream[") {
			continue
		}
		found++
		name := value.Type().Field(i).Name
		ch := elem.FieldByName("ch")
		if !ch.IsValid() || ch.IsNil() {
			t.Errorf("%s has no channel", name)
			continue
		}
		// `ch` is unexported, so reflect refuses to operate on it directly.
		ch = reflect.NewAt(ch.Type(), unsafe.Pointer(ch.UnsafeAddr())).Elem()
		// Select with a default, not Recv: receiving on an open stream would hang the suite
		// instead of failing it.
		chosen, _, recvOK := reflect.Select([]reflect.SelectCase{
			{Dir: reflect.SelectRecv, Chan: ch},
			{Dir: reflect.SelectDefault},
		})
		switch {
		case chosen == 1:
			t.Errorf("stream %q is still open after closeStreams — its listener will park "+
				"on it for ever, which looks exactly like waiting for news", name)
		case recvOK:
			t.Errorf("stream %q delivered a value rather than a close", name)
		}
	}
	if found < 8 {
		t.Fatalf("found %d streams by reflection, expected at least the 8 Remote declares — "+
			"the test is looking at the wrong thing", found)
	}
}
