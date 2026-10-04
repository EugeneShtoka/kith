package config

import (
	"slices"
	"strings"
	"testing"
)

// The record tables are every list of records but those with editors of their own,
// each with what default.toml says of it.
func TestRecordTablesAreListedAndDocumented(t *testing.T) {
	t.Parallel()
	var paths []string
	for _, tb := range RecordTables() {
		paths = append(paths, tb.Path)
		if strings.TrimSpace(tb.Doc) == "" {
			t.Errorf("%s has no text in default.toml", tb.Path)
		}
		if len(tb.Fields) == 0 {
			t.Errorf("%s has no fields", tb.Path)
		}
	}
	for _, own := range ownEditors {
		if slices.Contains(paths, own) {
			t.Errorf("%s is listed, but has an editor of its own", own)
		}
	}
	for _, want := range []string{"display.space_rule", "display.identity", "display.read_rule", "slack.account"} {
		if !slices.Contains(paths, want) {
			t.Errorf("%s is missing from %v", want, paths)
		}
	}
}

// A record is added, its fields set (each kind) and read back, unset, and removed; a
// change to a config copied by assignment never reaches the original.
func TestRecordsAreEditedFieldByField(t *testing.T) {
	t.Parallel()
	var base Config
	base.Display.ReadRules = []ReadRule{{Match: "Work"}}
	cfg := base // a plain copy: the slice is shared
	i, err := cfg.AddRecord("display.read_rule")
	if err != nil || i != 1 {
		t.Fatalf("AddRecord = %d, %v", i, err)
	}
	for name, text := range map[string]string{"match": "Friends", "send": "false", "delay": "30"} {
		if err := cfg.SetRecordValue("display.read_rule", i, name, text); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
		if got, set := cfg.RecordValue("display.read_rule", i, name); !set || got != text {
			t.Errorf("%s = %q (set %v), want %q", name, got, set, text)
		}
	}
	if err := cfg.SetRecordValue("display.read_rule", 0, "match", "Home"); err != nil {
		t.Fatal(err)
	}
	if base.Display.ReadRules[0].Match != "Work" || len(base.Display.ReadRules) != 1 {
		t.Errorf("the original changed under its copy: %+v", base.Display.ReadRules)
	}
	if err := cfg.SetRecordValue("display.read_rule", i, "delay", ""); err != nil {
		t.Fatal(err)
	}
	if _, set := cfg.RecordValue("display.read_rule", i, "delay"); set {
		t.Error("an empty value left delay set")
	}
	if err := cfg.SetRecordValue("display.read_rule", i, "delay", "soon"); err == nil {
		t.Error("delay took a word")
	}

	id, _ := cfg.AddRecord("display.identity")
	if err := cfg.SetRecordList("display.identity", id, "ids", []string{"@a:x", "whatsapp:1"}); err != nil {
		t.Fatal(err)
	}
	if got := cfg.RecordList("display.identity", id, "ids"); !slices.Equal(got, []string{"@a:x", "whatsapp:1"}) {
		t.Errorf("ids = %v", got)
	}
	if err := cfg.RemoveRecord("display.read_rule", 0); err != nil {
		t.Fatal(err)
	}
	if cfg.Records("display.read_rule") != 1 || cfg.Display.ReadRules[0].Match != "Friends" {
		t.Errorf("after removing the first: %+v", cfg.Display.ReadRules)
	}
	if err := cfg.RemoveRecord("display.read_rule", 0); err != nil || cfg.Display.ReadRules != nil {
		t.Errorf("removing the last left %v (%v), want the table unset", cfg.Display.ReadRules, err)
	}
	for _, own := range []string{"tag", "notifications.rule"} {
		if _, err := cfg.AddRecord(own); err == nil {
			t.Errorf("%s was edited here, though it has an editor of its own", own)
		}
	}
}
