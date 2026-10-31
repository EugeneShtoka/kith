package daemon

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
)

// Every string and map field of every BackendService request is classified for
// profiles, and nothing else is: a field added without a decision fails here, and a
// request carrying one is refused at runtime. A map, and a stream's request, are not read
// by admitNamed, so neither may name a room.
func TestEveryRequestFieldIsClassified(t *testing.T) {
	t.Parallel()
	methods := v1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	seen := map[protoreflect.FullName]bool{}
	for i := range methods.Len() {
		m := methods.Get(i)
		var walk func(protoreflect.MessageDescriptor, map[protoreflect.FullName]bool)
		walk = func(md protoreflect.MessageDescriptor, in map[protoreflect.FullName]bool) {
			if in[md.FullName()] {
				return
			}
			in[md.FullName()] = true
			for j := range md.Fields().Len() {
				f := md.Fields().Get(j)
				switch {
				case f.IsMap():
					seen[f.FullName()] = true
					if namedFields[f.FullName()] != notARoom {
						t.Errorf("%s: %s is a map, which namesIn does not read: it may name no room", m.Name(), f.FullName())
					}
				case f.Kind() == protoreflect.StringKind:
					seen[f.FullName()] = true
					kind := namedFields[f.FullName()]
					switch {
					case kind == 0:
						t.Errorf("%s: %s is not in namedFields: decide what it names", m.Name(), f.FullName())
					case m.IsStreamingServer() && kind != notARoom:
						t.Errorf("%s streams, so its request is not checked, but %s names a room", m.Name(), f.FullName())
					}
				case f.Kind() == protoreflect.MessageKind:
					walk(f.Message(), in)
				}
			}
		}
		walk(m.Input(), map[protoreflect.FullName]bool{})
	}
	for name := range namedFields {
		if !seen[name] {
			t.Errorf("namedFields names %s, which no request has", name)
		}
	}
}
