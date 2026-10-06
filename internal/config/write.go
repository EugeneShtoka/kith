package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Save writes cfg back to path, replacing the file.
func Save(path string, cfg Config) error {
	if err := backupOnce(path); err != nil {
		return err
	}
	return writeAtomic(path, Encode(cfg))
}

// Encode is cfg as Save writes it: the settings that differ from the defaults.
func Encode(cfg Config) string {
	return header + encodeDiff(reflect.ValueOf(cfg), reflect.ValueOf(omittedBaseline()), nil)
}

// header explains what the file now is.
const header = `# kith configuration — written by kith.
#
# This file records only the settings that differ from the built-in defaults, so
# anything absent is at its default and will follow if that default ever changes.
# Editing it by hand is still fine; the app rewrites it when you change a setting
# from inside it, which keeps the values and drops any comments you added.
#
# For every option with its documentation, run: kith --print-config
# For the current keybindings, press ? in the app.

`

// omittedBaseline is the config a file with nothing in it produces — the zero value
// with FillDefaults applied, exactly as Load would leave it.
func omittedBaseline() Config {
	var cfg Config
	cfg.Keys.FillDefaults()
	start := starter()
	cfg.Tags, cfg.Display.Rail.Order = start.Tags, start.Display.Rail.Order
	return cfg
}

// backupOnce copies path to path+".bak" the first time, so the hand-written version
// survives.
func backupOnce(path string) error {
	backup := path + ".bak"
	if _, err := os.Stat(backup); err == nil {
		return nil
	}
	current, err := os.ReadFile(path) // #nosec G304 -- the user's own config file
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("config: read %s for backup: %w", path, err)
	}
	// The path is the config file the app was told to use, plus a fixed suffix — the
	// same provenance as the file just read, not new input.
	if err := os.WriteFile(backup, current, 0o600); err != nil { // #nosec G703 -- derived from the caller's own config path
		return fmt.Errorf("config: write %s: %w", backup, err)
	}
	return nil
}

// encodeDiff renders the fields of value that differ from defaults, as TOML, with
// prefix naming the enclosing table.
func encodeDiff(value, defaults reflect.Value, prefix []string) string {
	values, tables := splitDiff(value, defaults, prefix)
	return values + tables
}

// splitDiff is encodeDiff with the two halves kept apart, so a caller can tell whether
// a table has any values of its own.
func splitDiff(value, defaults reflect.Value, prefix []string) (string, string) {
	var values, tables strings.Builder
	for i := range value.NumField() {
		field := value.Type().Field(i)
		name := tomlName(field)
		if name == "" {
			continue
		}
		got, want := value.Field(i), defaults.Field(i)

		switch {
		case got.Kind() == reflect.Struct:
			tables.WriteString(encodeTable(got, want, append(prefix, name)))
		case got.Kind() == reflect.Slice && got.Type().Elem().Kind() == reflect.Struct:
			if got.Len() == 0 && want.Len() > 0 {
				// Emptied: written as [], or the baseline's would come back on the next load.
				fmt.Fprintf(&values, "%s = []\n", name)
				continue
			}
			tables.WriteString(encodeStructSlice(got, want, append(prefix, name)))
		case got.Kind() == reflect.Slice:
			if literal, ok := arrayLiteral(got, want); ok {
				fmt.Fprintf(&values, "%s = %s\n", name, literal)
			}
		default:
			if scalarEqual(got, want) {
				continue
			}
			if literal, ok := scalarLiteral(got); ok {
				fmt.Fprintf(&values, "%s = %s\n", name, literal)
			}
		}
	}
	return values.String(), tables.String()
}

// encodeTable renders a nested struct as a `[table]`.
func encodeTable(got, want reflect.Value, path []string) string {
	values, tables := splitDiff(got, want, path)
	if values == "" {
		return tables
	}
	return "\n[" + strings.Join(path, ".") + "]\n" + values + tables
}

// arrayLiteral renders a slice of scalars as a TOML array. ok is false when it matches
// the baseline, so an untouched list is left out entirely.
func arrayLiteral(got, want reflect.Value) (string, bool) {
	if slicesEqual(got, want) {
		return "", false
	}
	items := make([]string, 0, got.Len())
	for i := range got.Len() {
		literal, ok := scalarLiteral(got.Index(i))
		if !ok {
			continue
		}
		items = append(items, literal)
	}
	// An emptied list is written as [], because omitting it would let the baseline come
	// back on the next load.
	line := "[" + strings.Join(items, ", ") + "]"
	if len(items) < 2 || len(line) <= arrayWrapWidth {
		return line, true
	}
	// One entry per line once it stops fitting, so long ID lists stay diffable.
	return "[\n\t" + strings.Join(items, ",\n\t") + ",\n]", true
}

