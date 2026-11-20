package crdt

import (
	"fmt"
	"slices"
	"strings"
)

// The shared config: each key of config.toml a mesh shares is a register, config/<TOML
// path>, holding the key's value as canonical TOML (internal/config reads it). Of
// concurrent writes the newest by (clock, origin) is applied and all are kept: written
// over with different values they are a conflict, shown until a write that has seen
// them all. Any bridge writes any of them; a key is never deleted, only written unset.
const configPrefix = "config/"

// ConfigKey is the key of the config register of the TOML path.
func ConfigKey(path string) string { return configPrefix + path }

// readConfigWrite checks w as a config write, reporting whether it is one: a path,
// holding a value.
func readConfigWrite(w Write) (bool, error) {
	path, ok := strings.CutPrefix(w.Key, configPrefix)
	switch {
	case !ok:
		return false, nil
	case path == "" || len(w.Value) == 0:
		return true, fmt.Errorf("%w: %s holds no config value", ErrBadWrite, w.Key)
	}
	return true, nil
}

// Config is each config register's entries by its TOML path, the applied one first:
// newest by (clock, origin), then the others newest first.
func (s State) Config() map[string][]Entry {
	out := map[string][]Entry{}
	for key, entries := range s.Entries {
		path, ok := strings.CutPrefix(key, configPrefix)
		if !ok || len(entries) == 0 {
			continue
		}
		sorted := slices.Clone(entries)
		slices.SortFunc(sorted, func(a, b Entry) int {
			if later(a, b) {
				return -1
			}
			return 1
		})
		out[path] = sorted
	}
	return out
}
