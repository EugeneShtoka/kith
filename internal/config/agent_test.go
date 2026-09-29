package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// loadText writes body beside a minimal account and loads it.
func loadText(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	full := "homeserver = \"https://x\"\nuser = \"@a:x\"\n" + body
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Load(path)
}

// The two halves are read into their own tables, every key where it was written.
func TestAgentReadAndWriteAreSeparateTables(t *testing.T) {
	t.Parallel()

	cfg, err := loadText(t, `
[agent.read]
rooms = ["space:Work", "dm"]
except = ["room:HR"]
encrypted = true

[agent.write]
rooms = ["space:Work"]
except = ["room:Board"]
encrypted = true
send = ["room:Notes"]
cooldown = "30s"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	read, write := cfg.Agent.Read, cfg.Agent.Write
	if !slices.Equal(read.Rooms, []string{"space:Work", "dm"}) || !slices.Equal(read.Except, []string{"room:HR"}) || !read.Encrypted {
		t.Errorf("read = %+v", read)
	}
	if !slices.Equal(write.Rooms, []string{"space:Work"}) || !slices.Equal(write.Except, []string{"room:Board"}) ||
		!write.Encrypted || !slices.Equal(write.Send, []string{"room:Notes"}) || write.Cooldown != "30s" {
		t.Errorf("write = %+v", write)
	}
}
