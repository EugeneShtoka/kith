package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/llm"
)

// fakeEndpoint serves one canned completion and records the bodies it was sent.
func fakeEndpoint(t *testing.T, reply string) (string, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + reply + `"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1/chat/completions", &got
}

func modelTasks() map[string]llm.Task {
	return map[string]llm.Task{
		domain.ModelRewrite: {
			Name:      domain.ModelRewrite,
			System:    "finish sentences in {language}",
			Prompt:    "context:\n{context}\n\ndraft:\n{draft}",
			MaxTokens: 32,
		},
		domain.ModelSummary: {
			Name:      domain.ModelSummary,
			System:    "summarize for {me} in {language}",
			Prompt:    "conversation:\n{context}",
			MaxTokens: 400,
		},
		domain.ModelTodo: {
			Name:      domain.ModelTodo,
			System:    "what does {me} owe people",
			Prompt:    "conversations:\n{context}",
			MaxTokens: 400,
		},
	}
}

func TestModelTaskRefusesWhatWasNotOptedIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "and the rest")

	cases := []struct {
		name     string
		settings ModelSettings
		want     string
	}{
		{
			"nothing configured, which is the default",
			ModelSettings{Tasks: modelTasks()},
			domain.ModelRefusedOff,
		},
		{
			"a room nobody named",
			ModelSettings{
				Endpoint: endpoint, Scope: domain.ModelScope{Only: []string{"!elsewhere:x"}, Encrypted: true},
				Tasks: modelTasks(), PerMinute: 10,
			},
			domain.ModelRefusedRoom,
		},
		{
			// Unknown encryption state counts as encrypted.
			"encryption unknown, and encrypted rooms are out",
			ModelSettings{Endpoint: endpoint, Tasks: modelTasks(), PerMinute: 10},
			domain.ModelRefusedEncrypted,
		},
		{
			"a task with no prompt",
			ModelSettings{
				Endpoint: endpoint, Scope: domain.ModelScope{Encrypted: true},
				Tasks: map[string]llm.Task{}, PerMinute: 10,
			},
			`no prompt configured for the "rewrite" task`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := New(nil, nil)
			b.UseModel(tc.settings)
			got, err := b.ModelTask(ctx, domain.ModelRequest{
				Task: domain.ModelRewrite, RoomID: "!a:x", Draft: "the quick brown ",
			})
			if err != nil {
				t.Fatalf("ModelTask() error = %v", err)
			}
			if got.Refusal != tc.want {
				t.Fatalf("refusal = %q, want %q", got.Refusal, tc.want)
			}
			if got.Asked() {
				t.Fatal("a refusal reported itself as asked")
			}
		})
	}
	// A refusal must not have sent anything.
	if len(*sent) != 0 {
		t.Fatalf("the endpoint was called %d times while every ask was refused", len(*sent))
	}
}

// A room with no cache is still answered, with an empty context.
func TestModelTaskAsksWhenOptedIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "fox jumps over")

	b := New(nil, nil)
	b.UseModel(ModelSettings{
		Endpoint: endpoint, Model: "test-model",
		Scope: domain.ModelScope{Only: []string{"!a:x"}, Encrypted: true},
		Tasks: modelTasks(), PerMinute: 10, Budget: 500, Timeout: 2 * time.Second,
	})

	got, err := b.ModelTask(ctx, domain.ModelRequest{
		Task: domain.ModelRewrite, RoomID: "!a:x", Draft: "the quick brown ",
	})
	if err != nil {
		t.Fatalf("ModelTask() error = %v", err)
	}
	if !got.Asked() || got.Text != "fox jumps over" {
		t.Fatalf("ModelTask() = %+v, want the model's reply as it came", got)
	}
	if got.Endpoint != endpoint || got.Model != "test-model" {
		t.Errorf("result names %q/%q, want the endpoint and model that answered", got.Endpoint, got.Model)
	}
	if len(*sent) != 1 {
		t.Fatalf("the endpoint was called %d times, want once", len(*sent))
	}
	body := (*sent)[0]
	if body["model"] != "test-model" {
		t.Errorf("body model = %v, want test-model", body["model"])
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("body has %d messages, want a system and a user one", len(messages))
	}
	user, _ := messages[1].(map[string]any)
	if content, _ := user["content"].(string); !strings.Contains(content, "the quick brown") {
		t.Errorf("user message = %q, want the draft in it", content)
	}
}

// The cap is a rolling minute, not a counter reset on the minute.
func TestModelTaskCapIsARollingMinute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "ok")

	b := New(nil, nil)
	b.UseModel(ModelSettings{
		Endpoint: endpoint, Scope: domain.ModelScope{Only: []string{"!a:x"}, Encrypted: true},
		Tasks: modelTasks(), PerMinute: 2,
	})
	clock := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	b.model.now = func() time.Time { return clock }

	ask := func() domain.ModelResult {
		got, err := b.ModelTask(ctx, domain.ModelRequest{
			Task: domain.ModelRewrite, RoomID: "!a:x", Draft: "one two three ",
		})
		if err != nil {
			t.Fatalf("ModelTask() error = %v", err)
		}
		return got
	}

	for i := range 2 {
		if !ask().Asked() {
			t.Fatalf("ask %d of 2 inside the cap was refused", i+1)
		}
	}
	third := ask()
	if third.Asked() {
		t.Fatal("the third ask went out past a cap of two")
	}
	if !strings.Contains(third.Refusal, "cap") {
		t.Errorf("refusal = %q, want it to name the cap", third.Refusal)
	}
	clock = clock.Add(61 * time.Second)
	if !ask().Asked() {
		t.Fatal("the window did not roll")
	}
	if len(*sent) != 3 {
		t.Fatalf("the endpoint was called %d times, want 3 (two, one refused, one after the window)", len(*sent))
	}
}

// A dry run returns the assembled request in full and sends nothing.
func TestModelTaskDryRunSendsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "should never be reached")

	b := New(nil, nil)
	b.UseModel(ModelSettings{
		Endpoint: endpoint, Scope: domain.ModelScope{Only: []string{"!a:x"}, Encrypted: true},
		Tasks: modelTasks(), PerMinute: 10,
	})

	got, err := b.ModelTask(ctx, domain.ModelRequest{
		Task: domain.ModelRewrite, RoomID: "!a:x", Draft: "the quick brown ", DryRun: true,
	})
	if err != nil {
		t.Fatalf("ModelTask() error = %v", err)
	}
	if len(*sent) != 0 {
		t.Fatal("a dry run reached the endpoint")
	}
	for _, want := range []string{"system:", "finish sentences", "user:", "the quick brown"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("preview = %q, want %q in it", got.Text, want)
		}
	}
	// A dry run is allowed with nothing typed; a real ask is not.
	empty, err := b.ModelTask(ctx, domain.ModelRequest{
		Task: domain.ModelRewrite, RoomID: "!a:x", DryRun: true,
	})
	if err != nil {
		t.Fatalf("ModelTask(empty draft) error = %v", err)
	}
	if !empty.Asked() {
		t.Fatalf("a dry run with no draft was refused: %q", empty.Refusal)
	}
	real, err := b.ModelTask(ctx, domain.ModelRequest{Task: domain.ModelRewrite, RoomID: "!a:x"})
	if err != nil {
		t.Fatalf("ModelTask(real, empty) error = %v", err)
	}
	if real.Asked() {
		t.Fatal("an ask with nothing to work from went out")
	}
}

func TestModelLanguageFollowsTheDraft(t *testing.T) {
	t.Parallel()

	for draft, want := range map[string]string{
		"the quick brown fox": "English",
		"שלום לכולם":          "Hebrew",
		"привет всем":         "Russian",
		"1234 ?!":             "",
	} {
		if got := modelLanguage(draft); got != want {
			t.Errorf("modelLanguage(%q) = %q, want %q", draft, got, want)
		}
	}
}

// The context is this room's messages and nothing else.
func TestModelTaskQuotesOnlyThisRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "ok")

	cache := testCache(t)
	if saveErr := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!here:x", Name: "Here"}, {ID: "!other:x", Name: "Other"}}); saveErr != nil {
		t.Fatalf("SaveRooms: %v", saveErr)
	}
	save := func(room domain.RoomID, body string) {
		t.Helper()
		if saveErr := cache.SaveMessages(ctx, room, []domain.Message{{
			ID: domain.EventID(string(room) + "$1"), RoomID: room, Sender: "@dana:x",
			SenderName: "Dana", Body: body, Timestamp: time.Unix(1_700_000_000, 0),
		}}); saveErr != nil {
			t.Fatalf("SaveMessages: %v", saveErr)
		}
	}
	save("!here:x", "quoting-this-is-fine")
	save("!other:x", "must-never-be-quoted")

	b := New(cache, nil)
	b.UseModel(ModelSettings{
		Endpoint: endpoint, Scope: domain.ModelScope{Only: []string{"!here:x"}, Encrypted: true},
		Tasks: modelTasks(), PerMinute: 10, Budget: 500,
	})

	if _, askErr := b.ModelTask(ctx, domain.ModelRequest{
		Task: domain.ModelRewrite, RoomID: "!here:x", Draft: "one two three ",
	}); askErr != nil {
		t.Fatalf("ModelTask() error = %v", askErr)
	}
	if len(*sent) != 1 {
		t.Fatalf("the endpoint was called %d times, want once", len(*sent))
	}
	whole := requestBody(t, sent)
	if !strings.Contains(whole, "quoting-this-is-fine") {
		t.Error("the open room's own messages were not quoted, so there is no context at all")
	}
	if strings.Contains(whole, "must-never-be-quoted") {
		t.Fatal("another room's message left the machine")
	}

	// The scope may name the room by its display name.
	b.UseModel(ModelSettings{
		Endpoint: endpoint, Scope: domain.ModelScope{Only: []string{"room:Here"}, Encrypted: true},
		Tasks: modelTasks(), PerMinute: 10, Budget: 500,
	})
	got, err := b.ModelTask(ctx, domain.ModelRequest{
		Task: domain.ModelRewrite, RoomID: "!here:x", Draft: "one two three ",
	})
	if err != nil {
		t.Fatalf("ModelTask(by name) error = %v", err)
	}
	if !got.Asked() {
		t.Fatalf("naming the room by its display name refused it: %q", got.Refusal)
	}
}

// Regression: /summary in a room read to the end summarized nothing; it must fall back
// to the recent conversation.
func TestSummaryFallsBackWhenYouAreUpToDate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "• the deploy is done")

	cache := testCache(t)
	if saveErr := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!here:x", Name: "Here"}}); saveErr != nil {
		t.Fatalf("SaveRooms: %v", saveErr)
	}
	var msgs []domain.Message
	for i := range 9 {
		msgs = append(msgs, domain.Message{
			ID: domain.EventID(fmt.Sprintf("$%d", i)), RoomID: "!here:x", Sender: "@dana:x",
			SenderName: "Dana", Body: fmt.Sprintf("message %d", i),
			Timestamp: time.Unix(int64(1_700_000_000+i*60), 0),
		})
	}
	if saveErr := cache.SaveMessages(ctx, "!here:x", msgs); saveErr != nil {
		t.Fatalf("SaveMessages: %v", saveErr)
	}

	b := New(cache, nil)
	b.UseModel(ModelSettings{
		Endpoint: endpoint, Scope: domain.ModelScope{Encrypted: true},
		Tasks: modelTasks(), PerMinute: 10, Budget: 500,
		Budgets: map[string]int{domain.ModelSummary: 4000},
	})
	summarize := func() domain.ModelResult {
		t.Helper()
		got, askErr := b.ModelTask(ctx, domain.ModelRequest{Task: domain.ModelSummary, RoomID: "!here:x"})
		if askErr != nil {
			t.Fatalf("ModelTask: %v", askErr)
		}
		return got
	}

	if saveErr := cache.SaveUnread(ctx, domain.Unread{RoomID: "!here:x", ReadEvent: "$8"}, 0); saveErr != nil {
		t.Fatalf("SaveUnread: %v", saveErr)
	}
	got := summarize()
	if !got.Asked() {
		t.Fatalf("a read room refused to summarize: %q", got.Refusal)
	}
	if !strings.Contains(got.Note, "up to date") {
		t.Errorf("note = %q, want it to say the room is read and this is the recent conversation", got.Note)
	}
	if body := requestBody(t, sent); !strings.Contains(body, "message 8") {
		t.Error("the fallback sent no conversation at all")
	}

	if saveErr := cache.SaveUnread(ctx, domain.Unread{RoomID: "!here:x", ReadEvent: "$1"}, 0); saveErr != nil {
		t.Fatalf("SaveUnread: %v", saveErr)
	}
	got = summarize()
	if !strings.Contains(got.Note, "7 messages since you last read") {
		t.Errorf("note = %q, want it to count what was missed", got.Note)
	}
	body := requestBody(t, sent)
	if strings.Contains(body, "message 0") {
		t.Error("the unread span included what had already been read")
	}

	// A room with nothing cached is refused without a request.
	if saveErr := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!empty:x", Name: "Empty"}}); saveErr != nil {
		t.Fatalf("SaveRooms: %v", saveErr)
	}
	before := len(*sent)
	empty, err := b.ModelTask(ctx, domain.ModelRequest{Task: domain.ModelSummary, RoomID: "!empty:x"})
	if err != nil {
		t.Fatalf("ModelTask(empty room): %v", err)
	}
	if empty.Asked() {
		t.Fatal("an empty room was summarized")
	}
	if len(*sent) != before {
		t.Fatal("an empty room still cost a request")
	}
}

// requestBody concatenates the message contents of the most recent request.
func requestBody(t *testing.T, sent *[]map[string]any) string {
	t.Helper()
	if len(*sent) == 0 {
		t.Fatal("nothing was sent")
	}
	messages, _ := (*sent)[len(*sent)-1]["messages"].([]any)
	var whole strings.Builder
	for _, entry := range messages {
		msg, _ := entry.(map[string]any)
		content, _ := msg["content"].(string)
		whole.WriteString(content)
	}
	return whole.String()
}

// :todo picks its own rooms: it reads unread ones and honors the scope for them too.
func TestTodoReadsUnreadRoomsWithinTheScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "• Work — Dana — waiting for the figures")

	cache := testCache(t)
	rooms := []domain.Room{
		{ID: "!busy:x", Name: "Busy"},
		{ID: "!quiet:x", Name: "Quiet"},
		{ID: "!banned:x", Name: "Banned"},
	}
	if saveErr := cache.SaveRooms(ctx, domain.MatrixRooms, rooms); saveErr != nil {
		t.Fatalf("SaveRooms: %v", saveErr)
	}
	say := func(room domain.RoomID, bodies ...string) {
		t.Helper()
		var msgs []domain.Message
		for i, body := range bodies {
			msgs = append(msgs, domain.Message{
				ID: domain.EventID(string(room) + string(rune('a'+i))), RoomID: room,
				Sender: "@dana:x", SenderName: "Dana", Body: body,
				Timestamp: time.Unix(int64(1_700_000_000+i*60), 0),
			})
		}
		if saveErr := cache.SaveMessages(ctx, room, msgs); saveErr != nil {
			t.Fatalf("SaveMessages: %v", saveErr)
		}
	}
	say("!busy:x", "did you send the figures", "still waiting on those")
	say("!quiet:x", "nothing to do here")
	say("!banned:x", "this must never be quoted")
	// Notifications, not Messages: SaveUnread does not store the derived count.
	for _, u := range []domain.Unread{
		{RoomID: "!busy:x", Notifications: 2},
		{RoomID: "!banned:x", Notifications: 1},
		{RoomID: "!quiet:x", ReadEvent: "!quiet:xa"},
	} {
		if saveErr := cache.SaveUnread(ctx, u, 0); saveErr != nil {
			t.Fatalf("SaveUnread: %v", saveErr)
		}
	}

	b := New(cache, nil)
	b.UseModel(ModelSettings{
		Endpoint: endpoint,
		Scope:    domain.ModelScope{Except: []string{"room:Banned"}, Encrypted: true},
		Tasks:    modelTasks(), PerMinute: 10, Budget: 500,
		Budgets: map[string]int{domain.ModelTodo: 4000}, TodoRooms: 5,
	})

	got, err := b.ModelTask(ctx, domain.ModelRequest{Task: domain.ModelTodo})
	if err != nil {
		t.Fatalf("ModelTask: %v", err)
	}
	if !got.Asked() {
		t.Fatalf("todo refused: %q", got.Refusal)
	}
	if !strings.Contains(got.Note, "1 rooms") {
		t.Errorf("note = %q, want it to count the rooms read", got.Note)
	}
	body := requestBody(t, sent)
	if !strings.Contains(body, "still waiting on those") {
		t.Error("the unread room was not quoted")
	}
	if strings.Contains(body, "must never be quoted") {
		t.Fatal("a room outside the scope was sent")
	}
	if strings.Contains(body, "nothing to do here") {
		t.Error("a room with nothing unread was quoted")
	}
	if !strings.Contains(body, "## Busy") {
		t.Error("the quoted conversation is not grouped by room")
	}
}

// Nothing unread anywhere is answered without a request.
func TestTodoWithNothingUnreadCostsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "should never be reached")

	cache := testCache(t)

	b := New(cache, nil)
	b.UseModel(ModelSettings{
		Endpoint: endpoint, Scope: domain.ModelScope{Encrypted: true},
		Tasks: modelTasks(), PerMinute: 10, Budgets: map[string]int{domain.ModelTodo: 4000}, TodoRooms: 5,
	})
	got, err := b.ModelTask(ctx, domain.ModelRequest{Task: domain.ModelTodo})
	if err != nil {
		t.Fatalf("ModelTask: %v", err)
	}
	if got.Asked() {
		t.Fatal("an empty todo still asked the model")
	}
	if !strings.Contains(got.Refusal, "up to date") {
		t.Errorf("refusal = %q, want it to say there is nothing unread", got.Refusal)
	}
	if len(*sent) != 0 {
		t.Fatal("an empty todo cost a request")
	}
}

// With the cache off, where a room sits is unknown: a scope that reads spaces or
// networks is refused rather than decided as if the room were in none, and a scope that
// names rooms only is still answered.
func TestModelTaskWithNoCacheRefusesAScopeThatNamesPlaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoint, sent := fakeEndpoint(t, "fox")
	for _, scope := range []domain.ModelScope{
		{Except: []string{"space:Work"}, Encrypted: true},
		{Only: []string{"protocol:matrix"}, Encrypted: true},
	} {
		b := New(nil, nil)
		b.UseModel(ModelSettings{Endpoint: endpoint, Scope: scope, Tasks: modelTasks(), PerMinute: 10})
		if _, err := b.ModelTask(ctx, domain.ModelRequest{Task: domain.ModelRewrite, RoomID: "!a:x", Draft: "x"}); !errors.Is(err, errNoPlaces) {
			t.Fatalf("scope %+v with no cache: error = %v, want errNoPlaces", scope, err)
		}
	}
	if len(*sent) != 0 {
		t.Fatalf("the endpoint was called %d times", len(*sent))
	}
	b := New(nil, nil)
	b.UseModel(ModelSettings{
		Endpoint: endpoint, Scope: domain.ModelScope{Only: []string{"group"}, Encrypted: true},
		Tasks: modelTasks(), PerMinute: 10, Budget: 500, Timeout: 2 * time.Second,
	})
	if got, err := b.ModelTask(ctx, domain.ModelRequest{Task: domain.ModelRewrite, RoomID: "!a:x", Draft: "x"}); err != nil || !got.Asked() {
		t.Fatalf("a rooms-only scope with no cache = %+v, %v, want it asked", got, err)
	}
}