// arrayWrapWidth is where a list stops being a line and becomes a block.
const arrayWrapWidth = 80

// encodeStructSlice renders a slice of structs as repeated `[[table]]` entries.
func encodeStructSlice(got, want reflect.Value, path []string) string {
	if slicesEqual(got, want) {
		return ""
	}
	var b strings.Builder
	zero := reflect.New(got.Type().Elem()).Elem()
	for i := range got.Len() {
		b.WriteString("\n[[" + strings.Join(path, ".") + "]]\n")
		b.WriteString(encodeDiff(got.Index(i), zero, path))
	}
	return b.String()
}

// tomlName is a field's TOML key, or "" for a field that is not written.
func tomlName(field reflect.StructField) string {
	tag := field.Tag.Get("toml")
	if tag == "" || tag == "-" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

// scalarEqual compares two scalar fields, treating a nil pointer as "unset" so an
// unset *bool matches an unset default rather than being written as its value.
func scalarEqual(got, want reflect.Value) bool {
	if got.Kind() == reflect.Pointer {
		switch {
		case got.IsNil() && want.IsNil():
			return true
		case got.IsNil() || want.IsNil():
			return false
		default:
			return got.Elem().Interface() == want.Elem().Interface()
		}
	}
	if got.Kind() == reflect.Map {
		return mapsEqual(got, want)
	}
	return got.Interface() == want.Interface()
}

// scalarLiteral renders a scalar as a TOML value. ok is false for a kind that has no
// literal form, which is a field the emitter should skip rather than guess at.
func scalarLiteral(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.String:
		return quoteTOML(v.String()), true
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), true
	case reflect.Int, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true
	case reflect.Float64:
		return floatTOML(v.Float())
	case reflect.Pointer:
		if v.IsNil() {
			return "", false
		}
		return scalarLiteral(v.Elem())
	case reflect.Map:
		return inlineTable(v), true
	default:
		return "", false
	}
}

// inlineTable renders a map[string]string as `{ k = "v", … }`, keys sorted so the
// output does not shuffle between saves.
func inlineTable(v reflect.Value) string {
	keys := make([]string, 0, v.Len())
	for _, k := range v.MapKeys() {
		keys = append(keys, k.String())
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, quoteTOML(k)+" = "+quoteTOML(v.MapIndex(reflect.ValueOf(k)).String()))
	}
	return "{ " + strings.Join(pairs, ", ") + " }"
}

// floatTOML renders a float as a TOML float value.
func floatTOML(f float64) (string, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", false
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s, true
}

// quoteTOML renders s as a TOML basic string. Only the escapes TOML requires are
// applied, so a display name full of accents or Hebrew stays readable in the file.
func quoteTOML(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// slicesEqual compares two slices element-wise, treating nil and empty as the same
// thing — a config file cannot tell them apart, so neither should the diff.
func slicesEqual(a, b reflect.Value) bool {
	if a.Len() != b.Len() {
		return false
	}
	for i := range a.Len() {
		if !reflect.DeepEqual(a.Index(i).Interface(), b.Index(i).Interface()) {
			return false
		}
	}
	return true
}

// mapsEqual compares two maps by content.
func mapsEqual(a, b reflect.Value) bool {
	if a.Len() != b.Len() {
		return false
	}
	for _, k := range a.MapKeys() {
		other := b.MapIndex(k)
		if !other.IsValid() || other.Interface() != a.MapIndex(k).Interface() {
			return false
		}
	}
	return true
}

// writeAtomic writes body to path (mode 0600) via a temp file and a rename, so an
// interrupted save cannot leave a half-written config.
func writeAtomic(path, body string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: mktemp: %w", err)
	}
	name := tmp.Name()
	// Cleanup on the error paths below; the error each returns is the report.
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(name) }
	if _, err := tmp.WriteString(body); err != nil {
		cleanup()
		return fmt.Errorf("config: write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("config: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("config: close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("config: chmod %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("config: publish %s: %w", path, err)
	}
	return nil
}

// Snapshot is a configuration and the revision of the file it was read from.
type Snapshot struct {
	Config   Config
	Revision string
}

// Revision names a config file's contents: two files have the same revision only when
// they are byte for byte the same, so a hand edit is a change too. A missing file is "".
func Revision(data []byte) string {
	if data == nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
