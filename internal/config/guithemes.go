package config

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// A graphical theme is a file, <name>.toml in the themes folder beside the config
// (GUIThemesDir), that [gui.theme] mode names: a base mode the window starts from and its
// roles' colors, as [gui.theme.light] and [gui.theme.dark] hold them. kith ships light and
// dark (the window's own, which "system" follows) and cobalt (themes/); a file of the same
// name takes the place of one.

//go:embed themes/*.toml
var shippedThemes embed.FS

// GUIThemeFile is one theme file: its base ("light" or "dark") and its roles' colors.
type GUIThemeFile struct {
	Base string `toml:"base"`
	GUIRoles
}

// GUIThemesDir is where a config's theme files are: themes/ beside it.
func GUIThemesDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "themes")
}

// LoadGUIThemes are the themes kith ships and those in dir, by name; one in dir takes
// the place of a shipped one of its name. A dir that is not there holds none.
func LoadGUIThemes(dir string) (map[string]GUIThemeFile, error) {
	out := map[string]GUIThemeFile{}
	shipped, err := fs.Glob(shippedThemes, "themes/*.toml")
	if err != nil {
		return nil, fmt.Errorf("config: the shipped themes: %w", err)
	}
	for _, name := range shipped {
		data, rerr := shippedThemes.ReadFile(name)
		if rerr != nil {
			return nil, fmt.Errorf("config: the shipped theme %s: %w", name, rerr)
		}
		if derr := decodeTheme(out, name, data); derr != nil {
			return nil, derr
		}
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: the themes in %s: %w", dir, err)
	}
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".toml" {
			continue
		}
		path := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(path) // #nosec G304 -- the user's own themes folder
		if err != nil {
			return nil, fmt.Errorf("config: theme %s: %w", path, err)
		}
		if err := decodeTheme(out, path, data); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// decodeTheme adds the theme in file (its name the file's, without .toml) to out.
func decodeTheme(out map[string]GUIThemeFile, file string, data []byte) error {
	var t GUIThemeFile
	meta, err := toml.Decode(string(data), &t)
	if err != nil {
		return fmt.Errorf("config: theme %s: %w", file, err)
	}
	if extra := meta.Undecoded(); len(extra) > 0 {
		return fmt.Errorf("config: theme %s: no key %s", file, extra[0])
	}
	out[strings.TrimSuffix(filepath.Base(file), ".toml")] = t
	return nil
}

// Role is the color of the role named name, "" for none set (or no role).
func (r GUIRoles) Role(name string) string {
	if p := r.role(name); p != nil {
		return *p
	}
	return ""
}
