package nodes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/infra"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestInvestigateNode_SingleRoundStop(t *testing.T) {
	runner := &MockInvestigateRunner{
		Rounds: []MockRoundResult{
			{
				Thought: "The user asks about Raft. Let me search the knowledge base.",
				ToolCalls: []MockToolCall{
					{ToolName: "search_kb", Args: map[string]any{"query": "Raft consensus"}, Result: "Raft is a consensus algorithm..."},
				},
				Progress:   "已确认: Raft 是分布式共识算法，由 Ongaro 2014 年提出",
				ShouldStop: true,
			},
		},
	}

	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:        "Raft 是什么",
		types.StateKeyMessages:     []model.Message{model.NewUserMessage("Raft 是什么")},
		types.StateKeyMaxRounds:    5,
		types.StateKeyAllowedTools: []string{"search_kb", "web_search", "web_fetch"},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := nodeFn(context.Background(), state)
	if err != nil {
		t.Fatalf("nodeFn() error = %v", err)
	}

	update := result.(graph.State)
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)
	if len(messages) < 3 {
		t.Errorf("expected at least 3 messages (user + think + tool), got %d", len(messages))
	}

	// Verify progress marker is present.
	hasProgress := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[Investigate - Progress") {
			hasProgress = true
			break
		}
	}
	if !hasProgress {
		t.Error("messages should contain a progress marker")
	}
}

func TestInvestigateNode_MultiRound(t *testing.T) {
	runner := &MockInvestigateRunner{
		Rounds: []MockRoundResult{
			{
				Thought: "Round 1: Let me search.",
				ToolCalls: []MockToolCall{
					{ToolName: "web_search", Args: map[string]any{"query": "Raft vs Paxos"}, Result: "Found 5 results"},
				},
				Progress: "Round 1 progress",
			},
			{
				Thought: "Round 2: I need to verify some details.",
				ToolCalls: []MockToolCall{
					{ToolName: "search_kb", Args: map[string]any{"query": "Raft details"}, Result: "KB result"},
				},
				Progress:   "Round 2 progress: confirmed findings",
				ShouldStop: true,
			},
		},
	}

	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:        "Raft vs Paxos",
		types.StateKeyMessages:     []model.Message{model.NewUserMessage("Raft vs Paxos")},
		types.StateKeyMaxRounds:    5,
		types.StateKeyAllowedTools: []string{"search_kb", "web_search"},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := nodeFn(context.Background(), state)
	if err != nil {
		t.Fatalf("nodeFn() error = %v", err)
	}

	update := result.(graph.State)
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)

	// Should have 2 progress markers.
	progressCount := 0
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[Investigate - Progress") {
			progressCount++
		}
	}
	if progressCount != 2 {
		t.Errorf("expected 2 progress markers, got %d", progressCount)
	}
}

func TestInvestigateNode_FanOut(t *testing.T) {
	// Same round, multiple independent tool calls.
	runner := &MockInvestigateRunner{
		Rounds: []MockRoundResult{
			{
				Thought: "I need to search multiple sources in parallel.",
				ToolCalls: []MockToolCall{
					{ToolName: "search_kb", Args: map[string]any{"query": "Raft"}, Result: "KB: Raft result"},
					{ToolName: "web_search", Args: map[string]any{"query": "Raft"}, Result: "Web: Raft result"},
					{ToolName: "web_fetch", Args: map[string]any{"url": "https://example.com"}, Result: "Fetched content"},
				},
				Progress:   "Fan-out complete: 3 tools succeeded",
				ShouldStop: true,
			},
		},
	}

	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:        "test",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyMaxRounds:    3,
		types.StateKeyAllowedTools: []string{"search_kb", "web_search", "web_fetch"},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := nodeFn(context.Background(), state)
	if err != nil {
		t.Fatalf("nodeFn() error = %v", err)
	}

	update := result.(graph.State)
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)

	// Count tool messages.
	toolCount := 0
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[Investigate - ToolCall") {
			toolCount++
		}
	}
	if toolCount != 3 {
		t.Errorf("expected 3 tool calls (fan-out), got %d", toolCount)
	}
}

