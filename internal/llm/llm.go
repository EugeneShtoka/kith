// Package llm talks to one OpenAI-compatible chat endpoint.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Task is one prompt the model can run, with bounds on the answer.
type Task struct {
	Name string // config/UI name
	// System and Prompt are templates over the same fields (see Render).
	System      string
	Prompt      string
	MaxTokens   int
	Temperature float64
	Stop        []string
	Model       string // overrides the client's model when set
	Effort      string // reasoning effort ("low", "medium", "high"); empty = endpoint default
}

// Client is one configured endpoint.
type Client struct {
	endpoint string
	model    string
	key      string
	http     *http.Client
}

// ErrNotConfigured is returned when there is no endpoint: the model layer is off.
var ErrNotConfigured = errors.New("llm: no endpoint configured")

// New makes a client, or reports that there is nothing configured.
func New(endpoint, model, key string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, ErrNotConfigured
	}
	return &Client{
		endpoint: endpoint,
		model:    model,
		key:      key,
		http:     &http.Client{Timeout: timeout},
	}, nil
}

// chatRequest is the OpenAI-compatible body.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	Effort      string        `json:"reasoning_effort,omitempty"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Render expands a template against a task's fields.
func Render(tpl string, fields map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(tpl, func(name string) string {
		if value, ok := fields[name]; ok {
			return value
		}
		return name
	})
}

var placeholderRe = regexp.MustCompile(`\{[a-z_]+\}`)

// Ask runs one task and returns what came back, trimmed.
func (c *Client) Ask(ctx context.Context, task Task, fields map[string]string) (string, error) {
	if c == nil {
		return "", ErrNotConfigured
	}
	model := c.model
	if task.Model != "" {
		model = task.Model
	}
	body := chatRequest{Model: model, Stop: task.Stop, Effort: task.Effort, MaxTokens: max(task.MaxTokens, 0)}
	if system := strings.TrimSpace(Render(task.System, fields)); system != "" {
		body.Messages = append(body.Messages, chatMessage{Role: "system", Content: system})
	}
	prompt := strings.TrimSpace(Render(task.Prompt, fields))
	if prompt == "" {
		return "", fmt.Errorf("llm: %s: nothing to ask", task.Name)
	}
	body.Messages = append(body.Messages, chatMessage{Role: "user", Content: prompt})
	if task.Temperature > 0 {
		temp := task.Temperature
		body.Temperature = &temp
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("llm: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", fmt.Errorf("llm: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: %s: %w", task.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return readReply(resp, task)
}

// readReply returns the reply text or why there is none.
func readReply(resp *http.Response, task Task) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return "", fmt.Errorf("llm: read reply: %w", err)
	}
	var decoded chatResponse
	// Decode before the status check: error details are in the body, but a refusal
	// need not be JSON, so the decode error matters only on a 200.
	decodeErr := json.Unmarshal(raw, &decoded)
	if resp.StatusCode != http.StatusOK {
		if decoded.Error != nil && decoded.Error.Message != "" {
			return "", fmt.Errorf("llm: %s: %s: %s", task.Name, resp.Status, decoded.Error.Message)
		}
		return "", fmt.Errorf("llm: %s: %s", task.Name, resp.Status)
	}
	if decodeErr != nil {
		return "", fmt.Errorf("llm: %s: decode reply: %w", task.Name, decodeErr)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("llm: %s: no reply", task.Name)
	}
	text := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if text == "" && decoded.Choices[0].FinishReason == "length" {
		// A reasoning model charges its thinking against max_tokens.
		return "", fmt.Errorf(
			"llm: %s: the model used all %d tokens thinking and answered with nothing — "+
				"raise [complete.model.%s] max_tokens or lower reasoning_effort",
			task.Name, task.MaxTokens, task.Name)
	}
	return text, nil
}

const maxReplyBytes = 1 << 20
