// Package phonebook is a folder of Markdown files, one per person, shared with other
// programs (a mail client, an Obsidian vault). YAML front matter holds the person: the
// IdentifierKeys say who it is, every other key is a field. The body is the user's
// notes and is never touched.
//
// Files are matched by identifiers, not name, and re-read on every call since other
// programs may change them. A write changes one key, keeps other keys' order and
// comments and the body byte for byte, and lands via rename so no reader sees half a file.
// It is made over the file as it was read (Card.Revision): a file changed, renamed or
// removed since is not written (ErrMoved).
package phonebook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// IdentifierKeys are the front-matter keys that identify a person: phone in
// international form, email, and per-network IDs (Matrix user ID, Telegram user ID,
// WhatsApp number's ID, Slack ID as team.user).
var IdentifierKeys = []string{"phone", "email", "matrix", "telegram", "whatsapp", "slack"}

// Identifiers are a person's identifiers, by key.
type Identifiers map[string][]string

// Card is one person's file.
type Card struct {
	// Path is the file; empty for a person with no file yet.
	Path string
	// Name is the file's name without .md.
	Name string
	IDs  Identifiers
	// Fields are the other keys, in the file's order.
	Fields []Field
	// Revision is the file as read, its name and its bytes: a write is made over it alone.
	Revision string
}

// Field is one of a person's other keys.
type Field struct {
	Key    string
	Values []string
	// List is whether the file writes it as a list.
	List bool
}

// Book is the folder.
type Book struct{ dir string }

// New returns the book in dir; it reads nothing until asked.
func New(dir string) *Book { return &Book{dir: dir} }

// Dir is the folder.
func (b *Book) Dir() string { return b.dir }

var (
	// errNoFrontMatter is a file that does not start with a front matter block: no card.
	errNoFrontMatter = errors.New("no front matter")
	// ErrMalformed is a card whose front matter kith cannot read: it is not YAML, or not
	// keys and values. It is never written.
	ErrMalformed = errors.New("its front matter is not YAML keys and values")
	// ErrMoved is a write refused because the card is no longer as it was read: another
	// program wrote, renamed or removed it, or made the person a card, since.
	ErrMoved = errors.New("phonebook: the card changed since it was read")
	// ErrField is a key or a value no card may hold.
	ErrField = errors.New("phonebook: not a field a card holds")
)

// Unreadable is a card of the folder kith cannot read (ErrMalformed).
type Unreadable struct {
	// Name is the file's name without .md.
	Name string
	Err  error
}

const (
	// maxName bounds a new card's name, in bytes, leaving room for " 1000.md" within the
	// 255 bytes a file name may have.
	maxName = 200
	// maxNumbered bounds the numbers a new card's name is tried with.
	maxNumbered = 1000
	// maxKey bounds a field's key, in bytes.
	maxKey = 128
)

// Find returns the first card, by file name, sharing an identifier with ids. A missing
// folder holds no one.
func (b *Book) Find(ids Identifiers) (Card, bool, error) {
	cards, err := b.cards()
	if err != nil {
		return Card{}, false, err
	}
	card, ok := NewIndex(cards).Find(ids)
	return card, ok, nil
}

// Cards returns every card, by file name. A missing folder holds no one.
func (b *Book) Cards() ([]Card, error) { return b.cards() }

// Scan returns every card, by file name, and the cards whose front matter cannot be
// read. A missing folder holds no one.
func (b *Book) Scan() ([]Card, []Unreadable, error) { return b.scan() }

// Read returns the card at path as the file has it now.
func (b *Book) Read(path string) (Card, error) { return read(path) }

// Keys is every field key the folder's cards use, sorted, identifiers left out.
func (b *Book) Keys() ([]string, error) {
	cards, err := b.cards()
	if err != nil {
		return nil, err
	}
	return NewIndex(cards).Keys(), nil
}

