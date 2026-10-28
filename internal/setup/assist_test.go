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
			Rooms:    []string{mx + "!a:x"}, Encrypted: true, Timeout: "4s",
		}, ""},
		{"a deny list on its own", config.Assist{
			Endpoint: "https://example.invalid/v1", Except: []string{mx + "!a:x"},
		}, ""},
		// Otherwise every ask fails with an error that reads as a bug.
		{"an endpoint that is not a URL", config.Assist{Endpoint: "api.groq.com/v1/chat"}, "complete.model.endpoint"},
		{"a timeout that is not a duration", config.Assist{
			Endpoint: "https://example.invalid/v1", Timeout: "soon",
		}, "complete.model.timeout"},
		{"a negative timeout", config.Assist{
			Endpoint: "https://example.invalid/v1", Timeout: "-1s",
		}, "complete.model.timeout"},
		// The allow list answers alone, so the exception would do nothing.
		{"a room named in both lists", config.Assist{
			Endpoint: "https://example.invalid/v1",
			Rooms:    []string{" " + mx + "!a:x "}, Except: []string{mx + "!A:X"},
		}, "complete.model.except"},
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

// A bad spelling is an error, not the default.
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
