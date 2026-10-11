package setup_test

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func TestAgentCooldown(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		spelled string
		want    time.Duration
		wantErr bool
	}{
		{"unset takes the default", "", setup.DefaultAgentCooldown, false},
		{"a duration", "30s", 30 * time.Second, false},
		{"spaces around it", " 5m ", 5 * time.Minute, false},
		// Off is explicit, so disabling the rail is visible in the file.
		{"zero is off", "0", 0, false},
		// A typo must not silently fall back to the default.
		{"a typo is refused", "5 minutes", 0, true},
		{"negative is refused", "-1m", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := setup.AgentCooldown(config.Agent{Write: config.AgentWrite{Cooldown: tc.spelled}})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("AgentCooldown(%q) = %v, want an error", tc.spelled, got)
				}
				if !strings.Contains(err.Error(), "agent.write.cooldown") {
					t.Errorf("error = %q, want it to name the setting", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("AgentCooldown(%q): %v", tc.spelled, err)
			}
			if got != tc.want {
				t.Errorf("AgentCooldown(%q) = %v, want %v", tc.spelled, got, tc.want)
			}
		})
	}
}

// All five agent place lists are validated at startup.
func TestPlaceEntriesChecksEveryAgentList(t *testing.T) {
	t.Parallel()

	bare := []string{"Standup"}
	cases := map[string]config.Agent{
		"agent.read.rooms":   {Read: config.AgentRead{Rooms: bare}},
		"agent.read.except":  {Read: config.AgentRead{Except: bare}},
		"agent.write.rooms":  {Write: config.AgentWrite{Rooms: bare}},
		"agent.write.except": {Write: config.AgentWrite{Except: bare}},
		"agent.write.send":   {Write: config.AgentWrite{Send: bare}},
	}
	for list, agent := range cases {
		t.Run(list, func(t *testing.T) {
			t.Parallel()
			err := setup.PlaceEntries(config.Config{Agent: agent})
			if err == nil {
				t.Fatalf("a bare entry in %s was accepted", list)
			}
			if !strings.Contains(err.Error(), list) {
				t.Errorf("error = %q, want it to name %s", err, list)
			}
		})
	}
	good := []string{"room:Standup", "space:Work", "dm"}
	cfg := config.Config{Agent: config.Agent{
		Read:  config.AgentRead{Rooms: good, Except: good},
		Write: config.AgentWrite{Rooms: good, Except: good, Send: good},
	}}
	if err := setup.PlaceEntries(cfg); err != nil {
		t.Errorf("well-formed agent lists were refused: %v", err)
	}
}
