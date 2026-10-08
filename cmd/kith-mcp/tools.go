package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// tool is one entry of tools/list, plus what runs it.
type tool struct {
	name    string
	summary string
	// schema is hand-written: its descriptions are what the assistant reads.
	schema map[string]any
	run    func(s *server, ctx context.Context, args json.RawMessage) (any, error)
}

func (s *server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		summary := t.summary
		if t.name == sendTool {
			summary += s.writePolicy()
		}
		out = append(out, map[string]any{
			"name":        t.name,
			"description": summary,
			"inputSchema": t.schema,
		})
	}
	return out
}

// sendTool is the one tool whose description depends on the config.
const sendTool = "send_message"

// writePolicy is `[agent.write]` said to the assistant, appended to send_message's
// description.
func (s *server) writePolicy() string {
	if !s.readScope().Shares() {
		return " WHERE YOU MAY WRITE: nowhere yet. " + nothingSharedNote
	}
	var b strings.Builder
	b.WriteString(" WHERE YOU MAY WRITE is set by [agent.write], and is always inside what you may read: ")
	switch only, except := listed(s.write.Only), listed(s.write.Except); {
	case only != "":
		b.WriteString("only rooms matching " + only)
	case except != "":
		b.WriteString("every room you can read except " + except)
	default:
		b.WriteString("every room you can read")
	}
	if s.write.Encrypted {
		b.WriteString(", encrypted rooms included")
	} else {
		b.WriteString(", and never an encrypted room")
	}
	b.WriteString(". Any other room is refused and nothing is written; list_rooms marks the rooms you may write to as writable.")
	if send := listed(s.send); send != "" {
		b.WriteString(" Of those, rooms matching " + send + " are sent to (unless the room is resting after a recent send); the rest are drafted.")
	} else {
		b.WriteString(" Nothing is sent unreviewed: every message you write becomes a draft.")
	}
	return b.String()
}

// listed is a config list as a phrase, empty when it names nothing.
func listed(entries []string) string {
	kept := make([]string, 0, len(entries))
	for _, e := range entries {
		if e = strings.TrimSpace(e); e != "" {
			kept = append(kept, e)
		}
	}
	return strings.Join(kept, ", ")
}

// call runs one tool.
func (s *server) call(params json.RawMessage) (any, *rpcError) {
	var req struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	for i := range tools {
		if tools[i].name != req.Name {
			continue
		}
		// Nothing shared: refuse up front. list_rooms answers with a note instead, and
		// send_message refuses itself so the attempt reaches the ledger.
		if !s.readScope().Shares() && tools[i].name != "list_rooms" && tools[i].name != sendTool {
			return toolError(errNothingShared), nil
		}
		return s.run(&tools[i], req.Args), nil
	}
	return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool " + req.Name}
}

// run executes one tool under callTimeout.
func (s *server) run(t *tool, raw json.RawMessage) any {
	ctx, cancel := context.WithTimeout(withCallMemo(context.Background()), callTimeout)
	defer cancel()
	if selves, err := s.backend.Selves(ctx); err == nil {
		s.selves = selves
	} else {
		s.logger().Warn("ask who this person is failed; messages are marked mine by the Matrix account alone", "err", err)
	}
	dir, err := s.backend.Directory(ctx)
	if err != nil {
		s.logger().Warn("read the directory failed; people are named as their networks name them", "err", err)
	}
	s.people = setup.PeopleOf(s.aliases, dir)
	result, err := t.run(s, ctx, raw)
	if err != nil {
		// The assistant reads the reason; the log keeps it for the person. Tool
		// errors name rooms and scope, never message text.
		s.logger().Warn("tool call failed", "tool", t.name, "err", err)
		return toolError(err)
	}
	return asJSON(result)
}

