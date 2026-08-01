// Package research implements the Research Agent, a 3-Node Graph Agent that
// validates all platform capabilities (Branch, Fan-out, Tool Calling, Streaming,
// Session, Memory, Filter, Tracing).
package research

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
		})
}
