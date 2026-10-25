package setup

import (
	"fmt"
	"slices"

	"github.com/EugeneShtoka/kith/internal/config"
)

// DefaultAccounts refuses a network's default that names none of its accounts.
func DefaultAccounts(cfg config.Config) error {
	for _, n := range cfg.Networks() {
		if d := n.DefaultAccount(); d != "" && !slices.Contains(n.AccountNames(), d) {
			return fmt.Errorf("%s: default = %q names no [[%s]]", n.Network(), d, n.Table())
		}
	}
	return nil
}
