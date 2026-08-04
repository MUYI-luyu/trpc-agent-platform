package types

import (
	"encoding/json"
	"time"
)

// StreamWriter allows Graph Nodes to push real-time SSE events to the client
// during execution. It is injected into graph.State via StateKeyStreamWriter and
// is not persisted across requests.
type StreamWriter interface {
	Write(event StreamEvent) error
}

// StreamEvent is a progress event pushed from a Node to the client.
type StreamEvent struct {
	// Type is the event type: "think_start", "think_end", "tool_start",
	// "tool_end", "tool_error", "progress", "error", "node_complete".
	Type string `json:"type"`
	// Node identifies the source Node: "clarify", "investigate", "synthesize".
	Node string `json:"node"`
	// Timestamp is the Unix millisecond timestamp of the event.
	Timestamp int64 `json:"timestamp"`
	// Round is the search round number (only relevant for Investigate events).
	Round int `json:"round,omitempty"`
	// Content is the human-readable event description.
	Content string `json:"content"`
	// Metadata carries optional structured data about the event.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// StreamEventType constants.
const (
	EventThinkStart   = "think_start"
	EventThinkEnd     = "think_end"
	EventToolStart    = "tool_start"
	EventToolEnd      = "tool_end"
	EventToolError    = "tool_error"
	EventProgress     = "progress"
	EventError        = "error"
	EventNodeComplete = "node_complete"
)

// NewStreamEvent creates a StreamEvent with a timestamp and node set.
func NewStreamEvent(evenrType, node string, content string) StreamEvent {
	return StreamEvent{
		Type:      evenrType,
		Node:      node,
		Timestamp: time.Now().UnixMilli(),
		Content:   content,
	}
}

// ─── No-op StreamWriter ─────────────────────────────────────────────────

// NopStreamWriter is a StreamWriter that discards all events. Use as the default
// when no SSE connection is available (e.g., in tests or batch processing).
type NopStreamWriter struct{}

func (NopStreamWriter) Write(_ StreamEvent) error { return nil }

// ─── Marshaling ─────────────────────────────────────────────────────────

// MarshalSSE formats a StreamEvent as a Server-Sent Event data line.
func (e StreamEvent) MarshalSSE() ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	// Format: "event: <type>\ndata: <json>\n\n"
	buf := make([]byte, 0, len(e.Type)+len(data)+20)
	buf = append(buf, "event: "...)
	buf = append(buf, e.Type...)
	buf = append(buf, '\n')
	buf = append(buf, "data: "...)
	buf = append(buf, data...)
	buf = append(buf, '\n', '\n')
	return buf, nil
}
