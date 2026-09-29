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

// ResolveTarget picks the target: the flag if set, else $KITH_LOG_TARGET if set, else
// the config's, else auto. Each is validated, like Resolve's levels.
func ResolveTarget(flag, configured string) (Target, error) {
	for _, name := range []string{flag, os.Getenv(EnvTarget), configured} {
		if strings.TrimSpace(name) != "" {
			return ParseTarget(name)
		}
	}
	return TargetAuto, nil
}

// Identifier is a program's SYSLOG_IDENTIFIER: the program, or program-profile.
func Identifier(program, profile string) string {
	if profile == "" {
		return program
	}
	return program + "-" + profile
}

// Destination says where Open should send a log.
type Destination struct {
	Target Target
	// Identifier tags journal entries (see Identifier).
	Identifier string
	// File is the log file: the target when it is TargetFile, the fallback when
	// the journal is not there. Empty means none, so no journal is an error.
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

// Close closes the file. The journal needs no closing: its socket is shared by the
// process.
func (s *Sink) Close() error {
	if s.closer == nil {
		return nil
	}
	if err := s.closer.Close(); err != nil {
		return fmt.Errorf("logging: close log: %w", err)
	}
	return nil
}

// ErrNoJournal is Open's error when the journal was wanted, is not there, and no
// file was given to fall back to.
var ErrNoJournal = errors.New("logging: no journal")

// Open opens d's log. Auto and journal try the journal first; without one they
// fall back to the file — silently for auto, with a one-time note at the top of the
// file for journal, since that was asked for by name. Nothing is ever written to
// stderr: the caller decides what an error means.
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
