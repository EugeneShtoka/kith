package setup

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestModelLayerRefusesWhatCannotWork(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  config.Assist
		want string // a fragment of the error, "" for accepted
	}{
		{"nothing configured is the default", config.Assist{}, ""},
		{"a working section", config.Assist{
			Endpoint: "http://localhost:11434/v1/chat/completions",
			Rooms:    []string{"!a:x"}, Encrypted: true, Timeout: "4s",
		}, ""},
		{"a deny list on its own", config.Assist{
			Endpoint: "https://example.invalid/v1", Except: []string{"!a:x"},
		}, ""},
		// A URL with a typo is the worst failure this section has: the feature looks on
		// and refuses every ask with the endpoint's own error, which reads as a bug.
		{"an endpoint that is not a URL", config.Assist{Endpoint: "api.groq.com/v1/chat"}, "not an http(s) URL"},
		{"a timeout that is not a duration", config.Assist{
			Endpoint: "https://example.invalid/v1", Timeout: "soon",
		}, "not a duration"},
		{"a negative timeout", config.Assist{
			Endpoint: "https://example.invalid/v1", Timeout: "-1s",
		}, "must be positive"},
		// A room on both lists: the allow list answers alone, so the exception does
		// nothing, and being told is better than finding out from a room that is
		// included despite being named as an exception.
		{"a room named in both lists", config.Assist{
			Endpoint: "https://example.invalid/v1",
			Rooms:    []string{" !a:x "}, Except: []string{"!A:X"},
		}, "the allow list answers alone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ModelLayer(tc.cfg)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("ModelLayer() = %v, want accepted", err)
			case tc.want != "" && err == nil:
				t.Fatalf("ModelLayer() accepted %+v, want %q", tc.cfg, tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("ModelLayer() = %v, want %q in it", err, tc.want)
			}
		})
	}
}

// What a completion quotes, resolved — and the one property that matters: a bad
// spelling is an error rather than the default, since a setting somebody believes they
// changed and did not is worse than one they never wrote.
func TestCompletionPick(t *testing.T) {
	t.Parallel()

	pick, err := CompletionPick(config.CompleteModel{})
	if err != nil {
		t.Fatalf("CompletionPick: %v", err)
	}
	if pick != domain.DefaultContextPick() {
		t.Errorf("pick = %+v, want the built-in shape", pick)
	}

	pick, err = CompletionPick(config.CompleteModel{Recent: 6, Gap: "45m", BeforeReply: 1})
	if err != nil {
		t.Fatalf("CompletionPick: %v", err)
	}
	if pick.Recent != 6 || pick.Gap != 45*time.Minute || pick.Before != 1 {
		t.Errorf("pick = %+v, want what the config said", pick)
	}

	if _, err := CompletionPick(config.CompleteModel{Gap: "an hour and a half"}); err == nil {
		t.Error("a gap that is not a duration was accepted")
	}
	if _, err := CompletionPick(config.CompleteModel{Recent: -1}); err == nil {
		t.Error("a negative message count was accepted")
	}
}
