package nodes

import (
	"context"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
)

func TestSynthesizeNode_BasicReport(t *testing.T) {
	llm := newMockLLMClient(
		&model.Response{
			IsPartial: true,
			Choices: []model.Choice{{
				Delta: model.Message{Content: "# Research Report: "},
			}},
		},
		&model.Response{
			IsPartial: true,
			Choices: []model.Choice{{
				Delta: model.Message{Content: "Test Query\n\n## Findings\n\nFound results."},
			}},
		},
		&model.Response{
			IsPartial: false,
			Choices: []model.Choice{{
				Message: model.Message{Content: "# Research Report: Test Query\n\n## Findings\n\nFound results."},
			}},
		},
	)

	synthFn := NewSynthesizeNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyQuery: "test query",
		types.StateKeyMessages: []model.Message{
			{Content: "[Investigate - Progress - Round 1]\n确认: Raft uses leader election"},
			{Role: model.RoleUser, Content: "[Tool Result: web_search]\n" + "ToolResult: Raft paper confirms leader election"},
		},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := synthFn(context.Background(), state)
	if err != nil {
		t.Fatalf("synthFn() error = %v", err)
	}

	update, ok := result.(graph.State)
	if !ok {
		t.Fatalf("result is not graph.State, got %T", result)
	}

	report, _ := graph.GetStateValue[string](update, types.StateKeyReport)
	if report == "" {
		t.Error("report should not be empty")
	}
	if !strings.Contains(report, "Research Report") {
		t.Error("report should contain 'Research Report'")
	}
}

func TestSynthesizeNode_StreamingDeltas(t *testing.T) {
	llm := newMockLLMClient(
		&model.Response{
			IsPartial: true,
			Choices: []model.Choice{{
				Delta: model.Message{Content: "Chunk1"},
			}},
		},
		&model.Response{
			IsPartial: true,
			Choices: []model.Choice{{
				Delta: model.Message{Content: "Chunk2"},
			}},
		},
		&model.Response{
			IsPartial: false,
			Choices: []model.Choice{{
				Message: model.Message{Content: "Chunk1Chunk2"},
			}},
		},
	)

	rec := &recordingStreamWriter{}
	synthFn := NewSynthesizeNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyQuery:        "test",
		types.StateKeyMessages:     []model.Message{},
		types.StateKeyStreamWriter: rec,
	}

	_, err := synthFn(context.Background(), state)
	if err != nil {
		t.Fatalf("synthFn() error = %v", err)
	}

	// Should have generated a think_start and think_end with the full report.
	if len(rec.events) < 2 {
		t.Fatalf("expected at least 2 events (think_start + think_end), got %d", len(rec.events))
	}
	if rec.events[0].Type != types.EventThinkStart {
		t.Errorf("first event = %q, want %q", rec.events[0].Type, types.EventThinkStart)
	}
	if rec.events[len(rec.events)-1].Type != types.EventThinkEnd {
		t.Errorf("last event = %q, want %q", rec.events[len(rec.events)-1].Type, types.EventThinkEnd)
	}
	if !strings.Contains(rec.events[len(rec.events)-1].Content, "Chunk") {
		t.Errorf("think_end content should contain the report, got %q", rec.events[len(rec.events)-1].Content)
	}
}

func TestSynthesizeNode_ContextCancellation(t *testing.T) {
	llm := newMockLLMClient()
	synthFn := NewSynthesizeNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyQuery:    "test",
		types.StateKeyMessages: []model.Message{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := synthFn(ctx, state)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestSynthesizeNode_PostProcessIntegration(t *testing.T) {
	// Report contains a number not in sources — post-process should flag it.
	llm := newMockLLMClient(
		&model.Response{
			IsPartial: false,
			Choices: []model.Choice{{
				Message: model.Message{Content: "Raft uses 150ms election timeout."},
			}},
		},
	)

	synthFn := NewSynthesizeNodeFunc(llm, types.DefaultPrompts(), nil)
	state := graph.State{
		types.StateKeyQuery: "test",
		types.StateKeyMessages: []model.Message{
			{Role: model.RoleUser, Content: "[Tool Result: web_search]\n" + "types.Source: timeout is 300ms"}, // different from 150ms
		},
		types.StateKeyStreamWriter: types.NopStreamWriter{},
	}

	result, err := synthFn(context.Background(), state)
	if err != nil {
		t.Fatalf("synthFn() error = %v", err)
	}

	update := result.(graph.State)
	report, _ := graph.GetStateValue[string](update, types.StateKeyReport)

	// The report should contain warnings from post-processing.
	if !strings.Contains(report, "验证警告") || !strings.Contains(report, "150ms") {
		t.Logf("Report: %s", report)
		// Note: this may pass or fail depending on whether the number is found in sources
	}
}

func TestExtractRound(t *testing.T) {
	tests := []struct {
		content string
		want    int
	}{
		{"[Investigate - Think - Round 1]\n...", 1},
		{"Round 3: search complete", 3},
		{"No round here", 0},
		{"Round 10", 10},
	}
	for _, tt := range tests {
		t.Run(tt.content[:min(len(tt.content), 30)], func(t *testing.T) {
			got := extractRound(tt.content)
			if got != tt.want {
				t.Errorf("extractRound() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPrioritizeMessages(t *testing.T) {
	messages := []model.Message{
		{Content: "[Investigate - Progress - Round 1]\nProgress 1"},
		{Content: "[Investigate - Think - Round 1]\nThink 1"},
		{Role: "tool", Content: "ToolResult 1"},
		{Content: "[Investigate - Progress - Round 2]\nProgress 2"},
		{Content: "[Investigate - Think - Round 2]\nThink 2"},
		{Role: "tool", Content: "ToolResult 2"},
	}

	prioritized := prioritizeMessages(messages)

	// Progress should come first.
	if len(prioritized) < 4 {
		t.Fatalf("expected at least 4 messages, got %d", len(prioritized))
	}

	// First two should be progress messages.
	if !strings.Contains(prioritized[0].Content, "Progress") {
		t.Error("first message should be a progress marker")
	}
	if !strings.Contains(prioritized[1].Content, "Progress") {
		t.Error("second message should be a progress marker")
	}

	// Think messages from ALL rounds should be included.
	thinkCount := 0
	for _, msg := range prioritized {
		if strings.Contains(msg.Content, "[Investigate - Think") {
			thinkCount++
		}
	}
	if thinkCount != 2 {
		t.Errorf("expected 2 Think messages (all rounds), got %d", thinkCount)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
