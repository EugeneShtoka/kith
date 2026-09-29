package main

import (
	"encoding/json"
	"fmt"
)

// MCP by hand: JSON-RPC 2.0 over stdio, one object per line. Notifications (no id) get
// no reply.

// protocolVersion is echoed on initialize; other requested versions are not refused,
// since every method here is unchanged since this revision.
const protocolVersion = "2024-11-05"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC's own codes, for the two things that can go wrong before a tool is reached.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// handle answers one request, and reports whether there is an answer to send.
func (s *server) handle(req *request) (response, bool) {
	if len(req.ID) == 0 {
		// Notification: no reply.
		return response{}, false
	}
	reply := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		// The client's name is recorded as the author of drafts and ledger lines.
		s.nameClient(clientInfo(req.Params))
		reply.Result = map[string]any{
			"protocolVersion": protocolVersion,
			// Tools only: never claim an unimplemented capability.
			"capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    "kith",
				"version": "dev",
			},
			"instructions": s.instructions(),
		}
	case "ping":
		reply.Result = map[string]any{}
	case "tools/list":
		reply.Result = map[string]any{"tools": s.toolList()}
	case "tools/call":
		reply.Result, reply.Error = s.call(req.Params)
	default:
		reply.Error = &rpcError{Code: codeMethodNotFound, Message: "unknown method " + req.Method}
	}
	return reply, true
}

// instructions is what the assistant is told about this server before it uses it.
func (s *server) instructions() string {
	if !s.readScope().Shares() {
		return nothingSharedNote + "\n\n" + instructions
	}
	return instructions
}

const instructions = `This is one person's Matrix account, out of a local cache.

Reading: the cache is what their client has synced, so a very old conversation may be
absent and a room nobody has opened may be thin. You may read only the rooms and spaces
their [agent.read] config lists — an empty list shares nothing — and encrypted rooms are
excluded unless they have said otherwise. A room you cannot see is a deliberate setting,
not an error.

Writing: you may write only in the rooms their [agent.write] config allows, which is
never wider than what you may read; send_message's description says where, and
list_rooms marks those rooms writable. A room outside it is refused. Where you may write,
send_message either sends or leaves a draft in the room's composer, and which of those it
does is decided by the person's configuration — not by you, and not by anything you can
pass. Its answer names what happened; repeat that to them rather than assuming a
message went out. Nothing here can edit or delete anything that was already said.`

// content wraps text as a tool answer's single text block.
func content(text string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	}
}

// toolError is a failure inside a tool: a result the assistant reads, not a protocol error.
func toolError(err error) map[string]any {
	out := content(err.Error())
	out["isError"] = true
	return out
}

// asJSON renders a tool's answer as indented JSON.
func asJSON(v any) map[string]any {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolError(fmt.Errorf("encoding the answer: %w", err))
	}
	return content(string(encoded))
}

// clientInfo is the name and version the client gave on initialize, if any.
func clientInfo(params json.RawMessage) (name, version string) {
	var in struct {
		ClientInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return "", ""
	}
	return in.ClientInfo.Name, in.ClientInfo.Version
}
