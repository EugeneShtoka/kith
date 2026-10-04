package config

import (
	"fmt"

	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// A property is one scalar setting of the config, by its TOML path ("display.fps"):
// what the settings screen lists. Tables of records (rules, identities, tags,
// accounts), the key bindings and the storage locations are not properties: the first
// are edited a record at a time, the keys have their own table, and storage is where
// the app's own files live, which it cannot move under itself.

// PropertyKind is what a property holds, which decides how it is edited.
type PropertyKind int

const (
	// PropertyBool is on or off; PropertyOptionalBool is on, off, or unset (its
	// default).
	PropertyBool PropertyKind = iota + 1
	PropertyOptionalBool
	// PropertyInt is a whole number; PropertyOptionalInt may also be unset.
	PropertyInt
	PropertyOptionalInt
	PropertyFloat
	PropertyText
	// PropertyList is a list of text entries.
	PropertyList
)

// Property is one setting: its path, its kind, and what default.toml says of it.
type Property struct {
	Path string
	Kind PropertyKind
	// Doc is default.toml's comment for it, and Default the value it writes (as TOML
	// writes it: true, 30, "room"); either is "" when it gives none. Example marks a
	// Default written only commented out: often the default, sometimes an example.
	Doc, Default string
	Example      bool
}

// notProperties are the paths under which nothing is a property (see above).
var notProperties = []string{"keys", "storage", "profile"}

// Properties is every property, in the order the config declares them.
func Properties() []Property {
	propertiesOnce.Do(func() {
		docs := documented()
		walkProperties(reflect.TypeFor[Config](), nil, func(path []string, kind PropertyKind) {
			p := Property{Path: strings.Join(path, "."), Kind: kind}
			if d, ok := docs[p.Path]; ok {
				p.Doc, p.Default, p.Example = d.text, d.value, d.example
			}
			properties = append(properties, p)
		})
	})
	return slices.Clone(properties)
}

var (
	propertiesOnce sync.Once
	properties     []Property
)

// isProperty reports whether path names a property.
func isProperty(path string) bool {
	return slices.ContainsFunc(Properties(), func(p Property) bool { return p.Path == path })
}

// walkProperties calls found for each property field under typ, path naming typ.
func walkProperties(typ reflect.Type, path []string, found func([]string, PropertyKind)) {
	for i := range typ.NumField() {
		field := typ.Field(i)
		name := tomlName(field)
		if name == "" || (len(path) == 0 && slices.Contains(notProperties, name)) {
			continue
		}
		at := append(slices.Clip(path), name)
		if field.Type.Kind() == reflect.Struct {
			walkProperties(field.Type, at, found)
			continue
		}
		if kind, ok := kindOf(field.Type); ok {
			found(at, kind)
		}
	}
}

// kindOf is the property kind a field type is; false for one that is no property.
func kindOf(t reflect.Type) (PropertyKind, bool) {
	switch {
	case t.Kind() == reflect.Bool:
		return PropertyBool, true
	case t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Bool:
		return PropertyOptionalBool, true
	case t.Kind() == reflect.Int:
		return PropertyInt, true
	case t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Int:
		return PropertyOptionalInt, true
	case t.Kind() == reflect.Float64:
		return PropertyFloat, true
	case t.Kind() == reflect.String:
		return PropertyText, true
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String:
		return PropertyList, true
	}
	return 0, false
}

// field is the value at path in c, settable when c is.
func field(c reflect.Value, path string) (reflect.Value, bool) {
	v := c
	for name := range strings.SplitSeq(path, ".") {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		found := false
		for i := range v.NumField() {
			if tomlName(v.Type().Field(i)) == name {
				v, found = v.Field(i), true
				break
			}
		}
		if !found {
			return reflect.Value{}, false
		}
	}
	return v, true
}

// Value is the property at path as text: a list joined by ", ", an unset optional
// "" (set reports whether it is set). false when path is no property.
func (c Config) Value(path string) (text string, set, ok bool) {
	v, ok := field(reflect.ValueOf(c), path)
	if !ok || !isProperty(path) {
		return "", false, false
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return "", false, true
		}
		return fmt.Sprint(v.Elem().Interface()), true, true
	case reflect.Slice:
		return strings.Join(v.Interface().([]string), ", "), v.Len() > 0, true //nolint:forcetypeassert // kindOf admits []string alone
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64), !v.IsZero(), true
	default:
		return fmt.Sprint(v.Interface()), !v.IsZero(), true
	}
}

// List is the list property at path; nil when path is no list.
func (c Config) List(path string) []string {
	v, ok := field(reflect.ValueOf(c), path)
	if !ok || v.Kind() != reflect.Slice || !isProperty(path) {
		return nil
	}
	return slices.Clone(v.Interface().([]string)) //nolint:forcetypeassert // as Value
}

