package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tgerr"
)

// invokerFunc is a tg.Invoker from a function.
type invokerFunc func(ctx context.Context, input bin.Encoder, output bin.Decoder) error

func (f invokerFunc) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return f(ctx, input, output)
}

// A short FLOOD_WAIT is waited out and the call asked again; a long one fails the
// call at once, and so does one that keeps coming back.
func TestAShortFloodIsWaitedOut(t *testing.T) {
	t.Parallel()
	flood := func(seconds int) error { return &tgerr.Error{Code: 420, Type: "FLOOD_WAIT", Argument: seconds} }
	for name, c := range map[string]struct {
		answers []error
		asked   int
		ok      bool
	}{
		"short, then answered": {[]error{flood(1), nil}, 2, true},
		"long":                 {[]error{flood(600)}, 1, false},
		"again and again":      {[]error{flood(0), flood(0), flood(0), flood(0), nil}, floodRetries + 1, false},
	} {
		asked := 0
		next := invokerFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
			err := c.answers[min(asked, len(c.answers)-1)]
			asked++
			return err
		})
		start := time.Now()
		err := waitOutFloods().Handle(next)(t.Context(), nil, nil)
		if (err == nil) != c.ok || asked != c.asked {
			t.Errorf("%s: err %v after %d asks, want ok %v after %d", name, err, asked, c.ok, c.asked)
		}
		if name == "long" && time.Since(start) > time.Second {
			t.Errorf("a long wait was waited")
		}
	}
}
