package spell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// The engine is a subprocess (kith is cgo-free) speaking the ispell pipe protocol.
// hunspell is the default because `-d d1,d2` loads several dictionaries into one
// process. Engines are often installed without dictionaries, so saying why checking
// is off matters as much as checking.

// Engine is a spellchecker binary that speaks the ispell pipe protocol.
type Engine struct {
	Command string // as configured, for messages
	// Path is where it was found; empty when not.
	Path string
}

// Dictionary is one installed dictionary, named the way the engine wants it.
type Dictionary struct {
	Tag string // "en_US", as passed to -d
	Dir string // where its .aff/.dic pair lives
}

// Availability is whether checking can happen and, if not, why.
type Availability struct {
	Engine       Engine
	Dictionaries []Dictionary
	// Searched is where dictionaries were looked for, for the message.
	Searched []string
}

// Ready reports whether checking can actually happen.
func (a Availability) Ready() bool { return a.Engine.Path != "" && len(a.Dictionaries) > 0 }

// Tags are the dictionary names, in the order they will be handed to the engine.
func (a Availability) Tags() []string {
	out := make([]string, 0, len(a.Dictionaries))
	for _, d := range a.Dictionaries {
		out = append(out, d.Tag)
	}
	return out
}

// Dirs are the directories of the found dictionaries, nearest first, deduplicated.
// Passed as DICPATH instead of SearchPaths so a same-named system dictionary cannot
// answer for the one Look chose.
func (a Availability) Dirs() []string {
	var out []string
	seen := make(map[string]bool, len(a.Dictionaries))
	for _, d := range a.Dictionaries {
		if d.Dir == "" || seen[d.Dir] {
			continue
		}
		seen[d.Dir] = true
		out = append(out, d.Dir)
	}
	return out
}

// Look finds the engine and the dictionaries, narrowing to want when non-empty.
// Unknown names in want are returned as missing (typically a config typo).
func Look(command string, want []string, dirs []string) (Availability, []string) {
	if command == "" {
		command = "hunspell"
	}
	avail := Availability{Engine: Engine{Command: command}, Searched: dirs}
	if path, err := exec.LookPath(command); err == nil {
		avail.Engine.Path = path
	}
	found := scan(dirs)
	if len(want) == 0 {
		avail.Dictionaries = found
		return avail, nil
	}
	byTag := make(map[string]Dictionary, len(found))
	for _, d := range found {
		byTag[d.Tag] = d
	}
	var missing []string
	for _, tag := range want {
		if d, ok := byTag[tag]; ok {
			avail.Dictionaries = append(avail.Dictionaries, d)
			continue
		}
		missing = append(missing, tag)
	}
	return avail, missing
}

// scan collects the dictionaries in dirs; a tag needs both an .aff and a .dic.
func scan(dirs []string) []Dictionary {
	seen := map[string]bool{}
	var out []Dictionary
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // a search-path entry that does not exist here holds nothing
		}
		affixes := map[string]bool{}
		dicts := map[string]bool{}
		for _, e := range entries {
			name := e.Name()
			switch filepath.Ext(name) {
			case ".aff":
				affixes[strings.TrimSuffix(name, ".aff")] = true
			case ".dic":
				dicts[strings.TrimSuffix(name, ".dic")] = true
			}
		}
		tags := make([]string, 0, len(affixes))
		for tag := range affixes {
			if dicts[tag] && !seen[tag] {
				tags = append(tags, tag)
			}
		}
		sort.Strings(tags)
		for _, tag := range tags {
			seen[tag] = true
			out = append(out, Dictionary{Tag: tag, Dir: dir})
		}
	}
	return out
}

// SearchPaths is where dictionaries are looked for, nearest first: kith's own
// (where it installs them), then DICPATH, then system directories.
func SearchPaths(dataDir string) []string {
	var dirs []string
	if dataDir != "" {
		dirs = append(dirs, filepath.Join(dataDir, "hunspell"))
	}
	if env := os.Getenv("DICPATH"); env != "" {
		dirs = append(dirs, filepath.SplitList(env)...)
	}
	return append(dirs,
		"/usr/share/hunspell",
		"/usr/share/myspell",
		"/usr/share/myspell/dicts",
		"/usr/local/share/hunspell",
		"/Library/Spelling",
		filepath.Join(os.Getenv("HOME"), "Library", "Spelling"),
	)
}

// Why explains an Availability that is not Ready, or is "" when it is.
func (a Availability) Why(distro string) string {
	switch {
	case a.Ready():
		return ""
	case a.Engine.Path == "":
		return fmt.Sprintf("spellcheck is off — %s was not found.\n%s",
			a.Engine.Command, installHint(distro, a.Engine.Command))
	default:
		return "spellcheck is off — no dictionaries found.\n" +
			"        Searched: " + strings.Join(a.Searched, ", ")
	}
}

// installHint is the install command for distro, or generic advice when unknown.
func installHint(distro, command string) string {
	switch distro {
	case "arch", "manjaro", "endeavouros":
		return "        Install it with:  sudo pacman -S " + command
	case "debian", "ubuntu", "linuxmint", "pop":
		return "        Install it with:  sudo apt install " + command
	case "fedora", "rhel", "centos":
		return "        Install it with:  sudo dnf install " + command
	case "opensuse", "opensuse-tumbleweed", "opensuse-leap":
		return "        Install it with:  sudo zypper install " + command
	case "alpine":
		return "        Install it with:  sudo apk add " + command
	case "darwin":
		return "        Install it with:  brew install " + command
	}
	return "        Install " + command + " with your package manager."
}

// Distro is the ID from /etc/os-release, or "".
func Distro() string {
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		return osReleaseID(string(data))
	}
	return ""
}

// osReleaseID pulls ID= out of an os-release file, unquoted.
func osReleaseID(data string) string {
	for line := range strings.SplitSeq(data, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ID="); ok {
			return strings.Trim(strings.TrimSpace(rest), `"'`)
		}
	}
	return ""
}