// SetValue writes text into the property at path, parsed by its kind. "" unsets an
// optional and zeroes the rest, which is the default a file without the key has.
func (c *Config) SetValue(path, text string) error {
	v, ok := field(reflect.ValueOf(c).Elem(), path)
	if !ok || !isProperty(path) {
		return fmt.Errorf("config: %s is not a setting", path)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		v.SetZero()
		return nil
	}
	switch kind, _ := kindOf(v.Type()); kind {
	case PropertyBool, PropertyOptionalBool:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return fmt.Errorf("%q is not on or off — write true or false", text)
		}
		setScalar(v, reflect.ValueOf(b))
	case PropertyInt, PropertyOptionalInt:
		n, err := strconv.Atoi(text)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", text)
		}
		setScalar(v, reflect.ValueOf(n))
	case PropertyFloat:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", text)
		}
		v.SetFloat(f)
	case PropertyText:
		v.SetString(text)
	case PropertyList:
		var entries []string
		for e := range strings.SplitSeq(text, ",") {
			if e = strings.TrimSpace(e); e != "" {
				entries = append(entries, e)
			}
		}
		v.Set(reflect.ValueOf(entries))
	default:
		return fmt.Errorf("config: %s is not a setting", path)
	}
	return nil
}

// SetList writes the list property at path; an empty list unsets it.
func (c *Config) SetList(path string, entries []string) error {
	v, ok := field(reflect.ValueOf(c).Elem(), path)
	if !ok || v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.String || !isProperty(path) {
		return fmt.Errorf("config: %s is not a list", path)
	}
	if len(entries) == 0 {
		v.SetZero()
		return nil
	}
	v.Set(reflect.ValueOf(slices.Clone(entries)))
	return nil
}

// setScalar sets v, or what it points to (a fresh value, never one shared with a
// config this one was cloned from).
func setScalar(v, x reflect.Value) {
	if v.Kind() == reflect.Pointer {
		p := reflect.New(v.Type().Elem())
		p.Elem().Set(x)
		v.Set(p)
		return
	}
	v.Set(x)
}

// doc is what default.toml says of one key; example marks a value only commented out.
type doc struct {
	text, value string
	example     bool
}

var (
	// tableLine is a [table] header; an [[array]] (live or commented) ends the keys of
	// the table before it.
	tableLine = regexp.MustCompile(`^\[([a-z_.]+)\]\s*(#.*)?$`)
	arrayLine = regexp.MustCompile(`^(?:# ?)?\[\[`)
	// keyLine is `key = value`, or `# key = value` for a default left commented out;
	// an example inside a comment is indented further ("#   key = …") and is no key.
	keyLine = regexp.MustCompile(`^(?:# ?)?([a-z_0-9]+)\s*=\s*(.*)$`)
)

// documented reads default.toml: for each key, what is written about it and the value
// it writes. The text is the comment directly above the key; else the comment opening
// its paragraph (one comment over a group of keys); else, in a table's first
// paragraph, the comment above the table's header — the key's own entry when that
// lists the keys one by one ("#   mode   ..."), else all of it. A key written twice
// (the default, then an alternative commented out) keeps the first.
func documented() map[string]doc {
	r := docReader{docs: map[string]doc{}, live: map[string]bool{}, headers: map[string][]string{}}
	for raw := range strings.SplitSeq(defaultConfigTOML, "\n") {
		r.line(strings.TrimRight(raw, " \t"))
	}
	// A key a table's header describes but the file never writes ("#   max_width ...").
	walkProperties(reflect.TypeFor[Config](), nil, func(path []string, _ PropertyKind) {
		p := strings.Join(path, ".")
		if _, ok := r.docs[p]; ok || len(path) < 2 {
			return
		}
		header, key := r.headers[strings.Join(path[:len(path)-1], ".")], path[len(path)-1]
		if listedIn(header, key) {
			r.docs[p] = doc{text: keyEntry(header, key)}
		}
	})
	return r.docs
}

// docReader is documented's place in default.toml.
type docReader struct {
	docs    map[string]doc
	live    map[string]bool     // keys written uncommented, which a commented one never replaces
	headers map[string][]string // each table's header comment, for keys it lists but never writes

	table           string
	inArray         bool     // inside an [[array]] block, whose keys are no properties
	exampleArray    bool     // …a commented one, which ends at the next blank line
	header, own     []string // the table header's comment; the comment since the last key
	para            []string // the comment opening the paragraph
	headerParagraph bool     // still in the table's first paragraph
	listOf          string   // the key whose list is written over the lines that follow
	listLines       []string // that list's lines so far
}

