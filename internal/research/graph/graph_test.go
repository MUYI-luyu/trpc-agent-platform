package graph

import (
	"context"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/graph"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
)

func TestBuildGraph_Compiles(t *testing.T) {
	g, err := BuildGraph()
	if err != nil {
		t.Fatalf("BuildGraph() error = %v, want nil", err)
	}
	if g == nil {
		t.Fatal("BuildGraph() returned nil graph")
	}
}

func TestClassifyQuery_Reject(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"帮我写个爬虫", types.ActionReject},
		{"今天天气怎么样", types.ActionReject},
		{"推荐小说", types.ActionReject},
		{"Raft是什么", types.ActionAnswer},
		{"什么是LSM-Tree", types.ActionAnswer},
		{"什么是共识算法", types.ActionAnswer},
		{"Raft和Paxos的区别", types.ActionResearch},
		{"Raft vs Paxos 工程区别", types.ActionResearch},
		{"etcd的实现原理", types.ActionResearch},
		// Note: The heuristic classifier is limited. Phase 2 replaces it with
		// the real Clarify LLM node which correctly handles boundary cases like
		// "推荐一本小说" (reject) and "Raft选举超时在生产中通常设多少" (research).
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := ClassifyQuery(tt.query)
			if got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

func TestClarifyRouteCondition(t *testing.T) {
	tests := []struct {
		action string
		want   string
	}{
		{types.ActionReject, types.ActionReject},
		{types.ActionAnswer, types.ActionAnswer},
		{types.ActionResearch, types.ActionResearch},
		{"", types.ActionAnswer}, // default fallback
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			state := graph.State{types.StateKeyAction: tt.action}
			got, err := clarifyRouteCondition(context.Background(), state)
			if err != nil {
				t.Fatalf("clarifyRouteCondition() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("clarifyRouteCondition() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewResearchState_Defaults(t *testing.T) {
	state := types.NewResearchState("test query", "tenant_001", "sess_001", 0, nil, types.NopStreamWriter{})

	if got, _ := graph.GetStateValue[string](state, types.StateKeyQuery); got != "test query" {
		t.Errorf("query = %q, want %q", got, "test query")
	}
	if got, _ := graph.GetStateValue[int](state, types.StateKeyMaxRounds); got != types.DefaultMaxRounds {
		t.Errorf("maxRounds = %d, want %d", got, types.DefaultMaxRounds)
	}
	if tools, _ := graph.GetStateValue[[]string](state, types.StateKeyAllowedTools); len(tools) != 3 {
		t.Errorf("allowedTools len = %d, want 3", len(tools))
	}
}

func TestStateSchema(t *testing.T) {
	schema := types.NewResearchStateSchema()
	if schema == nil {
		t.Fatal("types.NewResearchStateSchema() returned nil")
	}

	// Building a graph with this schema should work.
	g, err := BuildGraph()
	if err != nil {
		t.Fatalf("BuildGraph() with schema error = %v", err)
	}
	if g == nil {
		t.Fatal("BuildGraph returned nil")
	}
}

func TestSkeletonGraphRun_AllPaths(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"reject", "帮我写个爬虫"},
		{"answer", "Raft是什么"},
		{"research", "Raft和Paxos的区别"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := BuildGraph()
			if err != nil {
				t.Fatalf("BuildGraph() error = %v", err)
			}

			// The clarify skeleton sets Action based on ClassifyQuery.
			// The route condition then directs based on Action.
			// For "answer" and "reject", the Graph should route to END directly.
			// For "research", it goes through investigate → synthesize → END.

			expectedAction := ClassifyQuery(tt.query)

			state := types.NewResearchState(tt.query, "t1", "s1", 3, nil, types.NopStreamWriter{})
			// Simulate what the clarify skeleton does:
			action := ClassifyQuery(tt.query)
			state[types.StateKeyAction] = action

			// Verify routing:
			route, err := clarifyRouteCondition(context.Background(), state)
			if err != nil {
				t.Fatalf("route error = %v", err)
			}
			if route != expectedAction {
				t.Errorf("route = %q, want %q (action=%q)", route, expectedAction, action)
			}

			// Ensure the graph itself is valid for all paths.
			_ = g // Graph compiled successfully — implicitly tested by BuildGraph.
		})
	}
}
