package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ignoredType is the Matrix account data type for ignored users: the account's,
// not a room's. The homeserver stops sending the ignored users' events at all —
// no messages, no invites, nothing in sync. See the spec:
// https://spec.matrix.org/latest/client-server-api/#mignored_user_list
const ignoredType = "m.ignored_user_list"

// ignoredContent is the account-data event's shape: the Matrix spec requires a
// value for every ignored user, left as an empty object so the server can one
// day attach a reason without a schema break.
type ignoredContent struct {
	IgnoredUsers map[string]json.RawMessage `json:"ignored_users"`
}

// users is the content as a sorted set of MXIDs. Sorting makes the record stable
// across writes so a diff is readable, and the mirror can compare whole-set.
func (c ignoredContent) users() []string {
	ids := make([]string, 0, len(c.IgnoredUsers))
	for id := range c.IgnoredUsers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ignoredContentOf is a set of MXIDs as account data carries it: an empty object
// per user, which the spec reserves for a future value.
func ignoredContentOf(ids []string) ignoredContent {
	out := ignoredContent{IgnoredUsers: map[string]json.RawMessage{}}
	for _, id := range ids {
		out.IgnoredUsers[id] = json.RawMessage("{}")
	}
	return out
}

// ignoredFrom reports the ignored account data in a sync, and whether it was
// present (absence is no change, not a clear).
func ignoredFrom(events []*event.Event) (content ignoredContent, present bool) {
	for _, evt := range events {
		if evt == nil || evt.Type.Type != ignoredType {
			continue
		}
		var parsed ignoredContent
		if err := json.Unmarshal(evt.Content.VeryRaw, &parsed); err != nil {
			// Unparseable is absent: the mirror keeps its ignored set rather than
			// losing it to a server that briefly answered junk.
			continue
		}
		content, present = parsed, true
	}
	return content, present
}

// Ignore adds mxid to this account's m.ignored_user_list on the homeserver,
// then the mirror. The whole set is read first (another device may have moved
// since the last sync), so a concurrent write never shrinks the list silently.
func (b *InProc) Ignore(ctx context.Context, mxid string) error {
	return b.updateIgnored(ctx, func(set map[string]bool) { set[mxid] = true })
}

// Unignore is Ignore's inverse: drops mxid from the set, writes it back,
// mirrors. A user who was not ignored is a no-op (no write), so repeated calls
// are cheap.
func (b *InProc) Unignore(ctx context.Context, mxid string) error {
	return b.updateIgnored(ctx, func(set map[string]bool) { delete(set, mxid) })
}

// Ignored is the MXIDs this account ignores, as the homeserver has them.
func (b *InProc) Ignored(ctx context.Context) ([]string, error) {
	return b.serverIgnored(ctx)
}

// updateIgnored serializes reads and writes to the ignored set, so two local
// callers see each other's change. Server-side CAS is still missing (the Matrix
// account-data endpoint has no If-Match), so two devices racing can still lose
// one write — same risk as m.direct.
func (b *InProc) updateIgnored(ctx context.Context, change func(map[string]bool)) error {
	b.ignoredMu.Lock()
	defer b.ignoredMu.Unlock()
	before, err := b.serverIgnored(ctx)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, id := range before {
		set[id] = true
	}
	change(set)
	after := make([]string, 0, len(set))
	for id := range set {
		after = append(after, id)
	}
	sort.Strings(after)
	if sameStringSet(before, after) {
		return nil
	}
	return b.writeIgnored(ctx, after)
}

// serverIgnored is the ignored set as the homeserver has it; none yet is empty.
func (b *InProc) serverIgnored(ctx context.Context) ([]string, error) {
	var current ignoredContent
	err := b.client.GetAccountData(ctx, ignoredType, &current)
	switch {
	case errors.Is(err, mautrix.MNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("matrix: read the ignored list: %w", err)
	}
	return current.users(), nil
}

// writeIgnored stores the whole set on the homeserver, then the mirror.
func (b *InProc) writeIgnored(ctx context.Context, ids []string) error {
	content := ignoredContentOf(ids)
	if err := b.client.SetAccountData(ctx, ignoredType, &content); err != nil {
		return fmt.Errorf("matrix: record the ignored list: %w", err)
	}
	return toCache(b, "mirror the ignored list", func(c *db.Cache) error {
		return c.SetMatrixIgnored(ctx, domain.ProtocolMatrix, ids)
	})
}

// mirrorIgnored applies the set to the cache; best-effort, as the server copy is
// the record.
func (b *InProc) mirrorIgnored(ctx context.Context, ids []string) {
	if b.cache == nil {
		return
	}
	b.warnIf(ctx, b.cache.SetMatrixIgnored(ctx, domain.ProtocolMatrix, ids), "mirror the ignored list")
}

// sameStringSet reports whether two sorted lists hold the same entries.
func sameStringSet(a, b []string) bool {
	return slices.Equal(a, b)
}
