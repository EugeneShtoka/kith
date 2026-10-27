package config

import (
	"reflect"
	"slices"
	"strings"
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

// Settings edits a profile's archive and rail as fields of its record, by a dotted
// name; no other record has such fields.
func TestSettingsEditAProfilesArchiveAndRail(t *testing.T) {
	t.Parallel()
	var fields []string
	for _, table := range RecordTables() {
		for _, f := range table.Fields {
			if strings.Contains(f.Path, ".") {
				fields = append(fields, table.Path+": "+f.Path)
			}
		}
	}
	want := []string{
		"profile: archive.rooms", "profile: archive.push", "profile: rail.order",
		"profile: rail.hidden", "profile: rail.hide_when_empty", "profile: rail.network_colors",
	}
	if !slices.Equal(fields, want) {
		t.Errorf("dotted record fields = %v, want %v", fields, want)
	}

	cfg := Config{Profiles: []Profile{{Name: "default", Include: []string{"*"}}, {Name: "phone", Include: []string{"dm"}}}}
	if err := cfg.SetRecordList("profile", 1, "archive.rooms", []string{"not tag:Family"}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetRecordValue("profile", 1, "archive.push", "false"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetRecordList("profile", 1, "rail.order", []string{"tag:Unread", "*"}); err != nil {
		t.Fatal(err)
	}
	phone := cfg.Profiles[1]
	if !slices.Equal(phone.Archive.Rooms, []string{"not tag:Family"}) || phone.Archive.Push == nil || *phone.Archive.Push ||
		!slices.Equal(phone.Rail.Order, []string{"tag:Unread", "*"}) {
		t.Errorf("phone = %+v, want its archive and rail set", phone)
	}
	if got, set := cfg.RecordValue("profile", 1, "archive.push"); !set || got != "false" {
		t.Errorf("archive.push reads %q, %v; want false, set", got, set)
	}
	if got := cfg.RecordList("profile", 1, "rail.order"); !slices.Equal(got, []string{"tag:Unread", "*"}) {
		t.Errorf("rail.order reads %v", got)
	}
}
