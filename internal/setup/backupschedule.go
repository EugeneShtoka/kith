package setup

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/EugeneShtoka/kith/internal/config"
)

// errEveryAndCron is [backup] with both every and cron: which would kithd follow?
var errEveryAndCron = errors.New("backup: set every or cron, not both")

// BackupNext is when the scheduled backup after one made at last is due, as [backup]
// says: every after it, or the cron expression's next time after it (in local time).
// Neither set is every config.DefaultBackupEvery.
func BackupNext(b config.Backup) (func(last time.Time) time.Time, error) {
	every, spec := strings.TrimSpace(b.Every), strings.TrimSpace(b.Cron)
	switch {
	case every != "" && spec != "":
		return nil, errEveryAndCron
	case spec != "":
		sched, err := cron.ParseStandard(spec)
		if err != nil {
			return nil, fmt.Errorf("backup.cron %q: write five fields, minute hour day month weekday, like \"0 3 * * *\": %w", spec, err)
		}
		return sched.Next, nil
	case every != "":
		d, err := config.ParseEvery(every)
		if err != nil {
			return nil, fmt.Errorf("backup.every %q: write a duration like 6h or 90m, or days like 7d", every)
		}
		return func(last time.Time) time.Time { return last.Add(d) }, nil
	}
	return func(last time.Time) time.Time { return last.Add(config.DefaultBackupEvery) }, nil
}
