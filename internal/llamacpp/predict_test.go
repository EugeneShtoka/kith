package llamacpp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// stub is a llama-server that answers /health and returns one canned distribution,
// recording the request it was given.
type stub struct {
	*httptest.Server
	// asked is the last prompt received, for the trailing-space rule.
	asked string
	// probs is what it will answer with.
	probs []string
	// probes is the n_probs the last request asked for, for the slack rule.
	probes int
}

func newStub(t *testing.T, probs ...string) *stub {
	t.Helper()
	s := &stub{probs: probs}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req completionRequest
		_ = json.Unmarshal(body, &req)
		s.asked, s.probes = req.Prompt, req.NProbs

		top := make([]tokenProb, 0, len(s.probs))
		for i, token := range s.probs {
			top = append(top, tokenProb{Token: token, Logprob: float64(-i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"completion_probabilities": []map[string]any{{"top_logprobs": top}},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

// server pointed at the stub, skipping the spawn: what is under test here is the
// protocol, not the process.
func (s *stub) server() *server {
	return &server{base: s.URL, http: s.Client(), now: time.Now}
}

// words keeps only tokens that start a word, drops punctuation, folds case (keeping
// the likelier), keeps inner punctuation, handles SentencePiece, truncates at inner
// whitespace, and stops at n.
func TestWords(t *testing.T) {
	t.Parallel()

	tok := func(ts ...string) []tokenProb {
		out := make([]tokenProb, len(ts))
		for i, tk := range ts {
			out[i] = tokenProb{Token: tk}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		in   []tokenProb
		n    int
		want []string
	}{
		{"word starts only", tok(" weather", "ing", " data"), 4, []string{"weather", "data"}},
		{"punctuation and bare space", tok(" .", ",", " ", " …", " soon"), 4, []string{"soon"}},
		{"case folded", tok(" The", " the", " a"), 4, []string{"The", "a"}},
		{"inner punctuation", tok(" don't", " re-run", " v2"), 4, []string{"don't", "re-run", "v2"}},
		{"sentencepiece", tok("▁hello", "▁there"), 4, []string{"hello", "there"}},
		{"inner whitespace", tok(" yes\nplease"), 4, []string{"yes"}},
		{"count", tok(" a", " b", " c", " d"), 2, []string{"a", "b"}},
	} {
		if got := words(tc.in, tc.n); !slices.Equal(got, tc.want) {
			t.Errorf("%s: words = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A server error carries the body's message.
func TestAServerErrorCarriesWhatTheServerSaid(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"the prompt is too long"}}`)
	}))
	t.Cleanup(srv.Close)

	s := &server{base: srv.URL, http: srv.Client(), now: time.Now}
	_, err := s.distribution(context.Background(), "hello", 4)
	if err == nil || !strings.Contains(err.Error(), "the prompt is too long") {
		t.Fatalf("distribution: %v, want the server's own words", err)
	}
}

// A reply with no distribution is an error, not an empty answer.
func TestAReplyWithNoDistributionIsAnError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"content":" weather"}`)
	}))
	t.Cleanup(srv.Close)

	s := &server{base: srv.URL, http: srv.Client(), now: time.Now}
	if _, err := s.distribution(context.Background(), "hello", 4); err == nil {
		t.Fatal("a reply with no distribution was accepted")
	}
}
