package config

import (
	"reflect"
	"testing"
)

// A profile's archive and rail survive a save, push = false included (unset would
// hand the profile back the tag's own push), and the inline form a person writes reads
// the same as the tables kith writes.
func TestAProfilesArchiveAndRailSurviveASave(t *testing.T) {
	t.Parallel()
	inline, err := Decode(`
[[profile]]
name = "default"
include = ["*"]

[[profile]]
name    = "phone"
include = ["dm"]
archive = { rooms = ["not tag:Family", "protocol:signal"], push = false }
rail    = { order = ["tag:Unread", "*"], hidden = ["tag:Spam"] }
`)
	if err != nil {
		t.Fatal(err)
	}
	phone := inline.Profiles[1]
	want := Profile{
		Name: "phone", Include: []string{"dm"},
		Archive: ProfileArchive{Rooms: []string{"not tag:Family", "protocol:signal"}, Push: new(false)},
		Rail:    Rail{Order: []string{"tag:Unread", "*"}, Hidden: []string{"tag:Spam"}},
	}
	if !reflect.DeepEqual(phone, want) {
		t.Fatalf("decoded %+v, want %+v", phone, want)
	}
	saved, err := Decode(Encode(inline))
	if err != nil {
		t.Fatalf("a saved config does not read back: %v\n%s", err, Encode(inline))
	}
	if !reflect.DeepEqual(saved.Profiles, inline.Profiles) {
		t.Errorf("after a save the profiles are %+v, want %+v\n%s", saved.Profiles, inline.Profiles, Encode(inline))
	}
}
