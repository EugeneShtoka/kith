package apitest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/EugeneShtoka/kith/internal/api"
)

// Talk is a login's other side for tests (api.LoginTalk): it answers each field from
// Answers, in turn by key, and records every step. OnConfigure, when set, runs on each
// record written (a test's stand-in for the config being re-read); OnShow on each code.
type Talk struct {
	mu sync.Mutex
	// Answers are the answers to give, by field key, first first.
	Answers map[string][]string
	// Asked is every field asked for, by key, and Notes every note given with one.
	Asked []string
	Notes []string
	// Shown is every code shown; Written every record; Account the last one named.
	Shown   []string
	Written []api.LoginRecord
	Account string
	// OnConfigure and OnShow hear a record written and a code shown.
	OnConfigure func(api.LoginRecord) error
	OnShow      func(code string)
}

// ErrNoAnswer is a field Talk has no answer left for: the login asked more than the
// test expected.
var ErrNoAnswer = errors.New("apitest: no answer left for a field")

var _ api.LoginTalk = (*Talk)(nil)

// Ask answers each field from Answers.
func (t *Talk) Ask(ctx context.Context, note string, fields ...api.LoginField) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // the login's own end
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if note != "" {
		t.Notes = append(t.Notes, note)
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		t.Asked = append(t.Asked, f.Key)
		left := t.Answers[f.Key]
		if len(left) == 0 {
			return nil, fmt.Errorf("%w: %s (asked %v)", ErrNoAnswer, f.Key, t.Asked)
		}
		out[f.Key], t.Answers[f.Key] = left[0], left[1:]
		if out[f.Key] == "" {
			out[f.Key] = f.Value // enter keeps the suggestion
		}
	}
	return out, nil
}

// Show records the code.
func (t *Talk) Show(_ context.Context, code, _ string) error {
	t.mu.Lock()
	t.Shown = append(t.Shown, code)
	show := t.OnShow
	t.mu.Unlock()
	if show != nil {
		show(code)
	}
	return nil
}

// Configure records the record and runs OnConfigure.
func (t *Talk) Configure(_ context.Context, record api.LoginRecord) error {
	t.mu.Lock()
	t.Written = append(t.Written, record)
	configure := t.OnConfigure
	t.mu.Unlock()
	if configure != nil {
		return configure(record)
	}
	return nil
}

// Named records the account.
func (t *Talk) Named(account string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Account = account
}
