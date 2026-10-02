package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	sectionLine  = regexp.MustCompile(`^\s*\[\s*([A-Za-z0-9_.-]+)\s*\]\s*(#.*)?$`)
	instanceLine = regexp.MustCompile(`^\s*instance\s*=\s*"([^"]*)"\s*(#.*)?$`)
)

// SetInstance records [storage] instance in the config file at path, leaving the
// rest of the file — comments included — as it is. When the file already names one
// (another kith got there first), that one is returned and nothing is written.
func SetInstance(path, instance string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the user's own config file
	if err != nil {
		return "", fmt.Errorf("config: read %s: %w", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	section, header, empty := "", -1, -1
	for i, line := range lines {
		if m := sectionLine.FindStringSubmatch(line); m != nil {
			section = m[1]
			if section == "storage" {
				header = i
			}
			continue
		}
		if section != "storage" {
			continue
		}
		if m := instanceLine.FindStringSubmatch(line); m != nil {
			if m[1] != "" {
				return m[1], nil
			}
			empty = i
		}
	}
	entry := `instance = "` + instance + `"`
	switch {
	case empty >= 0:
		lines[empty] = entry
	case header >= 0:
		lines = append(lines[:header+1], append([]string{entry}, lines[header+1:]...)...)
	default:
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "", "[storage]", entry, "")
	}
	if err := writeAtomic(path, strings.Join(lines, "\n")); err != nil {
		return "", err
	}
	return instance, nil
}
