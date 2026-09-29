package setup

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/logging"
)

func TestLogLevel(t *testing.T) {
	t.Parallel()
	if got, err := LogLevel(config.Log{}); err != nil || got != slog.LevelInfo {
		t.Errorf("unset = %v, %v; want info", got, err)
	}
	if got, err := LogLevel(config.Log{Level: "debug"}); err != nil || got != slog.LevelDebug {
		t.Errorf("debug = %v, %v", got, err)
	}
	cfg := config.Config{Log: config.Log{Level: "chatty"}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "[log] level") {
		t.Errorf("Validate(bad level) = %v, want the section named", err)
	}
}

func TestLogTarget(t *testing.T) {
	t.Parallel()
	if got, err := LogTarget(config.Log{}); err != nil || got != logging.TargetAuto {
		t.Errorf("unset = %v, %v; want auto", got, err)
	}
	if got, err := LogTarget(config.Log{Target: "journal"}); err != nil || got != logging.TargetJournal {
		t.Errorf("journal = %v, %v", got, err)
	}
	cfg := config.Config{Log: config.Log{Target: "syslog"}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "[log] target") {
		t.Errorf("Validate(bad target) = %v, want the section named", err)
	}
}