// Create writes a new card with ids and the person tag, named after name (made safe,
// numbered when taken). It never replaces a file: a name another program took meanwhile
// is passed over for the next number.
func (b *Book) Create(name string, ids Identifiers) (Card, error) {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return Card{}, fmt.Errorf("phonebook: make %s: %w", b.dir, err)
	}
	defer lockDir(b.dir)()
	return b.create(name, ids)
}

func (b *Book) create(name string, ids Identifiers) (Card, error) {
	doc := &yaml.Node{Kind: yaml.MappingNode}
	for _, key := range IdentifierKeys {
		if values := ids[key]; len(values) > 0 {
			setKey(doc, key, values, true)
		}
	}
	setKey(doc, "tags", []string{"person"}, true)
	base := fileName(name)
	tmp, err := stage(filepath.Join(b.dir, base+".md"), doc, nil, 0o600)
	if err != nil {
		return Card{}, err
	}
	// The card is a link of its own once made; the staged name goes either way.
	defer func() { _ = os.Remove(tmp) }()
	for n := 1; n <= maxNumbered; n++ {
		path := filepath.Join(b.dir, base+".md")
		if n > 1 {
			path = filepath.Join(b.dir, fmt.Sprintf("%s %d.md", base, n))
		}
		// A link is made only where nothing is: the file appears whole, never over another.
		err := os.Link(tmp, path)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return Card{}, fmt.Errorf("phonebook: write %s: %w", path, err)
		}
		return read(path)
	}
	return Card{}, fmt.Errorf("phonebook: %d cards are named %s already", maxNumbered, base)
}

// Set writes one key of the card as it was read (its Path and Revision), as a list when
// list; no values removes it. The rest of the file is kept. A file changed, renamed or
// removed since it was read is not written: ErrMoved.
func (b *Book) Set(card Card, key string, values []string, list bool) (Card, error) {
	defer lockDir(b.dir)()
	return b.set(card, key, values, list)
}

func (b *Book) set(card Card, key string, values []string, list bool) (Card, error) {
	if err := CheckField(key, values); err != nil {
		return Card{}, err
	}
	raw, err := readFile(card.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Card{}, ErrMoved
	}
	if err != nil {
		return Card{}, err
	}
	if revision(card.Path, raw) != card.Revision {
		return Card{}, ErrMoved
	}
	doc, body, err := parse(raw)
	if err != nil {
		return Card{}, fmt.Errorf("phonebook: %s: %w", filepath.Base(card.Path), err)
	}
	if len(values) == 0 {
		removeKey(doc, key)
	} else {
		setKey(doc, key, values, list)
	}
	if err := replace(card.Path, doc, body, raw); err != nil {
		return Card{}, err
	}
	return read(card.Path)
}

// Write sets one key on a person's card as it was read (Set). A person who had no card
// (read.Path "") is given one named after name with ids (Create), unless a card holding
// any of ids is there now: ErrMoved. No values for a person with no card writes nothing.
// kith's writers of the folder, in any process, write one at a time (lockDir), so two
// never both make a person's card.
func (b *Book) Write(read Card, name string, ids Identifiers, key string, values []string, list bool) (Card, error) {
	if err := CheckField(key, values); err != nil || (read.Path == "" && len(values) == 0) {
		return Card{}, err
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return Card{}, fmt.Errorf("phonebook: make %s: %w", b.dir, err)
	}
	defer lockDir(b.dir)()
	if read.Path != "" {
		return b.set(read, key, values, list)
	}
	_, found, err := b.Find(ids)
	if err != nil {
		return Card{}, err
	}
	if found {
		return Card{}, ErrMoved
	}
	made, err := b.create(name, ids)
	if err != nil {
		return Card{}, err
	}
	return b.set(made, key, values, list)
}