// schema is shorthand for the object schemas below, all of which have the same shape.
func schema(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func list(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

var tools = []tool{
	{
		name: "list_rooms",
		summary: "List the rooms this account is in, most recently active first. " +
			"Use it to find a room by name, or to see what is unread.",
		schema: schema(map[string]any{
			"query": str("optional: only rooms whose name contains this"),
			"limit": num("optional: how many to return (default 40)"),
		}),
		run: (*server).listRooms,
	},
	{
		name: "find_rooms_with",
		summary: "Find the rooms a set of people are ALL in, most recently active first. " +
			"This is how to answer 'the room with Noa and Evgeny in it': pass both names, " +
			"and a second name can only narrow the result. Names are matched against display " +
			"names and Matrix IDs.",
		schema: schema(map[string]any{
			"people": list("the people who must all be in the room — display names or Matrix IDs"),
			"limit":  num("optional: how many rooms to return (default 10)"),
		}, "people"),
		run: (*server).findRoomsWith,
	},
	{
		name: "find_people",
		summary: "Find people by name among those who have posted in the rooms you may read, " +
			"returning their Matrix IDs. " +
			"Use it first when a request names somebody, then pass the IDs to find_rooms_with.",
		schema: schema(map[string]any{
			"query": str("part of a display name or Matrix ID"),
			"limit": num("optional: how many to return (default 10)"),
		}, "query"),
		run: (*server).findPeople,
	},
	{
		name: "read_room",
		summary: "Read the most recent messages in one room, oldest first. " +
			"Use it to catch up on a conversation, or to see how this person writes in it.",
		schema: schema(map[string]any{
			"room":   str("the room's ID, or its name as shown in the client"),
			"limit":  num("optional: how many messages (default 40, max 200)"),
			"sender": str("optional: only messages from this Matrix ID — pass the account's own to see how they write"),
			"thread": str("optional: only this thread — its root's event ID, or any message's in it (a message's \"thread\")"),
		}, "room"),
		run: (*server).readRoom,
	},
	{
		name: "search_messages",
		summary: "Search this account's cached messages by words. " +
			"Matching is by term, not meaning: if the first search misses, try the words the " +
			"person would actually have typed, and try them in their own language. " +
			"Use read_around on a hit to see what was actually being asked.",
		schema: schema(map[string]any{
			"query":  str("the words to look for"),
			"room":   str("optional: restrict to one room (ID or name)"),
			"sender": str("optional: only messages from this Matrix ID"),
			"since":  str("optional: RFC3339 date — only messages after it"),
			"limit":  num("optional: how many hits (default 20)"),
		}, "query"),
		run: (*server).searchMessages,
	},
	{
		name: "read_around",
		summary: "Read the conversation around one message — what came before and after it. " +
			"A search hit is a snippet; what somebody was asked to do is usually in the " +
			"messages after it.",
		schema: schema(map[string]any{
			"room":   str("the room's ID"),
			"event":  str("the message's event ID, from a search hit"),
			"before": num("optional: how many messages before it (default 5)"),
			"after":  num("optional: how many after it (default 10)"),
		}, "room", "event"),
		run: (*server).readAround,
	},
	{
		name: "unread_summary",
		summary: "What is waiting: the rooms with unread messages, and how many. " +
			"Use it to answer 'what have I missed'.",
		schema: schema(map[string]any{
			"limit": num("optional: how many rooms (default 20)"),
		}),
		run: (*server).unreadSummary,
	},
	{
		name: sendTool,
		summary: "Write to a room. Whether this SENDS the message or leaves it as a DRAFT " +
			"in that room's composer is decided by the account's own configuration, not by " +
			"you and not by anything you pass: rooms the person listed are sent to, every " +
			"other room you may write to is drafted. The answer says which happened and why, so say that " +
			"back to them rather than assuming — \"drafted\" means a person still has to " +
			"press send. Write the message as they would write it; nothing is added to it. " +
			"To answer inside a thread, pass the thread (read_room with thread shows it).",
		schema: schema(map[string]any{
			"room": str("the room's ID, or its name as shown in the client"),
			"text": str("the message, exactly as it should appear"),
			"thread": str("optional: write into this thread — its root's event ID, or any message's in it " +
				"(a message's \"thread\" field). Without it the message goes to the room's main timeline"),
			"reply_to": str("optional: the event ID of the message this answers. A message in a thread " +
				"is answered in that thread"),
		}, "room", "text"),
		run: (*server).sendMessage,
	},
}

// roomView is how a room is handed back: identified by ID, shown by name.
type roomView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Direct bool   `json:"direct_message,omitempty"`
	Unread int    `json:"unread,omitempty"`
	// Writable marks a room send_message may write to; only list_rooms fills it in.
	Writable bool `json:"writable,omitempty"`
}

func viewOf(room *domain.Room) roomView {
	return roomView{ID: string(room.ID), Name: room.DisplayName(), Direct: room.IsDirect}
}

// messageView is one message, in the terms somebody reading a conversation needs.
type messageView struct {
	EventID string `json:"event_id"`
	Room    string `json:"room,omitempty"`
	Sender  string `json:"sender"`
	Name    string `json:"sender_name,omitempty"`
	Sent    string `json:"sent"`
	Body    string `json:"body"`
	// Mine marks the account's own messages.
	Mine bool `json:"mine,omitempty"`
	// Thread is the root of the thread this message is in; ReplyTo is what it answers.
	Thread  string `json:"thread,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
}

func (s *server) view(msg domain.Message, withRoom bool) messageView {
	// Mentions name who they mention as everyone else is named here.
	body, _ := domain.ResolveMentions(msg.Body, msg.Mentions, func(mn domain.Mention, words string) string {
		return s.people.Name(mn.UserID, mn.Known, words)
	})
	out := messageView{
		EventID: string(msg.ID),
		Sender:  msg.Sender,
		Name:    s.people.Name(msg.Sender, msg.SenderName),
		Sent:    msg.Timestamp.Format(time.RFC3339),
		Body:    body,
		Mine:    msg.Sender != "" && slices.Contains(s.selves, msg.Sender),
		Thread:  string(msg.ThreadRoot),
		ReplyTo: string(msg.ReplyTo),
	}
	if withRoom {
		out.Room = string(msg.RoomID)
	}
	if msg.Redacted {
		out.Body = "(deleted)"
	}
	return out
}

func args[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("reading the arguments: %w", err)
	}
	return out, nil
}

func limitOr(n, fallback, most int) int {
	if n <= 0 {
		return fallback
	}
	if most > 0 && n > most {
		return most
	}
	return n
}
