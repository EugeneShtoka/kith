package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// showAgentLog prints the account's kith-mcp write ledger, oldest first. It needs no
// daemon: it must work exactly when something has gone wrong.
func showAgentLog(configPath, profile string) error {
	path, cfg, ready, err := loadConfig(configPath, profile)
	if err != nil || !ready {
		return err
	}
	storage, err := setup.StorageFor(cfg, path, profile)
	if err != nil {
		return err
	}
	ledger, err := agent.Open(storage.LedgerPath())
	if err != nil {
		return fmt.Errorf("opening the ledger: %w", err)
	}
	entries, skipped, err := ledger.Read()
	if err != nil {
		return fmt.Errorf("reading the ledger: %w", err)
	}
	if skipped > 0 {
		defer fmt.Printf("%d unreadable line(s) in the ledger were skipped.\n", skipped)
	}
	if len(entries) == 0 {
		fmt.Printf("Nothing has been written on your behalf.\n(%s)\n", path)
		return nil
	}
	for i := range entries {
		fmt.Println(agentLogLine(entries[i]))
	}
	fmt.Printf("\n%d entries in %s\n", len(entries), path)
	return nil
}

// agentLogLine is one entry as a line.
func agentLogLine(entry agent.Entry) string {
	where := entry.Name
	if where == "" {
		where = string(entry.Room)
	}
	line := fmt.Sprintf("%s  %-8s %s  [%s]  %s",
		entry.At.Local().Format(time.RFC3339), entry.Outcome, where, entry.Author,
		strings.Join(strings.Fields(entry.Text), " "))
	if entry.Reason != "" {
		line += "\n  ↳ " + entry.Reason
	}
	return line
}

// warnAboutAgentScope prints the `[agent.write]` entries `[agent.read]` rules out.
// Printed here because kith-mcp's stderr is somewhere nobody looks. Never fatal.
func warnAboutAgentScope(ctx context.Context, places setup.AgentPlaces, cfg config.Config) {
	ctx, cancel := context.WithTimeout(ctx, agentScopeTimeout)
	defer cancel()
	for _, warning := range setup.AgentWarnings(ctx, places, setup.PlacesOf(cfg.Display), cfg.Agent) {
		fmt.Fprintln(os.Stderr, "kith: warning:", warning)
	}
}

// agentScopeTimeout bounds the startup warning's cache reads.
const agentScopeTimeout = 3 * time.Second
