package nodes

import (
	"context"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// fakeCallableTool implements tool.CallableTool for testing executeTool.
type fakeCallableTool struct {
	name   string
	result any
}

func (f fakeCallableTool) Declaration() *tool.Declaration {
	return &tool.Declaration{Name: f.name}
}

func (f fakeCallableTool) Call(ctx context.Context, jsonArgs []byte) (any, error) {
	return f.result, nil
}

func TestExecuteTool_AllowedAndCalled(t *testing.T) {
	tools := map[string]tool.Tool{
		"web_search": fakeCallableTool{name: "web_search", result: map[string]any{"ok": true}},
	}
	r := &RealInvestigateRunner{tools: tools}

	tc := model.ToolCall{Function: model.FunctionDefinitionParam{Name: "web_search", Arguments: []byte(`{"q":"raft"}`)}}
	got, err := r.executeTool(context.Background(), tools, tc)
	if err != nil {
		t.Fatalf("executeTool() error = %v, want nil", err)
	}
	if got == "" {
		t.Fatal("executeTool() returned empty result, want serialized JSON")
	}
}

func TestExecuteTool_UnknownToolRejected(t *testing.T) {
	// Boundary: a tool name absent from the (already filtered) set must be
	// rejected even though the model asked for it — the defense-in-depth layer.
	tools := map[string]tool.Tool{}
	r := &RealInvestigateRunner{tools: tools}

	tc := model.ToolCall{Function: model.FunctionDefinitionParam{Name: "host_exec"}}
	if _, err := r.executeTool(context.Background(), tools, tc); err == nil {
		t.Fatal("executeTool() expected error for unknown tool, got nil")
	}
}

func TestExecuteTool_NonCallableToolRejected(t *testing.T) {
	tools := map[string]tool.Tool{
		"web_search": fakeTool{name: "web_search"}, // Declaration only, not CallableTool
	}
	r := &RealInvestigateRunner{tools: tools}

	tc := model.ToolCall{Function: model.FunctionDefinitionParam{Name: "web_search"}}
	if _, err := r.executeTool(context.Background(), tools, tc); err == nil {
		t.Fatal("executeTool() expected error for non-callable tool, got nil")
	}
}