// CheckField reports whether a card may hold key with values: a key of text with no
// colon, #, or control character, not starting with "-" nor blank around it, at most
// maxKey bytes; values of text with no control character (a line break among them).
func CheckField(key string, values []string) error {
	switch {
	case key == "" || strings.TrimSpace(key) != key:
		return fmt.Errorf("%w: a field needs a name with no blank around it", ErrField)
	case len(key) > maxKey:
		return fmt.Errorf("%w: a field's name is at most %d bytes", ErrField, maxKey)
	case !utf8.ValidString(key) || strings.ContainsFunc(key, unicode.IsControl):
		return fmt.Errorf("%w: a field's name is one line of text", ErrField)
	case strings.ContainsAny(key, ":#") || strings.HasPrefix(key, "-"):
		return fmt.Errorf("%w: a field's name cannot hold : or #, nor start with -", ErrField)
	}
	for _, v := range values {
		if !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl) {
			return fmt.Errorf("%w: a value of %s is not one line of text", ErrField, key)
		}
	}
	return nil
}

// cards returns every card by file name, skipping files without mapping front matter.
func (b *Book) cards() ([]Card, error) {
	cards, _, err := b.scan()
	return cards, err
}

// scan returns every card by file name, and the cards that cannot be read: files whose
// front matter is not a YAML mapping. A file with no front matter is not a card.
func (b *Book) scan() ([]Card, []Unreadable, error) {
	entries, err := os.ReadDir(b.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("phonebook: read %s: %w", b.dir, err)
	}
	var out []Card
	var unreadable []Unreadable
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		card, err := read(filepath.Join(b.dir, e.Name()))
		switch {
		case errors.Is(err, ErrMalformed):
			unreadable = append(unreadable, Unreadable{Name: nameOf(e.Name()), Err: err})
		case err == nil:
			out = append(out, card)
		}
	}
	return out, unreadable, nil
}

// read is the card in one file.
func read(path string) (Card, error) {
	raw, err := readFile(path)
	if err != nil {
		return Card{}, err
	}
	doc, _, err := parse(raw)
	if err != nil {
		return Card{}, fmt.Errorf("phonebook: %s: %w", filepath.Base(path), err)
	}
	card := Card{Path: path, Name: nameOf(path), IDs: Identifiers{}, Revision: revision(path, raw)}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key, value := doc.Content[i].Value, doc.Content[i+1]
		values, list := valuesOf(value)
		if slices.Contains(IdentifierKeys, key) {
			card.IDs[key] = values
			continue
		}
		card.Fields = append(card.Fields, Field{Key: key, Values: values, List: list})
	}
	return card, nil
}

// readFile is a card's file as it is now.
func readFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("phonebook: read %s: %w", path, err)
	}
	return raw, nil
}

// nameOf is a card's name: its file's, without the extension.
func nameOf(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// revision is a card's file as read at path: its name and its bytes.
func revision(path string, raw []byte) string {
	h := sha256.New()
	h.Write([]byte(filepath.Base(path)))
	h.Write([]byte{0})
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// parse splits a file into its front matter, as a mapping node, and its body.
func parse(raw []byte) (*yaml.Node, []byte, error) {
	rest, ok := bytes.CutPrefix(raw, []byte("---\n"))
	if !ok {
		rest, ok = bytes.CutPrefix(raw, []byte("---\r\n"))
	}
	if !ok {
		return nil, nil, errNoFrontMatter
	}
	var front, body []byte
	switch {
	case bytes.HasPrefix(rest, []byte("---\n")) || bytes.Equal(rest, []byte("---")):
		front, body = nil, bytes.TrimPrefix(bytes.TrimPrefix(rest, []byte("---")), []byte("\n"))
	default:
		end := bytes.Index(rest, []byte("\n---\n"))
		closing := len("\n---\n")
		if end < 0 && bytes.HasSuffix(rest, []byte("\n---")) {
			end, closing = len(rest)-len("\n---"), len("\n---")
		}
		if end < 0 {
			return nil, nil, errNoFrontMatter
		}
		front, body = rest[:end+1], rest[end+closing:]
	}
	var file yaml.Node
	if err := yaml.Unmarshal(front, &file); err != nil {
		return nil, nil, fmt.Errorf("%w (%w)", ErrMalformed, err)
	}
	if len(file.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, body, nil
	}
	if doc := file.Content[0]; doc.Kind == yaml.MappingNode {
		return doc, body, nil
	}
	return nil, nil, ErrMalformed
}

// replace puts front matter and body into path through a rename, if the file still
// holds was when it is about to: else ErrMoved.
func replace(path string, doc *yaml.Node, body, was []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := stage(path, doc, body, mode)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }() // gone once renamed
	if now, err := readFile(path); err != nil || !bytes.Equal(now, was) {
		return ErrMoved
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("phonebook: write %s: %w", path, err)
	}
	return nil
}

// stage writes front matter and body to a hidden file beside path, with mode, and
// returns its name, for the caller to put in place and remove.
func stage(path string, doc *yaml.Node, body []byte, mode os.FileMode) (string, error) {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	if len(doc.Content) > 0 {
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return "", fmt.Errorf("phonebook: write %s: %w", path, err)
		}
		if err := enc.Close(); err != nil {
			return "", fmt.Errorf("phonebook: write %s: %w", path, err)
		}
	}
	buf.WriteString("---\n")
	buf.Write(body)
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return "", fmt.Errorf("phonebook: write %s: %w", path, err)
	}
	_, err = tmp.Write(buf.Bytes())
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if closed := tmp.Close(); err == nil {
		err = closed
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("phonebook: write %s: %w", path, err)
	}
	return tmp.Name(), nil
}

