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

// A property is one scalar setting by TOML path ("display.fps"), as the settings screen
// lists it. Record tables, key bindings and storage are not properties: records are
// edited one at a time, keys have their own table, and the app cannot move its own files.

// PropertyKind is what a property holds, which decides how it is edited.
type PropertyKind int

const (
	// PropertyBool is on or off; PropertyOptionalBool may also be unset (its default).
	PropertyBool PropertyKind = iota + 1
	PropertyOptionalBool
	// PropertyInt is a whole number; PropertyOptionalInt may also be unset.
	PropertyInt
	PropertyOptionalInt
	PropertyFloat
	PropertyOptionalFloat
	PropertyText
	// PropertyList is a list of text entries.
	PropertyList
)

// Property is one setting: its path, its kind, the file it is in, and what that file's
// annotated default (default.toml, default_device.toml) says of it.
type Property struct {
	Path string
	Kind PropertyKind
	// File is BridgeFile or DeviceFile.
	File string
	// Doc is the annotated default's comment and Default its value as TOML (true, 30,
	// "room"); "" when absent. Example marks a Default written only commented out.
	Doc, Default string
	Example      bool
}

// notProperties are the top-level tables that hold no properties.
var notProperties = []string{"keys", "storage"}

// settingsFile is one file settings live in: its name, annotated default and type.
type settingsFile struct {
	name, annotated string
	typ             reflect.Type
}

// settingsFiles are the bridge's file and the device's, in the order settings list.
var settingsFiles = []settingsFile{
	{BridgeFile, defaultConfigTOML, reflect.TypeFor[Config]()},
	{DeviceFile, defaultDeviceTOML, reflect.TypeFor[Device]()},
}

// Properties is every property, the bridge's then the device's, each in the order its
// file's type declares them.
func Properties() []Property {
	propertiesOnce.Do(func() {
		for _, f := range settingsFiles {
			docs := documented(f)
			walkProperties(f.typ, nil, func(path []string, kind PropertyKind) {
				p := Property{Path: strings.Join(path, "."), Kind: kind, File: f.name}
				if d, ok := docs[p.Path]; ok {
					p.Doc, p.Default, p.Example = d.text, d.value, d.example
				}
				properties = append(properties, p)
			})
		}
	})
	return slices.Clone(properties)
}

var (
	propertiesOnce sync.Once
	properties     []Property
)

// isProperty reports whether path names a property of file.
func isProperty(file, path string) bool {
	return slices.ContainsFunc(Properties(), func(p Property) bool { return p.File == file && p.Path == path })
}

// Settings is everything a client runs on: the bridge's config, which kithd keeps,
// and this device's. Its property and record methods reach each path in its own file.
type Settings struct {
	Bridge Config
	Device Device
}

// Clone returns a deep copy of s.
func (s Settings) Clone() Settings {
	return Settings{Bridge: s.Bridge.Clone(), Device: s.Device.Clone()}
}

// root is the file path is in, as a value of s settable when s is, and its name.
func (s *Settings) root(path string) (reflect.Value, string) {
	if devicePaths()[path] {
		return reflect.ValueOf(&s.Device).Elem(), DeviceFile
	}
	return reflect.ValueOf(&s.Bridge).Elem(), BridgeFile
}

// devicePaths are the device's properties and record tables, by path.
var devicePaths = sync.OnceValue(func() map[string]bool {
	out := map[string]bool{}
	for _, p := range Properties() {
		out[p.Path] = out[p.Path] || p.File == DeviceFile
	}
	for _, t := range RecordTables() {
		out[t.Path] = out[t.Path] || t.File == DeviceFile
	}
	return out
})

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
	case t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Float64:
		return PropertyOptionalFloat, true
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
func (s Settings) Value(path string) (text string, set, ok bool) {
	root, file := s.root(path)
	v, ok := field(root, path)
	if !ok || !isProperty(file, path) {
		return "", false, false
	}
	text, set = valueText(v)
	return text, set, true
}

// valueText is a property field's value as text, and whether it is set.
func valueText(v reflect.Value) (text string, set bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return "", false
		}
		return fmt.Sprint(v.Elem().Interface()), true
	case reflect.Slice:
		return strings.Join(v.Interface().([]string), ", "), v.Len() > 0 //nolint:forcetypeassert // kindOf admits []string alone
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64), !v.IsZero()
	default:
		return fmt.Sprint(v.Interface()), !v.IsZero()
	}
}

