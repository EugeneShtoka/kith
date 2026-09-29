package setup

import (
	"fmt"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
)

// What `[agent.read]` and `[agent.write]` mean once they are running, for the one
// caller that acts on them.

// DefaultAgentCooldown is how long a room rests after an assistant posts to it when the
// config does not say.
const DefaultAgentCooldown = time.Minute

// AgentCooldown is the rest between two sends to one room, resolved.
func AgentCooldown(agent config.Agent) (time.Duration, error) {
	spelled := strings.TrimSpace(agent.Write.Cooldown)
	if spelled == "" {
		return DefaultAgentCooldown, nil
	}
	parsed, err := time.ParseDuration(spelled)
	if err != nil {
		return 0, fmt.Errorf("agent.write.cooldown: %q is not a duration (try \"30s\")", spelled)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("agent.write.cooldown: %q must not be negative — \"0\" turns it off", spelled)
	}
	return parsed, nil
}
