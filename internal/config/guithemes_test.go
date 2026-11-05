package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The shipped themes are there with no folder; a file in it adds a theme or takes the
// place of a shipped one of its name; a key no theme has is refused.
func TestThemesAreShippedAndTheFoldersOwn(t *testing.T) {
	t.Parallel()
	got, err := LoadGUIThemes(filepath.Join(t.TempDir(), "none"))
	if err != nil || got["cobalt"].Text != "#8ff586" || got["light"].Base != "light" || got["dark"].Base != "dark" {
		t.Fatalf("with no folder: %v, %v; want light, dark and cobalt shipped", got, err)
	}
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if werr := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	write("paper.toml", "base = \"light\"\naccent = \"#aa0000\"\n")
	write("cobalt.toml", "base = \"dark\"\ntext = \"#ffffff\"\n")
	write("notes.txt", "not a theme")
	got, err = LoadGUIThemes(dir)
	if err != nil || got["paper"].Accent != "#aa0000" || got["cobalt"].Text != "#ffffff" || len(got) != 4 {
		t.Fatalf("with a folder: %v, %v; want paper beside light, dark and the folder's cobalt", got, err)
	}
	write("bad.toml", "base = \"dark\"\nglow = \"#ffffff\"\n")
	if _, err := LoadGUIThemes(dir); err == nil {
		t.Error("a theme with a key no theme has was taken")
	}
}
