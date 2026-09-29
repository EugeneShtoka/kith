// Package buildinfo exposes version metadata, injected by -ldflags -X in release
// builds and otherwise read from the module build info ("dev" when absent).
package buildinfo

import "runtime/debug"

// Injected at release build time via -ldflags -X.
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

func version(bi *debug.BuildInfo) string {
	switch {
	case Version != "":
		return Version
	case bi != nil && bi.Main.Version != "" && bi.Main.Version != "(devel)":
		return bi.Main.Version
	}
	return "dev"
}

// injectedOr returns v, or the named vcs.* build setting when v is empty.
func injectedOr(v string, bi *debug.BuildInfo, key string) string {
	if v != "" || bi == nil {
		return v
	}
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

// String renders a multi-line version report for `<name> --version`.
func String(name string) string {
	bi, _ := debug.ReadBuildInfo()
	out := name + " " + version(bi)
	if c := injectedOr(Commit, bi, "vcs.revision"); c != "" {
		out += "\n  commit: " + c
	}
	if d := injectedOr(Date, bi, "vcs.time"); d != "" {
		out += "\n  built:  " + d
	}
	if bi != nil && bi.GoVersion != "" {
		out += "\n  go:     " + bi.GoVersion
	}
	return out
}