func TestInvestigateNode_PartialFailure(t *testing.T) {
	// 3 tools in parallel, 1 fails.
	runner := &MockInvestigateRunner{
		Rounds: []MockRoundResult{
			{
				Thought: "Searching multiple sources.",
				ToolCalls: []MockToolCall{
					{ToolName: "search_kb", Args: map[string]any{"query": "test"}, Result: "KB success"},
					{ToolName: "web_search", Args: map[string]any{"query": "test"}, Error: "503 Service Unavailable"},
					{ToolName: "web_fetch", Args: map[string]any{"url": "x"}, Result: "Fetch success"},
				},
				Progress:   "2 of 3 tools succeeded",
				ShouldStop: true,
			},
		},
	}

	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:        "test",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyMaxRounds:    3,
		types.StateKeyAllowedTools: []string{"search_kb", "web_search", "web_fetch"},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, _ := nodeFn(context.Background(), state)
	update := result.(graph.State)
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)

	// Should have error message for web_search.
	hasError := false
	hasSuccessKB := false
	hasSuccessFetch := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "ERROR:") {
			hasError = true
		}
		if strings.Contains(msg.Content, "KB success") {
			hasSuccessKB = true
		}
		if strings.Contains(msg.Content, "Fetch success") {
			hasSuccessFetch = true
		}
	}
	if !hasError {
		t.Error("expected error message for failed tool")
	}
	if !hasSuccessKB {
		t.Error("expected success result for search_kb")
	}
	if !hasSuccessFetch {
		t.Error("expected success result for web_fetch")
	}
}

func TestInvestigateNode_ConsecutiveFailureAbort(t *testing.T) {
	// 3 consecutive rounds with 100% failure → force stop.
	rounds := make([]MockRoundResult, 3)
	for i := range rounds {
		rounds[i] = MockRoundResult{
			Thought: "Trying to search...",
			ToolCalls: []MockToolCall{
				{ToolName: "web_search", Args: map[string]any{"query": "test"}, Error: "500 Internal Server Error"},
			},
			Progress: "All tools failed this round",
		}
	}

	runner := &MockInvestigateRunner{Rounds: rounds}
	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:        "test",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyMaxRounds:    10,
		types.StateKeyAllowedTools: []string{"web_search"},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, _ := nodeFn(context.Background(), state)
	update := result.(graph.State)
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)

	// Should have the force-stop system message.
	hasForceStop := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "连续 3 轮工具全部失败") {
			hasForceStop = true
			break
		}
	}
	if !hasForceStop {
		t.Error("expected force-stop message after 3 consecutive failure rounds")
	}
}

func TestInvestigateNode_MaxRounds(t *testing.T) {
	// Set MaxRounds to 2, provide 10 mock rounds → should stop after 2.
	rounds := make([]MockRoundResult, 10)
	for i := range rounds {
		rounds[i] = MockRoundResult{
			Thought: "Thought",
			ToolCalls: []MockToolCall{
				{ToolName: "search_kb", Args: map[string]any{"query": "test"}, Result: "result"},
			},
			Progress: "progress",
		}
	}

	runner := &MockInvestigateRunner{Rounds: rounds}
	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:        "test",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyMaxRounds:    2,
		types.StateKeyAllowedTools: []string{"search_kb"},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, _ := nodeFn(context.Background(), state)
	update := result.(graph.State)
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)

	// Count progress markers — should be at most 2 (one per round).
	progressCount := 0
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[Investigate - Progress") {
			progressCount++
		}
	}
	if progressCount != 2 {
		t.Errorf("expected exactly 2 rounds (maxRounds=2), got %d progress markers", progressCount)
	}

	hasMaxReached := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "达到最大轮数") {
			hasMaxReached = true
		}
	}
	if !hasMaxReached {
		t.Error("expected max rounds reached message")
	}
}

func TestInvestigateNode_ContextCancellation(t *testing.T) {
	// Slow rounds, context cancelled → should stop immediately.
	runner := &MockInvestigateRunner{
		SleepEachRound: 5 * time.Second, // would block, but ctx cancelled
		Rounds: []MockRoundResult{
			{
				Thought: "Round 1",
				ToolCalls: []MockToolCall{
					{ToolName: "search_kb", Args: map[string]any{"query": "test"}, Result: "ok"},
				},
			},
		},
	}

	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:     "test",
		types.StateKeyMessages:  []model.Message{},
		types.StateKeyMaxRounds: 5,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := nodeFn(ctx, state)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestInvestigateNode_RunnerError(t *testing.T) {
	runner := &MockInvestigateRunner{
		Error: types.ErrToolsAllFailed,
	}

	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:     "test",
		types.StateKeyMessages:  []model.Message{},
		types.StateKeyMaxRounds: 5,
	}

	result, err := nodeFn(context.Background(), state)
	if err != nil {
		t.Fatalf("nodeFn() should not return error on runner failure: %v", err)
	}

	// Should gracefully degrade — append system message and return.
	update := result.(graph.State)
	messages, _ := graph.GetStateValue[[]model.Message](update, types.StateKeyMessages)
	hasSysMsg := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[System]") {
			hasSysMsg = true
		}
	}
	if !hasSysMsg {
		t.Error("expected system error message on runner failure")
	}
}

