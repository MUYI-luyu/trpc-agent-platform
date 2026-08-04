// Package research implements the Research Agent, a 3-Node Graph Agent that
// validates all platform capabilities (Branch, Fan-out, Tool Calling, Streaming,
// Session, Memory, Filter, Tracing).
package types

import (
	"reflect"

	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

// ─── State key constants ───────────────────────────────────────────────

// State keys for the Research Agent's Graph State. Prefixed with "research_"
// to avoid collision with the framework's internal keys (messages, user_input,
// last_response, etc.).
const (
	// StateKeyQuery holds the user's current question (string).
	StateKeyQuery = "research_query"
	// StateKeyTenantID holds the tenant identifier (string).
	StateKeyTenantID = "research_tenant_id"
	// StateKeySessionID holds the session identifier (string).
	StateKeySessionID = "research_session_id"
	// StateKeyAction holds the Clarify routing decision: "reject" | "answer" | "research".
	StateKeyAction = "research_action"
	// StateKeyMessages holds the full conversation history ([]model.Message, append-only).
	// Writers: Clarify + Investigate. Readers: Investigate (first round), Synthesize (report material).
	StateKeyMessages = "research_messages"
	// StateKeyReport holds the final markdown report (string). Written by Synthesize.
	StateKeyReport = "research_report"
	// StateKeyMaxRounds is the maximum search rounds (int). Set by platform governance.
	StateKeyMaxRounds = "research_max_rounds"
	// StateKeyAllowedTools lists the permitted tool names ([]string). Set by platform governance.
	StateKeyAllowedTools = "research_allowed_tools"
	// StateKeyStreamWriter holds the SSE event pusher (StreamWriter). Not persisted.
	StateKeyStreamWriter = "research_stream_writer"
	// StateKeyFindings holds structured research findings extracted from tool
	// results ([]Finding). Written by Investigate, consumed by Synthesize.
	StateKeyFindings = "research_findings"
)

// ─── Action constants ───────────────────────────────────────────────────

const (
	ActionReject   = "reject"
	ActionAnswer   = "answer"
	ActionResearch = "research"
)

// ─── Default values ─────────────────────────────────────────────────────

const (
	DefaultMaxRounds = 5
)

// ─── Finding types ──────────────────────────────────────────────────────

// Finding is a structured research finding extracted from tool results.
// It decouples raw tool output from the Synthesize prompt, giving Synthesize
// verified claims rather than forcing it to parse raw machine logs.
type Finding struct {
	// Claim is the factual assertion extracted from a source.
	Claim string `json:"claim"`
	// Evidence lists the sources supporting this claim.
	Evidence []Source `json:"evidence"`
	// Confidence indicates how reliable the claim is: "high", "medium", "low".
	Confidence string `json:"confidence"`
}

// Source is a reference to where a finding came from.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// ─── State helpers ──────────────────────────────────────────────────────

// NewResearchState creates an initial graph.State with the given inputs and
// default values.
func NewResearchState(query, tenantID, sessionID string, maxRounds int, allowedTools []string, sw StreamWriter) graph.State {
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}
	if allowedTools == nil {
		allowedTools = []string{"search_kb", "web_search", "web_fetch"}
	}
	return graph.State{
		StateKeyQuery:        query,
		StateKeyTenantID:     tenantID,
		StateKeySessionID:    sessionID,
		StateKeyAction:       "",
		StateKeyMessages:     []model.Message{},
		StateKeyReport:       "",
		StateKeyMaxRounds:    maxRounds,
		StateKeyAllowedTools: allowedTools,
		StateKeyStreamWriter: sw,
		StateKeyFindings:     []Finding{},
	}
}

// ─── StateSchema definition ─────────────────────────────────────────────

// NewResearchStateSchema creates the StateSchema for the Research Agent Graph.
func NewResearchStateSchema() *graph.StateSchema {
	return graph.NewStateSchema().
		AddField(StateKeyQuery, graph.StateField{
			Type:    reflect.TypeOf(""),
			Reducer: graph.DefaultReducer,
		}).
		AddField(StateKeyTenantID, graph.StateField{
			Type:    reflect.TypeOf(""),
			Reducer: graph.DefaultReducer,
		}).
		AddField(StateKeySessionID, graph.StateField{
			Type:    reflect.TypeOf(""),
			Reducer: graph.DefaultReducer,
		}).
		AddField(StateKeyAction, graph.StateField{
			Type:    reflect.TypeOf(""),
			Reducer: graph.DefaultReducer,
		}).
		AddField(StateKeyMessages, graph.StateField{
			Type:    reflect.TypeOf([]model.Message{}),
			Reducer: graph.DefaultReducer,
			Default: func() any { return []model.Message{} },
		}).
		AddField(StateKeyReport, graph.StateField{
			Type:    reflect.TypeOf(""),
			Reducer: graph.DefaultReducer,
		}).
		AddField(StateKeyMaxRounds, graph.StateField{
			Type:    reflect.TypeOf(0),
			Reducer: graph.DefaultReducer,
			Default: func() any { return DefaultMaxRounds },
		}).
		AddField(StateKeyAllowedTools, graph.StateField{
			Type:    reflect.TypeOf([]string{}),
			Reducer: graph.DefaultReducer,
			Default: func() any { return []string{"search_kb", "web_search", "web_fetch"} },
		}).
		AddField(StateKeyStreamWriter, graph.StateField{
			Type:            reflect.TypeOf((*StreamWriter)(nil)).Elem(),
			Reducer:         graph.DefaultReducer,
			DisableDeepCopy: true,
		}).
		AddField(StateKeyFindings, graph.StateField{
			Type:    reflect.TypeOf([]Finding{}),
			Reducer: graph.DefaultReducer,
			Default: func() any { return []Finding{} },
		})
}

// truncateForLog truncates a string to maxLen for logging purposes.
func TruncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
