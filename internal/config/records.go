package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// A record table is a list of records, one [[block]] each ([[display.read_rule]]):
// what the settings screen edits a record at a time, each record's fields set as
// properties are. The tags and the notification rules have editors of their own, and
// profiles are the accounts the app is started with.

// RecordTable is one table: its path, what default.toml says above it, and its fields
// (each Path the field's name, its Doc the comment on it in default.toml's example).
type RecordTable struct {
	Path   string
	Doc    string
	Fields []Property
}

// ownEditors are the record tables edited elsewhere.
var ownEditors = []string{"tag", "notifications.rule", "profile"}

// RecordTables is every record table, in the order the config declares them.
func RecordTables() []RecordTable {
	tablesOnce.Do(func() {
		docs := documented()
		walkTables(reflect.TypeFor[Config](), nil, func(path string, elem reflect.Type) {
			t := RecordTable{Path: path, Doc: docs[path].text}
			if t.Doc == "" {
				t.Doc = paragraphAround("[[" + path + "]]")
			}
			for f := range elem.Fields() {
				name := tomlName(f)
				kind, ok := kindOf(f.Type)
				if name == "" || !ok {
					continue
				}
				t.Fields = append(t.Fields, Property{Path: name, Kind: kind, Doc: docs[path+"."+name].text})
			}
			tables = append(tables, t)
		})
	})
	return slices.Clone(tables)
}

var (
	tablesOnce sync.Once
	tables     []RecordTable
)

// paragraphAround is the comment paragraph of default.toml holding needle (a table
// shown as an example inside a longer comment); "" when none does.
func paragraphAround(needle string) string {
	lines := strings.Split(defaultConfigTOML, "\n")
	at := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "#") && strings.Contains(l, needle) })
	if at < 0 {
		return ""
	}
	first, last := at, at
	for first > 0 && strings.HasPrefix(lines[first-1], "#") && strings.TrimSpace(strings.TrimPrefix(lines[first-1], "#")) != "" {
		first--
	}
	for last+1 < len(lines) && strings.HasPrefix(lines[last+1], "#") {
		last++
	}
	comment := make([]string, 0, last-first+1)
	for _, l := range lines[first : last+1] {
		comment = append(comment, strings.TrimPrefix(l, "#"))
	}
	return joinDoc(comment)
}

// walkTables calls found for each record table under typ, path naming typ.
func walkTables(typ reflect.Type, path []string, found func(string, reflect.Type)) {
	for f := range typ.Fields() {
		name := tomlName(f)
		if name == "" || (len(path) == 0 && slices.Contains(notProperties, name)) {
			continue
		}
		at := append(slices.Clip(path), name)
		joined := strings.Join(at, ".")
		switch {
		case f.Type.Kind() == reflect.Struct:
			walkTables(f.Type, at, found)
		case f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.Struct && !slices.Contains(ownEditors, joined):
			found(joined, f.Type.Elem())
		}
	}
}

// table is the record table at path in c, settable when c is.
func table(c reflect.Value, path string) (reflect.Value, bool) {
	if !slices.ContainsFunc(RecordTables(), func(t RecordTable) bool { return t.Path == path }) {
		return reflect.Value{}, false
	}
	return field(c, path)
}

// recordField is field name of record i of the table at path in c.
func recordField(c reflect.Value, path string, i int, name string) (reflect.Value, error) {
	t, ok := table(c, path)
	if !ok {
		return reflect.Value{}, fmt.Errorf("config: %s is not a list of records", path)
	}
	if i < 0 || i >= t.Len() {
		return reflect.Value{}, fmt.Errorf("config: %s has no record %d", path, i+1)
	}
	rec := t.Index(i)
	for f := range rec.Type().Fields() {
		if tomlName(f) == name {
			return rec.FieldByIndex(f.Index), nil
		}
	}
	return reflect.Value{}, fmt.Errorf("config: %s has no field %q", path, name)
}

// Records is how many records the table at path holds.
func (c Config) Records(path string) int {
	t, ok := table(reflect.ValueOf(c), path)
	if !ok {
		return 0
	}
	return t.Len()
}

// RecordValue is a field of record i as text, and whether it is set (see Value).
func (c Config) RecordValue(path string, i int, name string) (text string, set bool) {
	v, err := recordField(reflect.ValueOf(c), path, i, name)
	if err != nil {
		return "", false
	}
	return valueText(v)
}

// RecordList is a list field of record i.
func (c Config) RecordList(path string, i int, name string) []string {
	v, err := recordField(reflect.ValueOf(c), path, i, name)
	if err != nil || v.Kind() != reflect.Slice {
		return nil
	}
	return slices.Clone(v.Interface().([]string)) //nolint:forcetypeassert // kindOf admits []string alone
}

// SetRecordValue writes text into a field of record i, parsed by its kind ("" unsets).
func (c *Config) SetRecordValue(path string, i int, name, text string) error {
	if err := c.ownTable(path); err != nil {
		return err
	}
	v, err := recordField(reflect.ValueOf(c).Elem(), path, i, name)
	if err != nil {
		return err
	}
	return setText(v, text)
}

// SetRecordList writes a list field of record i; an empty list unsets it.
func (c *Config) SetRecordList(path string, i int, name string, entries []string) error {
	if err := c.ownTable(path); err != nil {
		return err
	}
	v, err := recordField(reflect.ValueOf(c).Elem(), path, i, name)
	if err != nil {
		return err
	}
	if v.Kind() != reflect.Slice {
		return fmt.Errorf("config: %s.%s is not a list", path, name)
	}
	if len(entries) == 0 {
		v.SetZero()
		return nil
	}
	v.Set(reflect.ValueOf(slices.Clone(entries)))
	return nil
}

// AddRecord appends an empty record to the table at path, and is its index.
func (c *Config) AddRecord(path string) (int, error) {
	if err := c.ownTable(path); err != nil {
		return 0, err
	}
	t, _ := table(reflect.ValueOf(c).Elem(), path)
	t.Set(reflect.Append(t, reflect.Zero(t.Type().Elem())))
	return t.Len() - 1, nil
}

// RemoveRecord takes record i out of the table at path.
func (c *Config) RemoveRecord(path string, i int) error {
	if err := c.ownTable(path); err != nil {
		return err
	}
	t, _ := table(reflect.ValueOf(c).Elem(), path)
	if i < 0 || i >= t.Len() {
		return fmt.Errorf("config: %s has no record %d", path, i+1)
	}
	out := reflect.MakeSlice(t.Type(), 0, t.Len()-1)
	out = reflect.AppendSlice(out, t.Slice(0, i))
	out = reflect.AppendSlice(out, t.Slice(i+1, t.Len()))
	if out.Len() == 0 {
		out = reflect.Zero(t.Type())
	}
	t.Set(out)
	return nil
}

// ownTable gives c a table at path of its own before it is changed: a config cloned
// shallowly (or not at all) must never write through to the one it came from.
func (c *Config) ownTable(path string) error {
	t, ok := table(reflect.ValueOf(c).Elem(), path)
	if !ok {
		return fmt.Errorf("config: %s is not a list of records", path)
	}
	if !t.IsNil() {
		t.Set(deepCopy(t))
	}
	return nil
}