// valuesOf is a value node as strings: a list's items, or the one scalar; anything
// else as the YAML it is written as.
func valuesOf(n *yaml.Node) ([]string, bool) {
	switch n.Kind {
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			if item.Kind == yaml.ScalarNode {
				out = append(out, item.Value)
			} else if text, err := yaml.Marshal(item); err == nil {
				out = append(out, strings.TrimSpace(string(text)))
			}
		}
		return out, true
	case yaml.ScalarNode:
		if n.Tag == "!!null" || n.Value == "" {
			return nil, false
		}
		return []string{n.Value}, false
	default:
		text, _ := yaml.Marshal(n)
		return []string{strings.TrimSpace(string(text))}, false
	}
}

// setKey sets key in a mapping, in place where it is, else at the end.
func setKey(doc *yaml.Node, key string, values []string, list bool) {
	value := &yaml.Node{Kind: yaml.ScalarNode, Value: values[0]}
	if list || len(values) > 1 {
		value = &yaml.Node{Kind: yaml.SequenceNode}
		for _, v := range values {
			value.Content = append(value.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
		}
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == key {
			value.Style = doc.Content[i+1].Style &^ (yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle | yaml.LiteralStyle | yaml.FoldedStyle)
			value.HeadComment, value.LineComment, value.FootComment = doc.Content[i+1].HeadComment, doc.Content[i+1].LineComment, doc.Content[i+1].FootComment
			doc.Content[i+1] = value
			return
		}
	}
	doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

// removeKey drops key from a mapping.
func removeKey(doc *yaml.Node, key string) {
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == key {
			doc.Content = append(doc.Content[:i:i], doc.Content[i+2:]...)
			return
		}
	}
}

// identifier is an identifier as it is compared: a phone number by its digits, an email in
// any case, the rest as written; "" never matches.
func identifier(key, value string) string {
	value = strings.TrimSpace(value)
	switch key {
	case "phone":
		value = digits(value)
	case "email":
		value = strings.ToLower(value)
	}
	if value == "" {
		return ""
	}
	return key + "\x00" + value
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fileName strips from name what a file system or vault would refuse, or would show as
// other than it is (direction controls), and shortens it to maxName bytes; nothing left
// is "Person". It never holds a separator, nor is "." or "..", so a card's file is
// always one of the folder's own.
func fileName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '#', '^', '[', ']', utf8.RuneError:
			return -1
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	for len(name) > maxName {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	name = strings.Trim(strings.TrimSpace(name), ".")
	if name == "" {
		return "Person"
	}
	return name
}
