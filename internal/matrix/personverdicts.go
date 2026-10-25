package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// peopleType is the account data type for the people filters put into tags: the
// account's, not a room's, since a person is marked in every room. Matrix IDs only;
// another network's people are kept by its adapter.
const peopleType = "org.kith.people"

// peopleContent is every Matrix ID's marks and releases, by ID and lower-cased tag.
type peopleContent struct {
	Verdicts map[string]map[string]tagEntry `json:"verdicts"`
}

// verdicts is the content as the domain reads it.
func (c peopleContent) verdicts(x ids) []domain.PersonVerdict {
	var out []domain.PersonVerdict
	for person, tags := range c.Verdicts {
		for tag, e := range tags {
			v := domain.PersonVerdict{Person: x.userOf(person), Tag: tag, At: time.UnixMilli(e.At), Released: e.Released}
			if !e.Released {
				v.Rule, v.Filter = domain.MoveRule(e.Rule), e.Filter
			}
			out = append(out, v)
		}
	}
	return out
}

// peopleContentOf is a verdict set as account data carries it.
func peopleContentOf(x ids, set []domain.PersonVerdict) peopleContent {
	out := peopleContent{Verdicts: map[string]map[string]tagEntry{}}
	for _, v := range set {
		e := tagEntry{At: v.At.UnixMilli(), Released: v.Released}
		if !v.Released {
			e.Rule, e.Filter = int(v.Rule), v.Filter
		}
		person := matrixString(x.nativeUser(v.Person))
		if out.Verdicts[person] == nil {
			out.Verdicts[person] = map[string]tagEntry{}
		}
		out.Verdicts[person][strings.ToLower(strings.TrimSpace(v.Tag))] = e
	}
	return out
}

// peopleFrom reports the people account data in a sync, and whether it was present
// (absence is no change, not a release).
func peopleFrom(events []*event.Event) (content peopleContent, present bool) {
	for _, evt := range events {
		if evt == nil || evt.Type.Type != peopleType {
			continue
		}
		var parsed peopleContent
		if err := json.Unmarshal(evt.Content.VeryRaw, &parsed); err != nil {
			// Unparseable is absent: the mirror keeps its marks rather than losing them.
			continue
		}
		content, present = parsed, true
	}
	return content, present
}

// MarkPerson applies a mark or a release to the set on the homeserver, then the mirror.
// The server copy is read first: a release made on another machine may not have synced
// yet, and a filter's mark never overwrites one (domain.ErrPersonReleased).
func (b *InProc) MarkPerson(ctx context.Context, verdict domain.PersonVerdict) error {
	// The whole set is read, changed and written back (see MarkTag).
	b.tagsMu.Lock()
	defer b.tagsMu.Unlock()
	if verdict.At.IsZero() {
		verdict.At = time.Now()
	}
	verdict.Person = b.ids.user(b.ids.nativeUser(verdict.Person)) // the set's own form
	set, err := b.serverPeople(ctx)
	if err != nil {
		return err
	}
	set, err = domain.WithPersonVerdict(set, verdict)
	if err != nil {
		b.mirrorPeople(ctx, set) // the release, if the mirror missed it
		return fmt.Errorf("matrix: %w", err)
	}
	return b.writePeople(ctx, set)
}

// retagPeople moves tag from's person verdicts to tag to, or drops them. Call with
// tagsMu held.
func (b *InProc) retagPeople(ctx context.Context, from, to string) error {
	set, err := b.serverPeople(ctx)
	if err != nil {
		return err
	}
	if retagged, changed := domain.RetaggedPeople(set, from, to); changed {
		return b.writePeople(ctx, retagged)
	}
	return nil
}

// serverPeople is the verdict set as the homeserver has it; none yet is empty.
func (b *InProc) serverPeople(ctx context.Context) ([]domain.PersonVerdict, error) {
	var current peopleContent
	err := b.client.GetAccountData(ctx, peopleType, &current)
	switch {
	case errors.Is(err, mautrix.MNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("matrix: read the person verdicts: %w", err)
	}
	return current.verdicts(b.ids), nil
}

// writePeople stores the whole set on the homeserver, then the mirror.
func (b *InProc) writePeople(ctx context.Context, set []domain.PersonVerdict) error {
	content := peopleContentOf(b.ids, set)
	if err := b.client.SetAccountData(ctx, peopleType, &content); err != nil {
		return fmt.Errorf("matrix: record the person verdicts: %w", err)
	}
	return toCache(b, "mirror the person verdicts", func(c *db.Cache) error {
		return c.SetPersonVerdicts(ctx, b.ids.owner(), content.verdicts(b.ids))
	})
}

// mirrorPeople applies the set to the cache; best-effort, as the server copy is the
// record.
func (b *InProc) mirrorPeople(ctx context.Context, set []domain.PersonVerdict) {
	if b.cache == nil {
		return
	}
	b.warnIf(ctx, b.cache.SetPersonVerdicts(ctx, b.ids.owner(), set), "mirror person verdicts")
}