// List is the list property at path; nil when path is no list.
func (s Settings) List(path string) []string {
	root, file := s.root(path)
	v, ok := field(root, path)
	if !ok || v.Kind() != reflect.Slice || !isProperty(file, path) {
		return nil
	}
	return slices.Clone(v.Interface().([]string)) //nolint:forcetypeassert // as Value
}

// SetValue writes text into the property at path, parsed by its kind. "" unsets an
// optional and zeroes the rest, which is the default a file without the key has.
func (s *Settings) SetValue(path, text string) error {
	root, file := s.root(path)
	return setValue(root, file, path, text)
}

// setValue is SetValue in the file root, named file.
func setValue(root reflect.Value, file, path, text string) error {
	v, ok := field(root, path)
	if !ok || !isProperty(file, path) {
		return fmt.Errorf("config: %s is not a setting", path)
	}
	return setText(v, text)
}

// setText parses text into a property field by its kind; "" zeroes it.
func setText(v reflect.Value, text string) error {
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
	case PropertyFloat, PropertyOptionalFloat:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", text)
		}
		setScalar(v, reflect.ValueOf(f))
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
		return fmt.Errorf("config: %s is not a setting", v.Type())
	}
	return nil
}

// SetList writes the list property at path; an empty list unsets it.
func (s *Settings) SetList(path string, entries []string) error {
	root, file := s.root(path)
	v, ok := field(root, path)
	if !ok || v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.String || !isProperty(file, path) {
		return fmt.Errorf("config: %s is not a list", path)
	}
	if len(entries) == 0 {
		v.SetZero()
		return nil
	}
	v.Set(reflect.ValueOf(slices.Clone(entries)))
	return nil
}

// setScalar sets v, or a freshly allocated pointee so a cloned config shares nothing.
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
	arrayLine = regexp.MustCompile(`^(?:# ?)?\[\[([a-z_.]+)\]\]`)
	// keyLine is `key = value`, or `# key = value` for a default left commented out;
	// an example inside a comment is indented further ("#   key = …") and is no key.
	keyLine = regexp.MustCompile(`^(?:# ?)?([a-z_0-9]+)\s*=\s*(.*)$`)
)

// documented reads each key's doc and value from a file's annotated default. The doc
// is the comment directly above the key; else its paragraph's opening comment; else,
// in a table's first paragraph, the key's entry in the header comment ("#   mode
// ...") or the whole header. A key written twice keeps the first.
func documented(f settingsFile) map[string]doc {
	r := docReader{docs: map[string]doc{}, live: map[string]bool{}, headers: map[string][]string{}}
	for raw := range strings.SplitSeq(f.annotated, "\n") {
		r.line(strings.TrimRight(raw, " \t"))
	}
	// A key a table's header describes but the file never writes ("#   max_width ...").
	walkProperties(f.typ, nil, func(path []string, _ PropertyKind) {
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
	inArray         bool     // inside an [[array]] block, whose keys are its records' fields
	array           string   // that block's table
	exampleArray    bool     // a commented [[array]], which ends at the next blank line
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
		r.array = arrayLine.FindStringSubmatch(line)[1]
		if _, ok := r.docs[r.array]; !ok && len(r.own) > 0 {
			r.docs[r.array] = doc{text: joinDoc(r.own)} // the table's text, above its first record
		}
		r.own, r.para, r.headerParagraph = nil, nil, false
	case tableLine.MatchString(line):
		r.table = tableLine.FindStringSubmatch(line)[1]
		r.inArray, r.exampleArray, r.header, r.headerParagraph = false, false, r.own, true
		r.headers[r.table], r.own, r.para = r.own, nil, nil
	default:
		if m := keyLine.FindStringSubmatch(line); len(m) > 2 && isValue(m[2]) {
			if r.inArray {
				r.field(m[1], m[2])
				return
			}
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

// field records a field of a record table from its example: its trailing comment, else
// the comment above it; the first example of a field that says something wins.
func (r *docReader) field(name, rest string) {
	path := r.array + "." + name
	_, trailing := splitTrailing(rest)
	text := trailing
	if text == "" {
		text = joinDoc(r.own)
	}
	if d, seen := r.docs[path]; !seen || (d.text == "" && text != "") {
		r.docs[path] = doc{text: text}
	}
	r.own = nil
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

// isValue reports whether what follows `key =` is a TOML value or a multi-line list
// opening, so prose like "# send_receipts = false still moves..." stays a comment.
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
