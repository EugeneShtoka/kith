package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The client is tested against a local server, because everything worth pinning here is
// about the *request* it builds and how it reads a reply — neither of which needs a real
// model, and both of which are what breaks when an endpoint is swapped for another.

func serve(t *testing.T, status int, body string) (string, *[]map[string]any, *[]http.Header) {
	t.Helper()
	var bodies []map[string]any
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		_ = json.NewDecoder(r.Body).Decode(&decoded)
		bodies = append(bodies, decoded)
		headers = append(headers, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &bodies, &headers
}

func completeTask() Task {
	return Task{
		Name:      "complete",
		System:    "finish sentences in {language}",
		Prompt:    "draft:\n{draft}",
		MaxTokens: 32,
		Stop:      []string{"\n"},
	}
}

func fields() map[string]string {
	return map[string]string{"{draft}": "the quick brown", "{language}": "English"}
}

func TestNewRefusesNothingConfigured(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{"", "   "} {
		if _, err := New(endpoint, "m", "", time.Second); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("New(%q) error = %v, want ErrNotConfigured", endpoint, err)
		}
	}
	// And a nil client refuses rather than panicking, which is what makes "off" a state
	// the caller can hold rather than a branch it has to remember.
	var off *Client
	if _, err := off.Ask(context.Background(), completeTask(), fields()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("(*Client)(nil).Ask() error = %v, want ErrNotConfigured", err)
	}
}

func TestAskBuildsTheRequest(t *testing.T) {
	t.Parallel()

	url, bodies, headers := serve(t, http.StatusOK,
		`{"choices":[{"message":{"role":"assistant","content":"  fox jumps  "}}]}`)
	client, err := New(url, "test-model", "sekrit", time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := client.Ask(context.Background(), completeTask(), fields())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// Trimmed, because a model that pads its answer with spaces would otherwise insert
	// them into somebody's message.
	if got != "fox jumps" {
		t.Fatalf("Ask() = %q, want the reply trimmed", got)
	}
	if len(*bodies) != 1 {
		t.Fatalf("called %d times, want once", len(*bodies))
	}
	body := (*bodies)[0]
	if body["model"] != "test-model" {
		t.Errorf("model = %v, want test-model", body["model"])
	}
	if body["max_tokens"] != float64(32) {
		t.Errorf("max_tokens = %v, want 32", body["max_tokens"])
	}
	// Temperature is left out when the task has no opinion, so the endpoint's own
	// default applies rather than a zero this client never chose.
	if _, sent := body["temperature"]; sent {
		t.Error("temperature was sent for a task that did not set one")
	}
	if streaming, _ := body["stream"].(bool); streaming {
		t.Error("stream = true; the reply is read whole")
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v, want a system and a user one", messages)
	}
	system, _ := messages[0].(map[string]any)
	if content, _ := system["content"].(string); content != "finish sentences in English" {
		t.Errorf("system = %q, want the template expanded", content)
	}
	if auth := (*headers)[0].Get("Authorization"); auth != "Bearer sekrit" {
		t.Errorf("Authorization = %q, want the key as a bearer token", auth)
	}
}

// A model on this machine has nothing to authenticate, and requiring a key for localhost
// would make the private option the awkward one.
func TestAskWithoutAKeySendsNoHeader(t *testing.T) {
	t.Parallel()

	url, _, headers := serve(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)
	client, err := New(url, "local", "", time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Ask(context.Background(), completeTask(), fields()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if auth := (*headers)[0].Get("Authorization"); auth != "" {
		t.Errorf("Authorization = %q, want none", auth)
	}
}

func TestAskReportsWhatWentWrong(t *testing.T) {
	t.Parallel()

	// The useful half of an error from these endpoints is in the body, so a status line
	// without it is a message nobody can act on.
	url, _, _ := serve(t, http.StatusBadRequest, `{"error":{"message":"model not found"}}`)
	client, _ := New(url, "nope", "", time.Second)
	_, err := client.Ask(context.Background(), completeTask(), fields())
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("Ask() error = %v, want the endpoint's own words in it", err)
	}

	// A 200 with no choices is not an answer.
	empty, _, _ := serve(t, http.StatusOK, `{"choices":[]}`)
	client, _ = New(empty, "m", "", time.Second)
	if _, err := client.Ask(context.Background(), completeTask(), fields()); err == nil {
		t.Fatal("Ask() accepted a reply with no choices")
	}

	// A task whose prompt renders to nothing is refused before anything is sent: there
	// is no request to make, and sending an empty one would spend a slot to be told so.
	quiet, bodies, _ := serve(t, http.StatusOK, `{"choices":[{"message":{"content":"x"}}]}`)
	client, _ = New(quiet, "m", "", time.Second)
	if _, err := client.Ask(context.Background(), Task{Name: "empty", Prompt: "  "}, nil); err == nil {
		t.Fatal("Ask() sent a request with nothing in it")
	}
	if len(*bodies) != 0 {
		t.Fatal("an empty prompt reached the endpoint")
	}
}

func TestRenderExpandsWhatItKnows(t *testing.T) {
	t.Parallel()

	got := Render("a {draft} b {language} c {unknown_field} d", fields())
	// Unknown names are left alone: "{like this}" in a prompt is text somebody meant to
	// be there, and blanking it would silently change what they wrote.
	want := "a the quick brown b English c {unknown_field} d"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
	// One pass, so a value containing a placeholder is not re-expanded — the same rule
	// the notification templates follow, and the reason a draft cannot rewrite a prompt.
	if got := Render("{draft}", map[string]string{"{draft}": "{language}", "{language}": "English"}); got != "{language}" {
		t.Fatalf("Render() = %q, want one pass only", got)
	}
}

func TestAskCarriesTemperatureWhenTheTaskSetsOne(t *testing.T) {
	t.Parallel()

	url, bodies, _ := serve(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)
	client, _ := New(url, "m", "", time.Second)
	task := completeTask()
	task.Temperature = 0.4
	if _, err := client.Ask(context.Background(), task, fields()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := (*bodies)[0]["temperature"]; got != 0.4 {
		t.Errorf("temperature = %v, want 0.4", got)
	}
}

// A 200 whose body is not JSON (a proxy's HTML page) says so, instead of "no reply".
func TestAskReportsAnUndecodableReply(t *testing.T) {
	t.Parallel()
	url, _, _ := serve(t, http.StatusOK, `<html>gateway</html>`)
	client, _ := New(url, "m", "", time.Second)
	_, err := client.Ask(context.Background(), completeTask(), fields())
	if err == nil || !strings.Contains(err.Error(), "decode reply") {
		t.Fatalf("Ask() error = %v, want the decode failure named", err)
	}
}
