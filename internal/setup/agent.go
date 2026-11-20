package setup

import (
	"fmt"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// DefaultAgentCooldown is the default rest for a room after an assistant posts to it.
const DefaultAgentCooldown = time.Minute

// DefaultAgentQueueLimit is how many of an assistant's messages may wait to go out to
// one room by default.
const DefaultAgentQueueLimit = 10

// AgentKithLinks reports whether [agent] links asks for kith's own links on every
// message, rather than its network's where it has one.
func AgentKithLinks(agent config.Agent) (bool, error) {
	switch spelled := strings.TrimSpace(agent.Links); spelled {
	case "", "native":
		return false, nil
	case "kith":
		return true, nil
	default:
		return false, fmt.Errorf("agent.links: %q is neither \"native\" nor \"kith\"", spelled)
	}
}

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

// AgentQueueLimit is how many messages may wait to go out to one room, resolved.
func AgentQueueLimit(agent config.Agent) (int, error) {
	if agent.Write.QueueLimit == nil {
		return DefaultAgentQueueLimit, nil
	}
	if limit := *agent.Write.QueueLimit; limit >= 0 {
		return limit, nil
	}
	return 0, fmt.Errorf("agent.write.queue_limit: %d must not be negative — 0 lets no message wait", *agent.Write.QueueLimit)
}

// weekdays reads a [[agent.write.hours]] day by its first three letters.
var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// AgentHours is [[agent.write.hours]], resolved.
func AgentHours(agent config.Agent) ([]domain.WorkingHours, error) {
	out := make([]domain.WorkingHours, 0, len(agent.Write.Hours))
	for i, h := range agent.Write.Hours {
		w, err := notify.ParseWhen(h.When)
		if err != nil || w.Start == w.End {
			return nil, fmt.Errorf("agent.write.hours[%d].when: %q is not a window (try \"09:00-18:00\")", i, h.When)
		}
		hours := domain.WorkingHours{Match: strings.TrimSpace(h.Match), Start: w.Start, End: w.End}
		for _, spelled := range h.Days {
			day, ok := weekdays[strings.ToLower(strings.TrimSpace(spelled))]
			if !ok {
				return nil, fmt.Errorf("agent.write.hours[%d].days: %q is not a day (mon, tue, wed, thu, fri, sat, sun)", i, spelled)
			}
			hours.Days = append(hours.Days, day)
		}
		out = append(out, hours)
	}
	return out, nil
}
