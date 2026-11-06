package daemon_test

import (
	"reflect"
	"slices"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/config"
)

// The alias editor's writes land in the config as the terminal client's do: an account
// joins a person, its color and picture with it; moved to another person it leaves the
// first, and the picture it was; a picture none of the person's, or no name, is refused
// and nothing is written.
func TestAnAccountJoinsAPersonWithTheirPicture(t *testing.T) {
	t.Parallel()
	w := settingsServed(t, config.Config{})
	wa, tg := "whatsapp:15550101@s.whatsapp.net", "telegram:42"
	set := func(id, alias, color, picture string) error {
		_, err := w.c.SetIdentity(t.Context(), inProfile("home", &v1.SetIdentityRequest{Id: id, Alias: alias, Color: color, Picture: picture}))
		return err
	}
	people := func() []*v1.Identity {
		r, err := w.c.Identities(t.Context(), inProfile("home", &v1.IdentitiesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.GetIdentities()
	}
	is := func(when string, want ...*v1.Identity) {
		t.Helper()
		got := people()
		if len(got) != len(want) {
			t.Fatalf("%s: people %v, want %v", when, got, want)
		}
		for i := range want {
			g, w := got[i], want[i]
			if g.GetAlias() != w.GetAlias() || g.GetColor() != w.GetColor() || g.GetPicture() != w.GetPicture() || !slices.Equal(g.GetIds(), w.GetIds()) {
				t.Errorf("%s: person %d = %v, want %v", when, i, g, w)
			}
		}
	}

	is("at first")
	if err := set(wa, "Dana", "#ff0000", ""); err != nil {
		t.Fatal(err)
	}
	is("one account", &v1.Identity{Alias: "Dana", Color: "#ff0000", Ids: []string{wa}})
	if err := set(tg, "Dana", "#ff0000", tg); err != nil {
		t.Fatal(err)
	}
	is("two, the second's picture", &v1.Identity{Alias: "Dana", Color: "#ff0000", Ids: []string{wa, tg}, Picture: tg})
	if err := set(tg, "Eli", "", ""); err != nil {
		t.Fatal(err)
	}
	is("the second moved", &v1.Identity{Alias: "Dana", Color: "#ff0000", Ids: []string{wa}}, &v1.Identity{Alias: "Eli", Ids: []string{tg}})

	before := w.bytes()
	for name, refused := range map[string]func() error{
		"a picture none of theirs": func() error { return set(wa, "Dana", "", "telegram:9") },
		"no name":                  func() error { return set(wa, " ", "", "") },
		"no account":               func() error { return set("", "Dana", "", "") },
	} {
		if err := refused(); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if !reflect.DeepEqual(before, w.bytes()) {
		t.Error("a refused write changed the file")
	}
}
