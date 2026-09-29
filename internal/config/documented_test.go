package config

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// configLeaves lists every setting Config reads as its dotted TOML path, with the
// table it lives in. An array of tables ([[display.name]]) is a table of its own.
func configLeaves(t reflect.Type, table string, out map[string]string) {
	for f := range t.Fields() {
		name := tomlName(f)
		if name == "" {
			continue
		}
		path := strings.TrimPrefix(table+"."+name, ".")
		ft := f.Type
		if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			configLeaves(ft, path, out)
			continue
		}
		out[path] = table
	}
}

// Every setting is documented in default.toml: its key is named — set, in a
// commented example, or in prose — inside its own table's text. A table's text runs
// from the comment block above each of its headers (live or commented) to the next
// live header.
func TestEveryConfigFieldIsDocumented(t *testing.T) {
	t.Parallel()

	leaves := map[string]string{}
	configLeaves(reflect.TypeFor[Config](), "", leaves)
	// ModelPrompt.Budget is read for summary and todo only; documenting it for these
	// two would promise an effect it does not have.
	for path := range leaves {
		if path == "assist.rewrite.budget" || path == "assist.thread_name.budget" {
			delete(leaves, path)
		}
	}

	lines := strings.Split(defaultConfigTOML, "\n")
	header := regexp.MustCompile(`^(#\s*)?\[\[?([\w.]+)\]\]?\s*$`)
	liveHeaderAfter := func(from int) int {
		for j := from; j < len(lines); j++ {
			if m := header.FindStringSubmatch(lines[j]); len(m) > 2 && m[1] == "" {
				return j
			}
		}
		return len(lines)
	}
	regionOf := func(table string) string {
		if table == "" {
			return strings.Join(lines[:liveHeaderAfter(0)], "\n")
		}
		var region []string
		for i, line := range lines {
			if m := header.FindStringSubmatch(line); len(m) < 3 || m[2] != table {
				continue
			}
			start := i
			for start > 0 && strings.HasPrefix(lines[start-1], "#") {
				start--
			}
			region = append(region, lines[start:liveHeaderAfter(i+1)]...)
		}
		return strings.Join(region, "\n")
	}
	regions := map[string]string{}
	for path, table := range leaves {
		key := path[strings.LastIndex(path, ".")+1:]
		region, ok := regions[table]
		if !ok {
			region = regionOf(table)
			regions[table] = region
		}
		if region == "" {
			t.Errorf("%s: default.toml never shows its table [%s]", path, table)
			continue
		}
		if !regexp.MustCompile(`(^|[^\w])` + regexp.QuoteMeta(key) + `([^\w]|$)`).MatchString(region) {
			t.Errorf("%s: default.toml does not document %q under [%s]", path, key, table)
		}
	}
}