func TestInvestigateNode_ProgressEvents(t *testing.T) {
	// Capture progress events via a recording stream writer.
	rec := &recordingStreamWriter{}
	runner := &MockInvestigateRunner{
		Rounds: []MockRoundResult{
			{
				Thought: "Thought 1",
				ToolCalls: []MockToolCall{
					{ToolName: "search_kb", Args: map[string]any{"query": "test"}, Result: "ok"},
				},
				Progress:   "Progress 1",
				ShouldStop: true,
			},
		},
	}

	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:        "test",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyMaxRounds:    5,
		types.StateKeyAllowedTools: []string{"search_kb"},
		types.StateKeyStreamWriter: rec,
	}

	_, _ = nodeFn(context.Background(), state)

	if len(rec.events) == 0 {
		t.Error("expected progress events to be pushed")
	}

	// Check that we got tool_start and tool_end.
	hasStart := false
	hasEnd := false
	hasProgress := false
	for _, evt := range rec.events {
		if evt.Type == types.EventToolStart {
			hasStart = true
		}
		if evt.Type == types.EventToolEnd {
			hasEnd = true
		}
		if evt.Type == types.EventProgress {
			hasProgress = true
		}
	}
	if !hasStart {
		t.Error("expected tool_start event")
	}
	if !hasEnd {
		t.Error("expected tool_end event")
	}
	if !hasProgress {
		t.Error("expected progress event")
	}
}

func TestInvestigateNode_MissingRequiredState(t *testing.T) {
	runner := &MockInvestigateRunner{}
	nodeFn := NewInvestigateNodeFunc(runner)

	// Missing query.
	state := graph.State{
		types.StateKeyMessages: []model.Message{},
	}
	_, err := nodeFn(context.Background(), state)
	if err == nil {
		t.Error("expected error for missing query")
	}
}

// recordingStreamWriter captures events for assertion.
type recordingStreamWriter struct {
	events []types.StreamEvent
}

func (r *recordingStreamWriter) Write(event types.StreamEvent) error {
	r.events = append(r.events, event)
	return nil
}

var _ types.StreamWriter = (*recordingStreamWriter)(nil)

// ─── extractFindings tests ──────────────────────────────────────────────

func TestExtractFindings_EmptyMessages(t *testing.T) {
	findings := extractFindings(nil)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for nil, got %d", len(findings))
	}
	findings = extractFindings([]model.Message{})
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for empty, got %d", len(findings))
	}
}

func TestExtractFindings_WebSearchResults(t *testing.T) {
	output := infra.WebSearchOutput{
		Results: []infra.WebSearchResult{
			{Title: "Test Title 1", Snippet: "Test snippet 1", URL: "https://example.com/1"},
			{Title: "Test Title 2", Snippet: "Test snippet 2", URL: "https://example.com/2"},
		},
	}
	outputJSON, _ := json.Marshal(output)
	_ = outputJSON // suppress unused warning — used below as string

	messages := []model.Message{
		model.NewUserMessage("test query"),
		model.NewUserMessage("[Tool Result: web_search]\n" + string(outputJSON)),
		model.NewAssistantMessage("[Investigate - Think - Round 1]\nLet me analyze..."),
	}

	findings := extractFindings(messages)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	if findings[0].Claim != "Test snippet 1" {
		t.Errorf("expected claim 'Test snippet 1', got %q", findings[0].Claim)
	}
	if findings[0].Evidence[0].Title != "Test Title 1" {
		t.Errorf("expected source title 'Test Title 1', got %q", findings[0].Evidence[0].Title)
	}
	if findings[0].Evidence[0].URL != "https://example.com/1" {
		t.Errorf("expected source URL, got %q", findings[0].Evidence[0].URL)
	}
	if findings[0].Confidence != "medium" {
		t.Errorf("expected confidence 'medium', got %q", findings[0].Confidence)
	}
}

func TestExtractFindings_WebFetchResults(t *testing.T) {
	output := infra.WebFetchOutput{
		URL:     "https://example.com/article",
		Title:   "Article Title",
		Content: "This is the full article content extracted from the page.",
	}
	outputJSON, _ := json.Marshal(output)

	messages := []model.Message{
		model.NewUserMessage("[Tool Result: web_fetch]\n" + string(outputJSON)),
	}

	findings := extractFindings(messages)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Evidence[0].Title != "Article Title" {
		t.Errorf("expected 'Article Title', got %q", findings[0].Evidence[0].Title)
	}
	if findings[0].Evidence[0].URL != "https://example.com/article" {
		t.Errorf("expected URL, got %q", findings[0].Evidence[0].URL)
	}
}

