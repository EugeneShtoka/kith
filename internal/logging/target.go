package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Target is where a log goes: `[log] target`, --log-target, $KITH_LOG_TARGET.
type Target string

// The targets. Auto is the journal when one answers, else the file.
const (
	TargetAuto    Target = "auto"
	TargetJournal Target = "journal"
	TargetFile    Target = "file"
)

// EnvTarget is the environment variable that overrides the config's target.
const EnvTarget = "KITH_LOG_TARGET"

// Targets lists the target names ParseTarget accepts, for help text.
const Targets = "auto, journal, file"

// ParseTarget reads a target name. Empty means auto.
func ParseTarget(name string) (Target, error) {
	switch t := Target(strings.ToLower(strings.TrimSpace(name))); t {
	case "", TargetAuto:
		return TargetAuto, nil
	case TargetJournal, TargetFile:
		return t, nil
	default:
		return TargetAuto, fmt.Errorf("logging: unknown target %q (want one of %s)", name, Targets)
	}
}

// ResolveTarget picks the target like Resolve: flag, $KITH_LOG_TARGET, config, auto.
func ResolveTarget(flag, configured string) (Target, error) {
	for _, name := range []string{flag, os.Getenv(EnvTarget), configured} {
		if strings.TrimSpace(name) != "" {
			return ParseTarget(name)
		}
	}
	return TargetAuto, nil
}

// Destination says where Open should send a log.
type Destination struct {
	Target Target
	// Identifier tags journal entries (see Identifier).
	Identifier string
	// File is the target for TargetFile, else the fallback without a journal; empty
	// makes a missing journal an error.
	File string
	// Level is the minimum level written; nil means info.
	Level slog.Leveler
	// Journal is the journal to try; nil means SystemJournal.
	Journal *Journal
}

// Sink is an open log.
type Sink struct {
	Logger *slog.Logger
	// Journal reports whether entries go to the journal; otherwise to Path.
	Journal bool
	Path    string
	closer  io.Closer
}

// Close closes the file; the journal socket is process-wide and stays open.
func (s *Sink) Close() error {
	if s.closer == nil {
		return nil
	}
	if err := s.closer.Close(); err != nil {
		return fmt.Errorf("logging: close log: %w", err)
	}
	return nil
}

// ErrNoJournal means the journal was wanted, is absent, and no fallback file was given.
var ErrNoJournal = errors.New("logging: no journal")

// Open opens d's log. Auto and journal fall back to the file without a journal;
// journal also notes the fallback in the file. Nothing is written to stderr.
func Open(d Destination) (*Sink, error) {
	if d.Target != TargetFile {
		j := SystemJournal
		if d.Journal != nil {
			j = *d.Journal
		}
		if j.Available() {
			return &Sink{Logger: slog.New(NewJournalHandler(j, d.Identifier, d.Level)), Journal: true}, nil
		}
		if d.File == "" {
			return nil, ErrNoJournal
		}
	}
	file, err := OpenFile(d.File, DefaultMaxBytes)
	if err != nil {
		return nil, err
	}
	log := New(file, Options{Level: d.Level})
	if d.Target == TargetJournal {
		log.Warn("log target is journal, but there is no journal; logging to this file instead")
	}
	return &Sink{Logger: log, Path: file.Path(), closer: file}, nil
}
