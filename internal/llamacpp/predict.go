package llamacpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"
)

// Only the first generated position is read. A token is a word offer only if it
// starts a word (leading space or U+2581), contains a letter or digit, and is not a
// case-duplicate of an earlier one.

// completionRequest is llama.cpp's native /completion body (the one carrying n_probs).
// Temperature 0 so sampling cannot reorder the ranking; cache_prompt reuses the KV
// cache across a draft's shared prefix.
type completionRequest struct {
	Prompt      string  `json:"prompt"`
	NPredict    int     `json:"n_predict"`
	NProbs      int     `json:"n_probs"`
	Temperature float64 `json:"temperature"`
	CachePrompt bool    `json:"cache_prompt"`
}

// completionResponse is the ranked tokens at the first position, or the server's error.
type completionResponse struct {
	Probabilities []struct {
		Top []tokenProb `json:"top_logprobs"`
	} `json:"completion_probabilities"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// tokenProb is one candidate token; the server returns them sorted.
type tokenProb struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

// distribution asks the server what may come next.
func (s *server) distribution(ctx context.Context, prompt string, n int) ([]tokenProb, error) {
	s.touch()
	body, err := json.Marshal(completionRequest{
		Prompt:      prompt,
		NPredict:    1,
		NProbs:      n,
		Temperature: 0,
		CachePrompt: true,
	})
	if err != nil {
		return nil, fmt.Errorf("llamacpp: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url("/completion"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llamacpp: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llamacpp: predict: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return nil, fmt.Errorf("llamacpp: read reply: %w", err)
	}
	var decoded completionResponse
	// Decoded before the status check: a refusal's reason is in the body. A refusal
	// need not be JSON, so the decode error only matters on a 200.
	decodeErr := json.Unmarshal(raw, &decoded)
	if resp.StatusCode != http.StatusOK {
		if decoded.Error != nil && decoded.Error.Message != "" {
			return nil, replyErr{fmt.Errorf("llamacpp: predict: %s: %s", resp.Status, decoded.Error.Message)}
		}
		return nil, replyErr{fmt.Errorf("llamacpp: predict: %s", resp.Status)}
	}
	if decodeErr != nil {
		return nil, replyErr{fmt.Errorf("llamacpp: predict: decode reply: %w", decodeErr)}
	}
	if len(decoded.Probabilities) == 0 {
		return nil, replyErr{errors.New("llamacpp: predict: no distribution in the reply")}
	}
	return decoded.Probabilities[0].Top, nil
}

// replyErr is an answer that was not a prediction: the server is up and talking, so
// it is kept.
type replyErr struct{ error }

func (e replyErr) Unwrap() error { return e.error }

// maxReplyBytes bounds one reply.
const maxReplyBytes = 1 << 20

// words turns a distribution into the options worth offering, best first.
func words(probs []tokenProb, n int) []string {
	out := make([]string, 0, n)
	seen := make(map[string]bool, n)
	for _, p := range probs {
		word, ok := wordOf(p.Token)
		if !ok || seen[strings.ToLower(word)] {
			continue
		}
		seen[strings.ToLower(word)] = true
		out = append(out, word)
		if len(out) == n {
			break
		}
	}
	return out
}

// wordOf is the word a token offers, and false when it offers none.
func wordOf(token string) (string, bool) {
	// GPT-2 BPE marks a word start with a space, SentencePiece with U+2581.
	switch {
	case strings.HasPrefix(token, " "):
		token = token[1:]
	case strings.HasPrefix(token, "▁"):
		token = strings.TrimPrefix(token, "▁")
	default:
		// Continues the word on screen: not an offer.
		return "", false
	}
	// Keep only the first word of a token that spans whitespace.
	if cut := strings.IndexFunc(token, unicode.IsSpace); cut >= 0 {
		token = token[:cut]
	}
	if token == "" || !hasLetterOrDigit(token) {
		return "", false
	}
	return token, true
}

// hasLetterOrDigit separates words ("don't", "v2") from punctuation.
func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