func TestExtractFindings_MalformedJSON(t *testing.T) {
	messages := []model.Message{
		model.NewUserMessage("[Tool Result: web_search]\nnot valid json {{{"),
		model.NewUserMessage("[Tool Result: web_fetch]\nalso not json"),
	}

	findings := extractFindings(messages)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for malformed JSON, got %d", len(findings))
	}
}

func TestExtractFindings_SkipsEmptyResults(t *testing.T) {
	output := infra.WebSearchOutput{
		Results: []infra.WebSearchResult{
			{Title: "", Snippet: "", URL: ""}, // empty — should be skipped
			{Title: "Valid", Snippet: "Valid snippet", URL: "https://example.com"},
		},
	}
	outputJSON, _ := json.Marshal(output)

	messages := []model.Message{
		model.NewUserMessage("[Tool Result: web_search]\n" + string(outputJSON)),
	}

	findings := extractFindings(messages)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (empty skipped), got %d", len(findings))
	}
	if findings[0].Claim != "Valid snippet" {
		t.Errorf("expected 'Valid snippet', got %q", findings[0].Claim)
	}
}

func TestExtractFindings_IgnoresNonToolMessages(t *testing.T) {
	messages := []model.Message{
		model.NewUserMessage("search for Raft"),
		model.NewAssistantMessage("[Investigate - Think - Round 1]\nRaft is a consensus algorithm."),
		model.NewSystemMessage("System message"),
	}

	findings := extractFindings(messages)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for non-tool messages, got %d", len(findings))
	}
}

func TestExtractFindings_NodeFuncWritesFindingsToState(t *testing.T) {
	output := infra.WebSearchOutput{
		Results: []infra.WebSearchResult{
			{Title: "types.Finding from search", Snippet: "Raft uses leader-based replication", URL: "https://raft.github.io"},
		},
	}
	outputJSON, _ := json.Marshal(output)

	runner := &MockInvestigateRunner{
		Rounds: []MockRoundResult{
			{
				Thought: "Searching for Raft...",
				ToolCalls: []MockToolCall{
					{ToolName: "web_search", Args: map[string]any{"query": "Raft"}, Result: string(outputJSON)},
				},
				Progress:   "Found Raft overview",
				ShouldStop: true,
			},
		},
	}

	nodeFn := NewInvestigateNodeFunc(runner)
	state := graph.State{
		types.StateKeyQuery:        "Raft 是什么",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyMaxRounds:    3,
		types.StateKeyAllowedTools: []string{"web_search"},
	}

	result, err := nodeFn(context.Background(), state)
	if err != nil {
		t.Fatalf("nodeFn() error: %v", err)
	}

	update := result.(graph.State)
	findings, ok := graph.GetStateValue[[]types.Finding](update, types.StateKeyFindings)
	if !ok {
		t.Fatal("types.StateKeyFindings not found in state")
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding in state, got %d", len(findings))
	}
	if findings[0].Claim != "Raft uses leader-based replication" {
		t.Errorf("expected claim, got %q", findings[0].Claim)
	}
}

func TestFormatFindingsForPrompt_Empty(t *testing.T) {
	result := formatFindingsForPrompt(nil)
	if result != "" {
		t.Errorf("expected empty string for nil, got %q", result)
	}
	result = formatFindingsForPrompt([]types.Finding{})
	if result != "" {
		t.Errorf("expected empty string for empty slice, got %q", result)
	}
}

func TestFormatFindingsForPrompt_WithFindings(t *testing.T) {
	findings := []types.Finding{
		{
			Claim:      "Raft is a consensus algorithm",
			Evidence:   []types.Source{{Title: "Raft Paper", URL: "https://raft.github.io/raft.pdf"}},
			Confidence: "high",
		},
	}

	result := formatFindingsForPrompt(findings)
	if !strings.Contains(result, "Raft is a consensus algorithm") {
		t.Error("expected claim in formatted output")
	}
	if !strings.Contains(result, "Raft Paper") {
		t.Error("expected source title in formatted output")
	}
	if !strings.Contains(result, "https://raft.github.io/raft.pdf") {
		t.Error("expected source URL in formatted output")
	}
	if !strings.Contains(result, "high") {
		t.Error("expected confidence in formatted output")
	}
}
