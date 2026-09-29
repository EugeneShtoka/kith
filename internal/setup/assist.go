package setup

import (
	"fmt"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Checking `[assist]`, and what its tasks may quote.

// Default bounds for what a task may quote.
const (
	// defaultSummaryBudget is how much conversation a summary may quote — far more than
	// a completion, because it is *about* the conversation.
	defaultSummaryBudget = 6000
	// A todo reads several rooms, so its budget is larger and its room count is what
	// really bounds it: five conversations is what somebody can act on in one sitting,
	// and a list of thirty is a list nobody reads.
	defaultTodoBudget = 9000
	defaultTodoRooms  = 8
)

// ModelBudgets is how much conversation each task may quote, where it differs from the
// section's own budget.
func ModelBudgets(cfg config.Assist, completion config.CompleteModel) map[string]int {
	return map[string]int{
		domain.ModelSummary: orInt(cfg.Summary.Budget, defaultSummaryBudget),
		domain.ModelTodo:    orInt(cfg.Todo.Budget, defaultTodoBudget),
		// The completion's budget comes from its own section, which is the whole reason
		// this takes two arguments: the model it feeds runs on this machine, so what
		// the number buys is room in a 1024-token window rather than a smaller bill.
		domain.ModelComplete: orInt(completion.Budget, config.DefaultModelBudget),
	}
}

// ModelLayer checks `[complete.model]`: the endpoint's shape, the timeout's spelling,
// and that the two opt-in lists do not contradict what they are for.
func ModelLayer(cfg config.Assist) error {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		// Off, which is the default.
		return nil
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return fmt.Errorf("complete.model.endpoint: %q is not an http(s) URL", endpoint)
	}
	if spelled := strings.TrimSpace(cfg.Timeout); spelled != "" {
		parsed, err := time.ParseDuration(spelled)
		if err != nil {
			return fmt.Errorf("complete.model.timeout: %q is not a duration (try \"4s\")", spelled)
		}
		if parsed <= 0 {
			return fmt.Errorf("complete.model.timeout: %q must be positive", spelled)
		}
	}
	// A room in both lists is the contradiction this section invites now that there are
	// two of them.
	for _, room := range cfg.Except {
		if strings.TrimSpace(room) == "" {
			continue
		}
		if names(cfg.Rooms, room) {
			return fmt.Errorf(
				"complete.model.except: %q is also in complete.model.rooms — "+
					"the allow list answers alone, so the exception would do nothing", room)
		}
	}
	return nil
}

// names reports whether a list names one room, by the same comparison domain.AllowModel
// makes — trimmed and case-insensitive, because a config file is typed by hand.
func names(list []string, room string) bool {
	for _, entry := range list {
		if strings.EqualFold(strings.TrimSpace(entry), strings.TrimSpace(room)) {
			return true
		}
	}
	return false
}

func orInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// TodoRooms is how many rooms `:todo` may read at once.
func TodoRooms(cfg config.Assist) int { return orInt(cfg.TodoRooms, defaultTodoRooms) }
