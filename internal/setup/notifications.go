package setup

import (
	"fmt"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// NotificationRules is a built-in account-wide rule (show = mention) followed by the
// configured rules. A configured rule with no match or sender ties on
// specificity and, being later, overrides the account-wide level. Misspellings are errors.
func NotificationRules(notifs config.Notifications) ([]notify.Rule, error) {
	show := notify.LevelMention
	rules := []notify.Rule{{Name: everythingRule, Show: &show}}
	for i := range notifs.Rules {
		rule, err := notificationRule(&notifs.Rules[i])
		if err != nil {
			return nil, fmt.Errorf("notifications.rule[%d]: %w", i, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// everythingRule names the account-wide rule in the status badge and the `W` overlay.
const everythingRule = "all rooms"

// notificationRule converts one configured rule, leaving unset fields unset so a rule
// changes only what it names.
func notificationRule(c *config.Rule) (notify.Rule, error) {
	thread, err := notify.ParseThread(c.Thread)
	if err != nil {
		return notify.Rule{}, fmt.Errorf("thread: %w", err)
	}
	rule := notify.Rule{Name: c.Name, Match: c.Match, Sender: c.Sender, Thread: thread}
	if rule.Show, err = optionalLevel("show", c.Show); err != nil {
		return notify.Rule{}, err
	}
	if rule.Ring, err = optionalLevel("ring", c.Ring); err != nil {
		return notify.Rule{}, err
	}
	if c.When != "" {
		w, err := notify.ParseWhen(c.When)
		if err != nil {
			return notify.Rule{}, fmt.Errorf("when: %w", err)
		}
		rule.When = &w
	}
	return rule, nil
}

// optionalLevel parses a level that may be unset (nil).
func optionalLevel(key, spelled string) (*notify.Level, error) {
	if spelled == "" {
		return nil, nil
	}
	l, err := notify.ParseLevel(spelled)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return &l, nil
}

// NotifierReporting builds the delivery sinks, or a Nop when notifications are off;
// report receives every failed delivery.
func NotifierReporting(notifs config.DeviceNotifications, report func(sink string, err error)) notify.Notifier {
	if !notifs.Enabled {
		return notify.Nop{}
	}
	return notify.New(notify.Settings{
		Report:       report,
		Desktop:      notifs.Desktop,
		Command:      notifs.Command,
		SoundCommand: notifs.SoundCommand,
		Timeout:      notifs.PopupTimeout(),
		Templates:    Templates(notifs),
	})
}

// NotifierSinks is NotifierReporting as a builder, for rebuilding sinks on reload.
func NotifierSinks(report func(sink string, err error)) func(config.DeviceNotifications) notify.Notifier {
	return func(notifs config.DeviceNotifications) notify.Notifier { return NotifierReporting(notifs, report) }
}

// NotificationLimit is how much one room may interrupt you with, resolved.
func NotificationLimit(notifs config.Notifications) (notify.Limit, error) {
	if notifs.MaxPerRoom <= 0 {
		return notify.Limit{}, nil
	}
	window := defaultRateWindow
	if spelled := strings.TrimSpace(notifs.RateWindow); spelled != "" {
		parsed, err := time.ParseDuration(spelled)
		if err != nil {
			return notify.Limit{}, fmt.Errorf("notifications.rate_window: %q is not a duration (try \"2m\")", spelled)
		}
		if parsed <= 0 {
			return notify.Limit{}, fmt.Errorf("notifications.rate_window: %q must be positive", spelled)
		}
		window = parsed
	}
	return notify.Limit{Max: notifs.MaxPerRoom, Window: window}, nil
}

// defaultRateWindow is the burst window when only a count is configured.
const defaultRateWindow = 2 * time.Minute

// Templates is the user's title and body format, defaults filled in.
func Templates(notifs config.DeviceNotifications) notify.Templates {
	return notify.Templates{Title: notifs.TitleTemplate(), Body: notifs.BodyTemplate()}
}
