package nodes

import (
	"context"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
)

// mockLLMClient implements LLMClient with pre-programmed responses.
type mockLLMClient struct {
	responses []*model.Response
	info      model.Info
}

func newMockLLMClient(responses ...*model.Response) *mockLLMClient {
	return &mockLLMClient{
		responses: responses,
		info:      model.Info{Name: "mock", ContextWindow: 8192},
	}
}

func (m *mockLLMClient) GenerateContent(_ context.Context, _ *model.Request) (<-chan *model.Response, error) {
	ch := make(chan *model.Response, len(m.responses))
	for _, r := range m.responses {
		ch <- r
	}
	close(ch)
	return ch, nil
}

func (m *mockLLMClient) Info() model.Info { return m.info }

// ─── Action parsing tests ────────────────────────────────────────────────

func TestParseActionFromContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"json reject", `{"action": "reject"}`, types.ActionReject},
		{"json answer", `{"action": "answer"}`, types.ActionAnswer},
		{"json research", `{"action": "research"}`, types.ActionResearch},
		{"json with spaces", `  {"action": "research"}  `, types.ActionResearch},
		{"empty", "", ""},
		{"plain text no action", "This is some reasoning text", ""},
		{"json embedded in text", `Reasoning... {"action": "reject"} more text`, types.ActionReject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseActionFromContent(tt.content)
			if got != tt.want {
				t.Errorf("parseActionFromContent(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestExtractActionFromText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"reject keyword", `I think this should be rejected`, types.ActionReject},
		{"research keyword", "This requires research", types.ActionResearch},
		{"answer keyword", "I can answer this", types.ActionAnswer},
		{"default", "no clear action", types.ActionAnswer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractActionFromText(tt.text)
			if got != tt.want {
				t.Errorf("extractActionFromText(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// ─── Clarify NodeFunc tests ──────────────────────────────────────────────

func TestClarifyNode_Reject(t *testing.T) {
	llm := newMockLLMClient(
		&model.Response{
			IsPartial: false,
			Choices: []model.Choice{{
				Message: model.Message{Content: `{"action": "reject"}`},
			}},
		},
	)

	clarifyFn := NewClarifyNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyQuery:        "帮我写个爬虫",
		types.StateKeyTenantID:     "t1",
		types.StateKeySessionID:    "s1",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := clarifyFn(context.Background(), state)
	if err != nil {
		t.Fatalf("clarifyFn() error = %v", err)
	}

	update, ok := result.(graph.State)
	if !ok {
		t.Fatalf("result is not graph.State, got %T", result)
	}

	action, _ := graph.GetStateValue[string](update, types.StateKeyAction)
	if action != types.ActionReject {
		t.Errorf("action = %q, want %q", action, types.ActionReject)
	}
}

func TestClarifyNode_Answer(t *testing.T) {
	llm := newMockLLMClient(
		&model.Response{
			IsPartial: false,
			Choices: []model.Choice{{
				Message: model.Message{Content: `{"action": "answer"}`},
			}},
		},
	)

	clarifyFn := NewClarifyNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyQuery:        "Raft是什么",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := clarifyFn(context.Background(), state)
	if err != nil {
		t.Fatalf("clarifyFn() error = %v", err)
	}

	update, ok := result.(graph.State)
	if !ok {
		t.Fatalf("result is not graph.State, got %T", result)
	}

	action, _ := graph.GetStateValue[string](update, types.StateKeyAction)
	if action != types.ActionAnswer {
		t.Errorf("action = %q, want %q", action, types.ActionAnswer)
	}

	// Messages should be updated with the query and analysis.
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)
	if len(messages) < 2 {
		t.Errorf("expected at least 2 messages, got %d", len(messages))
	}
}

func TestClarifyNode_Research(t *testing.T) {
	llm := newMockLLMClient(
		&model.Response{
			IsPartial: false,
			Choices: []model.Choice{{
				Message: model.Message{Content: `{"action": "research"}`},
			}},
		},
	)

	clarifyFn := NewClarifyNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyQuery:        "Raft和Paxos的区别是什么",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := clarifyFn(context.Background(), state)
	if err != nil {
		t.Fatalf("clarifyFn() error = %v", err)
	}

	update, ok := result.(graph.State)
	if !ok {
		t.Fatalf("result is not graph.State, got %T", result)
	}

	action, _ := graph.GetStateValue[string](update, types.StateKeyAction)
	if action != types.ActionResearch {
		t.Errorf("action = %q, want %q", action, types.ActionResearch)
	}
}

func TestClarifyNode_StreamingDeltas(t *testing.T) {
	// Simulate streaming: multiple partial deltas + a final message.
	llm := newMockLLMClient(
		&model.Response{
			IsPartial: true,
			Choices: []model.Choice{{
				Delta: model.Message{Content: "Let me analyze"},
			}},
		},
		&model.Response{
			IsPartial: true,
			Choices: []model.Choice{{
				Delta: model.Message{Content: " this question..."},
			}},
		},
		&model.Response{
			IsPartial: false,
			Choices: []model.Choice{{
				Message: model.Message{Content: `{"action": "answer"}`},
			}},
		},
	)

	clarifyFn := NewClarifyNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyQuery:        "What is Raft?",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := clarifyFn(context.Background(), state)
	if err != nil {
		t.Fatalf("clarifyFn() error = %v", err)
	}

	update := result.(graph.State)
	action, _ := graph.GetStateValue[string](update, types.StateKeyAction)
	if action != types.ActionAnswer {
		t.Errorf("action = %q, want %q", action, types.ActionAnswer)
	}

	// The reasoning should include the streaming deltas.
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)
	if len(messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(messages))
	}
	msg := messages[len(messages)-1]
	if msg.Content == "" {
		t.Error("assistant message content should not be empty")
	}
}

func TestClarifyNode_MissingQuery(t *testing.T) {
	llm := newMockLLMClient()
	clarifyFn := NewClarifyNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyMessages: []model.Message{},
	}

	_, err := clarifyFn(context.Background(), state)
	if err == nil {
		t.Fatal("expected error for missing query, got nil")
	}
}

func TestClarifyNode_ContextCancellation(t *testing.T) {
	llm := newMockLLMClient()
	clarifyFn := NewClarifyNodeFunc(llm, types.DefaultPrompts(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	state := graph.State{
		types.StateKeyQuery:    "test",
		types.StateKeyMessages: []model.Message{},
	}

	_, err := clarifyFn(ctx, state)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// ─── Request building tests ───────────────────────────────────────────────

func TestBuildClarifyRequest_IncludesHistory(t *testing.T) {
	history := []model.Message{
		model.NewUserMessage("Raft是什么"),
		model.NewAssistantMessage("Raft 是一种共识算法。"),
	}
	req := buildClarifyRequest("system prompt", "它的选举过程呢？", history, types.DefaultConfig())

	if len(req.Messages) != 4 {
		t.Fatalf("len(req.Messages) = %d, want 4 (system + 2 history + query)", len(req.Messages))
	}
	if req.Messages[0].Role != model.RoleSystem {
		t.Errorf("messages[0].Role = %q, want system", req.Messages[0].Role)
	}
	if req.Messages[1].Content != "Raft是什么" || req.Messages[1].Role != model.RoleUser {
		t.Errorf("messages[1] = %+v, want history user", req.Messages[1])
	}
	if req.Messages[2].Content != "Raft 是一种共识算法。" || req.Messages[2].Role != model.RoleAssistant {
		t.Errorf("messages[2] = %+v, want history assistant", req.Messages[2])
	}
	if req.Messages[3].Content != "它的选举过程呢？" || req.Messages[3].Role != model.RoleUser {
		t.Errorf("messages[3] = %+v, want current query", req.Messages[3])
	}
}

func TestBuildClarifyRequest_NoHistory(t *testing.T) {
	req := buildClarifyRequest("system prompt", "Raft是什么", nil, types.DefaultConfig())

	if len(req.Messages) != 2 {
		t.Fatalf("len(req.Messages) = %d, want 2 (system + query)", len(req.Messages))
	}
	if req.Messages[0].Role != model.RoleSystem {
		t.Errorf("messages[0].Role = %q, want system", req.Messages[0].Role)
	}
	if req.Messages[1].Role != model.RoleUser || req.Messages[1].Content != "Raft是什么" {
		t.Errorf("messages[1] = %+v, want current query", req.Messages[1])
	}
}

// ─── Logprobs confidence tests ───────────────────────────────────────────

func TestComputeConfidence(t *testing.T) {
	tests := []struct {
		name     string
		logprobs *LogprobsInfo
		wantMin  float64
		wantMax  float64
	}{
		{
			name:     "nil returns 0",
			logprobs: nil,
			wantMax:  0,
		},
		{
			name:     "insufficient tokens",
			logprobs: &LogprobsInfo{TopLogprobs: []LogprobToken{{Token: "answer", Logprob: -0.5}}},
			wantMax:  0,
		},
		{
			name: "high confidence",
			logprobs: &LogprobsInfo{
				TopLogprobs: []LogprobToken{
					{Token: "answer", Logprob: -0.1},
					{Token: "research", Logprob: -3.0},
				},
			},
			wantMin: 0.9, // large margin → high confidence
		},
		{
			name: "low confidence",
			logprobs: &LogprobsInfo{
				TopLogprobs: []LogprobToken{
					{Token: "answer", Logprob: -0.5},
					{Token: "research", Logprob: -0.6},
				},
			},
			wantMax: 0.6, // small margin → low confidence
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeConfidence(tt.logprobs)
			if tt.wantMax > 0 && got > tt.wantMax {
				t.Errorf("ComputeConfidence() = %f, want <= %f", got, tt.wantMax)
			}
			if tt.wantMin > 0 && got < tt.wantMin {
				t.Errorf("ComputeConfidence() = %f, want >= %f", got, tt.wantMin)
			}
		})
	}
}

func TestApplyConfidenceThreshold(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		confidence float64
		want       string
	}{
		{"high confidence answer", types.ActionAnswer, 0.95, types.ActionAnswer},
		{"low confidence answer", types.ActionAnswer, 0.70, types.ActionResearch},
		{"exactly threshold answer", types.ActionAnswer, 0.85, types.ActionAnswer},
		{"zero confidence", types.ActionAnswer, 0, types.ActionAnswer}, // skip if no data
		{"reject unaffected", types.ActionReject, 0.5, types.ActionReject},
		{"research unaffected", types.ActionResearch, 0.5, types.ActionResearch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyConfidenceThreshold(tt.action, tt.confidence, 0.85)
			if got != tt.want {
				t.Errorf("ApplyConfidenceThreshold(%q, %f) = %q, want %q",
					tt.action, tt.confidence, got, tt.want)
			}
		})
	}
}
