// Package phonebook is a folder of Markdown files, one per person, that kith shares
// with whatever else reads them (a mail client, an Obsidian vault). A file's YAML front
// matter holds the person: the keys in IdentifierKeys say who it is about, every other
// key is a field of theirs. The body is the person's own notes, never touched.
//
// A file is found by its identifiers, not its name, so renaming it elsewhere breaks
// nothing. Files are read afresh on every call: another program may have changed them.
// A change rewrites the front matter with the one key changed, the rest kept in their
// order with their comments, and the body byte for byte, through a rename so a reader
// never sees half a file.
package phonebook

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// IdentifierKeys are the front-matter keys that say who a file is about, as every app
// sharing the folder reads them: a phone number in international form, an email
// address, and a person's ID on each network (a Matrix user ID, a Telegram user ID, a
// WhatsApp number's ID, a Slack ID as team.user).
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

// New is the book in dir. It reads nothing until asked.
func New(dir string) *Book { return &Book{dir: dir} }

// Dir is the folder.
func (b *Book) Dir() string { return b.dir }

// errNoFrontMatter is a file that does not start with a front matter block.
var errNoFrontMatter = errors.New("no front matter")

// Find is the first card, by file name, whose identifiers share one with ids; false for
// none. A folder that does not exist yet holds no one.
func (b *Book) Find(ids Identifiers) (Card, bool, error) {
	cards, err := b.cards()
	if err != nil {
		return Card{}, false, err
	}
	for i := range cards {
		if matches(cards[i].IDs, ids) {
			return cards[i], true, nil
		}
	}
	return Card{}, false, nil
}

// Keys is every field key the folder's cards use, sorted, identifiers left out.
func (b *Book) Keys() ([]string, error) {
	cards, err := b.cards()
	if err != nil {
		return nil, err
	}
	var keys []string
	for i := range cards {
		for _, f := range cards[i].Fields {
			if !slices.Contains(keys, f.Key) {
				keys = append(keys, f.Key)
			}
		}
	}
	slices.Sort(keys)
	return keys, nil
}

// Create writes a new card for a person: named name (made safe for a file name, and
// numbered when taken), with their identifiers and the person tag.
func (b *Book) Create(name string, ids Identifiers) (Card, error) {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return Card{}, fmt.Errorf("phonebook: make %s: %w", b.dir, err)
	}
	base := fileName(name)
	path := filepath.Join(b.dir, base+".md")
	for n := 2; exists(path); n++ {
		path = filepath.Join(b.dir, fmt.Sprintf("%s %d.md", base, n))
	}
	doc := &yaml.Node{Kind: yaml.MappingNode}
	for _, key := range IdentifierKeys {
		if values := ids[key]; len(values) > 0 {
			setKey(doc, key, values, true)
		}
	}
	setKey(doc, "tags", []string{"person"}, true)
	if err := write(path, doc, nil); err != nil {
		return Card{}, err
	}
	return read(path)
}

// Set writes one key of the card at path: values as a list when list, else the one
// value; no values removes the key. The rest of the file is kept.
func (b *Book) Set(path, key string, values []string, list bool) (Card, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a card the folder holds
	if err != nil {
		return Card{}, fmt.Errorf("phonebook: read %s: %w", path, err)
	}
	doc, body, err := parse(raw)
	if err != nil {
		return Card{}, fmt.Errorf("phonebook: %s: %w", path, err)
	}
	if len(values) == 0 {
		removeKey(doc, key)
	} else {
		setKey(doc, key, values, list)
	}
	if err := write(path, doc, body); err != nil {
		return Card{}, err
	}
	return read(path)
}

// cards is every card in the folder, by file name; a file that is not one (no front
// matter, or front matter that is not a mapping) is left out.
func (b *Book) cards() ([]Card, error) {
	entries, err := os.ReadDir(b.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("phonebook: read %s: %w", b.dir, err)
	}
	var out []Card
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		card, err := read(filepath.Join(b.dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, card)
	}
	return out, nil
}

// read is the card in one file.
func read(path string) (Card, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a card the folder holds
	if err != nil {
		return Card{}, fmt.Errorf("phonebook: read %s: %w", path, err)
	}
	doc, _, err := parse(raw)
	if err != nil {
		return Card{}, fmt.Errorf("phonebook: %s: %w", path, err)
	}
	card := Card{Path: path, Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), IDs: Identifiers{}}
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
		return nil, nil, fmt.Errorf("front matter: %w", err)
	}
	if len(file.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, body, nil
	}
	if doc := file.Content[0]; doc.Kind == yaml.MappingNode {
		return doc, body, nil
	}
	return nil, nil, errors.New("front matter is not a mapping")
}

// write puts front matter and body into path through a rename.
func write(path string, doc *yaml.Node, body []byte) error {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	if len(doc.Content) > 0 {
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return fmt.Errorf("phonebook: write %s: %w", path, err)
		}
		if err := enc.Close(); err != nil {
			return fmt.Errorf("phonebook: write %s: %w", path, err)
		}
	}
	buf.WriteString("---\n")
	buf.Write(body)
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("phonebook: write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone once renamed
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("phonebook: write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("phonebook: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("phonebook: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("phonebook: write %s: %w", path, err)
	}
	return nil
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

// matches reports whether two sets of identifiers share one: a phone number by its
// digits, an email in any case, the rest as written.
func matches(a, b Identifiers) bool {
	for key, values := range a {
		for _, v := range values {
			for _, w := range b[key] {
				if same(key, v, w) {
					return true
				}
			}
		}
	}
	return false
}

func same(key, a, b string) bool {
	switch key {
	case "phone":
		da, db := digits(a), digits(b)
		return da != "" && da == db
	case "email":
		return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	return strings.TrimSpace(a) == strings.TrimSpace(b) && strings.TrimSpace(a) != ""
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

// fileName is a person's name as a file may be called: what a file system or a vault
// would refuse taken out.
func fileName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '#', '^', '[', ']':
			return -1
		}
		if r < ' ' {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.Trim(strings.TrimSpace(name), ".")
	if name == "" {
		return "Person"
	}
	return name
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
