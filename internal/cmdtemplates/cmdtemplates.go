// Package cmdtemplates is the commands kith ships ready to add: each is a script and its
// [[command]] block, kept as files under templates/<name>/ and embedded, so adding one
// adds files, not Go.
package cmdtemplates

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/EugeneShtoka/kith/internal/config"
)

//go:embed templates
var files embed.FS

// Template is one ready-made command.
type Template struct {
	Name   string         // the folder's name
	About  string         // one line, shown before it is picked: what it does and what it needs
	Block  config.Command // written into the config as is
	Script []byte         // written to the commands folder, executable, under Block.Name
}

// spec is a template's template.toml.
type spec struct {
	About   string         `toml:"about"`
	Command config.Command `toml:"command"`
}

// All is every template, by name.
func All() ([]Template, error) {
	dirs, err := fs.ReadDir(files, "templates")
	if err != nil {
		return nil, fmt.Errorf("cmdtemplates: %w", err)
	}
	out := make([]Template, 0, len(dirs))
	for _, dir := range dirs {
		t, err := read(dir.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func read(name string) (Template, error) {
	dir := path.Join("templates", name)
	raw, err := files.ReadFile(path.Join(dir, "template.toml"))
	if err != nil {
		return Template{}, fmt.Errorf("cmdtemplates: %s: %w", name, err)
	}
	var s spec
	if _, err = toml.Decode(string(raw), &s); err != nil {
		return Template{}, fmt.Errorf("cmdtemplates: %s: %w", name, err)
	}
	script, err := files.ReadFile(path.Join(dir, "script"))
	if err != nil {
		return Template{}, fmt.Errorf("cmdtemplates: %s: %w", name, err)
	}
	return Template{Name: name, About: s.About, Block: s.Command, Script: script}, nil
}