// line reads one line.
func (r *docReader) line(line string) {
	switch {
	case r.listOf != "":
		r.listLine(line)
	case line == "":
		r.own, r.para, r.headerParagraph = nil, nil, false
		if r.exampleArray {
			r.inArray, r.exampleArray = false, false
		}
	case arrayLine.MatchString(line):
		r.inArray, r.exampleArray = true, strings.HasPrefix(line, "#")
		r.own, r.para, r.headerParagraph = nil, nil, false
	case tableLine.MatchString(line):
		r.table = tableLine.FindStringSubmatch(line)[1]
		r.inArray, r.exampleArray, r.header, r.headerParagraph = false, false, r.own, true
		r.headers[r.table], r.own, r.para = r.own, nil, nil
	default:
		if m := keyLine.FindStringSubmatch(line); m != nil && !r.inArray && isValue(m[2]) {
			r.key(m[1], m[2], strings.HasPrefix(line, "#"))
			return
		}
		if comment, ok := strings.CutPrefix(line, "#"); ok {
			r.own = append(r.own, comment)
			if len(r.para) == 0 || len(r.own) == len(r.para)+1 {
				r.para = append(r.para, comment) // the paragraph's opening comment, unbroken
			}
		}
	}
}

// listLine gathers a list written over several lines, to its closing bracket.
func (r *docReader) listLine(line string) {
	r.listLines = append(r.listLines, strings.TrimSpace(line))
	if strings.HasPrefix(strings.TrimSpace(line), "]") {
		d := r.docs[r.listOf]
		d.value = strings.Join(r.listLines, " ")
		r.docs[r.listOf], r.listOf, r.listLines = d, "", nil
	}
}

// key records one key line: its text and value, unless a live line wrote it already.
func (r *docReader) key(name, rest string, commented bool) {
	path := name
	if r.table != "" {
		path = r.table + "." + name
	}
	value, trailing := splitTrailing(rest)
	if value == "[" && !commented {
		r.listOf, r.listLines = path, []string{"["} // its entries follow, to "]"
	}
	if _, seen := r.docs[path]; !seen || (!commented && !r.live[path]) {
		text := joinDoc(r.own)
		switch {
		case text != "":
		case len(r.para) > 0:
			text = joinDoc(r.para)
		case r.headerParagraph:
			text = keyEntry(r.header, name)
		}
		if trailing != "" {
			text = strings.TrimSpace(text + " " + trailing)
		}
		r.docs[path] = doc{text: text, value: value, example: commented}
		r.live[path] = !commented
	}
	r.own = nil
}

// keyEntry is key's own entry in a table header's comment ("#   key   what it is",
// with its indented continuation lines), else the whole comment.
func keyEntry(header []string, key string) string {
	for i, line := range header {
		rest, ok := strings.CutPrefix(strings.TrimLeft(line, " "), key+" ")
		if !ok || !strings.HasPrefix(line, "  ") || strings.HasPrefix(strings.TrimSpace(rest), "=") {
			continue // not an entry: an example ("key = ...")
		}
		entry := []string{strings.TrimSpace(rest)}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		for _, next := range header[i+1:] {
			if deeper := len(next) - len(strings.TrimLeft(next, " ")); deeper <= indent || strings.TrimSpace(next) == "" {
				break
			}
			entry = append(entry, strings.TrimSpace(next))
		}
		return strings.Join(entry, " ")
	}
	return joinDoc(header)
}

// listedIn reports whether a table header's comment lists key one by one.
func listedIn(header []string, key string) bool {
	for _, line := range header {
		rest, ok := strings.CutPrefix(strings.TrimLeft(line, " "), key+" ")
		if ok && strings.HasPrefix(line, "  ") && !strings.HasPrefix(strings.TrimSpace(rest), "=") {
			return true
		}
	}
	return false
}

// joinDoc is comment lines as text, lines kept (a table of values stays a table), the
// space after each "#" taken off.
func joinDoc(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(strings.TrimPrefix(l, " "), " "))
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

// isValue reports whether what follows `key =` is a TOML value (or a list opening over
// several lines), so prose that merely starts like a key ("# send_receipts = false
// still moves…") is read as the comment it is.
func isValue(s string) bool {
	value, _ := splitTrailing(s)
	if value == "[" {
		return true
	}
	var v map[string]any
	_, err := toml.Decode("v = "+value, &v)
	return err == nil
}

// DefaultList is a list property's default entries, as default.toml writes them; nil
// for none, for one written only as a commented example, or for no list.
func (p Property) DefaultList() []string {
	if p.Kind != PropertyList || p.Default == "" || p.Example {
		return nil
	}
	var v struct{ V []string }
	if _, err := toml.Decode("V = "+p.Default, &v); err != nil {
		return nil
	}
	return v.V
}

// splitTrailing cuts a value from the comment after it, outside quotes.
func splitTrailing(s string) (value, comment string) {
	quoted := false
	for i, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == '#' && !quoted:
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
		}
	}
	return strings.TrimSpace(s), ""
}
