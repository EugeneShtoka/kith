// Package models is the catalog of completion models kith can install: what each
// is, where it comes from and whether it is on disk. Data only: the download and the
// server that runs a model are in llamacpp.
package models

import (
	"os"
	"path/filepath"
	"sort"
)

// File is one downloadable file and the hash it must have.
type File struct {
	// Path is relative to modelBase.
	Path   string
	SHA256 string
	Size   int64
}

// Source is a model that can be installed, named by the tag it installs as.
type Source struct {
	// Tag is what a person types and the installed file's stem.
	Tag  string
	Name string
	File File
	// Recall is the measured share of probes where the typed word was offered.
	Recall  float64
	License string
}

// Bytes is the download size.
func (s Source) Bytes() int64 { return s.File.Size }

// Filename is the installed name: the tag plus the upstream extension.
func (s Source) Filename() string { return s.Tag + filepath.Ext(s.File.Path) }

// Installable lists the models that can be fetched, sorted.
func Installable() []string {
	out := make([]string, 0, len(modelSources))
	for tag := range modelSources {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// SourceFor returns the manifest entry for a tag.
func SourceFor(tag string) (Source, bool) {
	s, ok := modelSources[tag]
	return s, ok
}

// Pin describes where the weights come from, for the confirmation prompt.
func Pin() (repo, commit, date string) { return modelRepo, modelCommit, modelDate }

// Installed is the path to the weights for a tag, and false when they are missing or
// the wrong size (a truncated GGUF never becomes healthy).
func Installed(dir, tag string) (string, bool) {
	src, ok := modelSources[tag]
	if !ok {
		return "", false
	}
	path := filepath.Join(dir, src.Filename())
	info, err := os.Stat(path)
	if err != nil || info.Size() != src.File.Size {
		return "", false
	}
	return path, true
}

// Any is the first installed model in Installable order.
func Any(dir string) (tag, path string, ok bool) {
	for _, candidate := range Installable() {
		if found, yes := Installed(dir, candidate); yes {
			return candidate, found, true
		}
	}
	return "", "", false
}

// Base is the pinned upstream every File.Path is relative to.
func Base() string { return modelBase }
