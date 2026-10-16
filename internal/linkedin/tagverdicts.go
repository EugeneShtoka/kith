package linkedin

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// MarkTag records a filter's move or a release locally; LinkedIn has nowhere to keep
// one.
func (a *Adapter) MarkTag(ctx context.Context, verdict domain.TagVerdict) error {
	if a.cache == nil {
		return nil
	}
	if err := a.cache.MarkTag(ctx, verdict); err != nil {
		return fmt.Errorf("linkedin: record the %s verdict on %s: %w", verdict.Tag, verdict.Room, err)
	}
	return nil
}

// RetagVerdicts moves a tag's local verdicts with its new name, or drops them.
func (a *Adapter) RetagVerdicts(ctx context.Context, from, to string) error {
	if a.cache == nil {
		return nil
	}
	if err := a.cache.RetagVerdicts(ctx, from, to); err != nil {
		return fmt.Errorf("linkedin: move the %s verdicts: %w", from, err)
	}
	if err := a.cache.RetagPersonVerdicts(ctx, domain.ProtocolLinkedIn, from, to); err != nil {
		return fmt.Errorf("linkedin: move the %s person verdicts: %w", from, err)
	}
	return nil
}

// MarkPerson records a filter's mark or a release on a LinkedIn ID locally; LinkedIn
// has nowhere to keep one.
func (a *Adapter) MarkPerson(ctx context.Context, verdict domain.PersonVerdict) error {
	if a.cache == nil {
		return nil
	}
	if err := a.cache.MarkPerson(ctx, verdict); err != nil {
		return fmt.Errorf("linkedin: record the %s verdict on %s: %w", verdict.Tag, verdict.Person, err)
	}
	return nil
}
