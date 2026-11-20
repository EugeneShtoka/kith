package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// The bridges of a mesh share config.toml (planning/design-mesh.md, Config): each leaf
// key of a table, and each array whole ([[tag]] and every list of tables included), is
// one register, named by its TOML path and holding its value as canonical TOML. The keys
// that describe this machine (Local) stay in the file and never cross.

// Local is every key that names this machine: a path, an address, an account it runs,
// how it logs or backs up. A key at or under one of these TOML paths stays in the file;
// every other key is shared by the bridges of a mesh. A new key that names this machine
// is added here.
var Local = []string{
	"allow_token_file", "bridge_contacts", "storage", "remote", "log", "backup",
	"commands.dir", "phonebook.dir", "spell.command",
	"complete.model.command", "complete.model.model", "complete.model.threads", "complete.model.port",
	"matrix.account", "matrix.default", "whatsapp.account", "whatsapp.default",
	"slack.account", "slack.default", "telegram.account", "telegram.default",
	"messenger.account", "messenger.default", "linkedin.account", "linkedin.default",
	"gmessages.account", "gmessages.default", "signal.account", "signal.default",
	"instagram.account", "instagram.default",
}

// IsLocal reports whether the key at path (as Leaves names it) stays on this machine.
func IsLocal(path string) bool {
	return slices.ContainsFunc(Local, func(l string) bool { return path == l || strings.HasPrefix(path, l+".") })
}

// Unset is the value of a key written as unset: it reads as its default. It is a value
// of its own, not a deletion, so an unset concurrent with an edit is a conflict.
var Unset = []byte("# unset\n")

// leafValue is the one key a leaf's value holds.
const leafValue = "value"

// Leaves is cfg as registers: the TOML path of each leaf key and each array that differs
// from its default, and its value. Each value is canonical, so equal settings are equal
// bytes however the file spelled them.
func Leaves(cfg Config) (map[string][]byte, error) {
	var tree map[string]any
	if _, err := toml.Decode(Encode(cfg), &tree); err != nil {
		return nil, fmt.Errorf("config: read the config as keys: %w", err)
	}
	out := map[string][]byte{}
	var walk func(prefix string, table map[string]any) error
	walk = func(prefix string, table map[string]any) error {
		for key, v := range table {
			path := prefix + pathPart(key)
			if sub, ok := v.(map[string]any); ok {
				if err := walk(path+".", sub); err != nil {
					return err
				}
				continue
			}
			value, err := toml.Marshal(map[string]any{leafValue: v})
			if err != nil {
				return fmt.Errorf("config: write %s as a key: %w", path, err)
			}
			out[path] = value
		}
		return nil
	}
	return out, walk("", tree)
}

// bareKey is a TOML key written without quotes.
var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// pathPart is one key of a path, quoted when TOML needs it.
func pathPart(key string) string {
	if bareKey.MatchString(key) {
		return key
	}
	return quoteTOML(key)
}

// errNotAPath is a register named by no TOML path.
var errNotAPath = errors.New("config: not a key's path")

// pathKeys is the keys of a path Leaves names, read as TOML reads them.
func pathKeys(path string) ([]string, error) {
	var tree map[string]any
	if _, err := toml.Decode(path+" = 0", &tree); err != nil {
		return nil, fmt.Errorf("%w: %q", errNotAPath, path)
	}
	var keys []string
	for len(tree) == 1 {
		for key, v := range tree {
			keys = append(keys, key)
			tree, _ = v.(map[string]any)
		}
	}
	return keys, nil
}

// LeafValue is a leaf's value as it reads, and whether it holds one (false: Unset).
func LeafValue(value []byte) (any, bool, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(value), &doc); err != nil {
		return nil, false, fmt.Errorf("config: read a key's value: %w", err)
	}
	v, ok := doc[leafValue]
	return v, ok, nil
}

// LeafText is a leaf's value as TOML writes it after `key = `, for showing; "unset"
// for Unset.
func LeafText(value []byte) string {
	text, ok := strings.CutPrefix(strings.TrimSpace(string(value)), leafValue+" = ")
	switch {
	case ok:
		return text
	case strings.HasPrefix(string(value), "#"):
		return "unset"
	}
	return strings.TrimSpace(string(value)) // a list of tables, as its [[value]] sections
}

// FromLeaves is the config leaves make: each key's value, every key absent or Unset at
// its default. One that does not decode, or a set that is no config, is the error.
func FromLeaves(leaves map[string][]byte) (Config, error) {
	tree := map[string]any{}
	for _, path := range slices.Sorted(maps.Keys(leaves)) {
		v, set, err := LeafValue(leaves[path])
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		if !set {
			continue
		}
		keys, err := pathKeys(path)
		if err != nil {
			return Config{}, err
		}
		if err := put(tree, keys, v); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	text, err := toml.Marshal(tree)
	if err != nil {
		return Config{}, fmt.Errorf("config: write the keys as a config: %w", err)
	}
	return decode("the mesh's config", string(text))
}

// errKeyClash is two keys where one is a table the other holds a value at.
var errKeyClash = errors.New("config: a key is both a value and a table")

// put sets the value at keys in tree, making the tables on the way.
func put(tree map[string]any, keys []string, v any) error {
	for _, key := range keys[:len(keys)-1] {
		sub, ok := tree[key].(map[string]any)
		if !ok {
			if _, taken := tree[key]; taken {
				return errKeyClash
			}
			sub = map[string]any{}
			tree[key] = sub
		}
		tree = sub
	}
	last := keys[len(keys)-1]
	if _, taken := tree[last]; taken {
		return errKeyClash
	}
	tree[last] = v
	return nil
}
