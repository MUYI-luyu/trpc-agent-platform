package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/infra"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// ─── Investigate Runner interface ────────────────────────────────────────

// InvestigateRunner executes the ReAct loop for the Investigate node.
//
// Implementations:
//   - mockInvestigateRunner — for testing (returns pre-configured rounds)
//   - realInvestigateRunner — wraps a tRPC Runner (Phase 6)
type InvestigateRunner interface {
	// Run executes the research loop. It receives the accumulated Messages
	// and returns the updated Messages after all rounds complete.
	// The stopCh callback reports progress events to the types.StreamWriter.
	Run(ctx context.Context, state InvestigateState) ([]model.Message, error)
}

// InvestigateState holds the input state for the Investigate Runner.
type InvestigateState struct {
	Query        string                                     // original user question
	Messages     []model.Message                            // conversation history so far
	MaxRounds    int                                        // max research rounds
	AllowedTools []string                                   // permitted tool names
	ReportRound  func(round int, eventType, content string) // progress callback
}

// ─── Investigate NodeFunc ────────────────────────────────────────────────

// NewInvestigateNodeFunc creates a Graph NodeFunc for the Investigate node.
//
// It extracts state from the Graph context, runs the ReAct loop via the
// provided runner, and writes results back to State.Messages.
//
// Stop conditions (enforced by the node, not the LLM):
//   - ctx.Done() — client disconnect, timeout, or manual cancellation
//   - MaxRounds reached
//   - Runner reports natural stop (LLM no longer requests tool calls)
//   - 3 consecutive rounds with 100% tool failure
func NewInvestigateNodeFunc(runner InvestigateRunner) graph.NodeFunc {
	return func(ctx context.Context, state graph.State) (any, error) {
		// Check context before any work.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("investigate: context cancelled: %w", err)
		}

		// Extract state.
		query, ok := graph.GetStateValue[string](state, types.StateKeyQuery)
		if !ok || query == "" {
			return nil, fmt.Errorf("investigate: missing query in state")
		}

		messages, _ := graph.GetStateValue[[]model.Message](state, types.StateKeyMessages)
		maxRounds, _ := graph.GetStateValue[int](state, types.StateKeyMaxRounds)
		if maxRounds <= 0 {
			maxRounds = types.DefaultMaxRounds
		}
		allowedTools, _ := graph.GetStateValue[[]string](state, types.StateKeyAllowedTools)

		// Build progress callback wired to types.StreamWriter.
		sw, _ := graph.GetStateValue[types.StreamWriter](state, types.StateKeyStreamWriter)
		reportRound := func(round int, eventType, content string) {
			if sw != nil {
				evt := types.StreamEvent{
					Type:      eventType,
					Node:      "investigate",
					Timestamp: time.Now().UnixMilli(),
					Round:     round,
					Content:   content,
				}
				_ = sw.Write(evt)
			}
		}

		// Build investigate state.
		invState := InvestigateState{
			Query:        query,
			Messages:     append([]model.Message(nil), messages...),
			MaxRounds:    maxRounds,
			AllowedTools: allowedTools,
			ReportRound:  reportRound,
		}

		// Run the investigation loop.
		updatedMessages, err := runner.Run(ctx, invState)
		if err != nil {
			// Log the error and append a system message. Use the partial
			// messages (which include all tool results collected before the
			// error) rather than the pre-Run messages so Synthesize still
			// has research data to work with.
			log.Printf("[investigate] NodeFunc: runner.Run error: %v (partial messages=%d)",
				err, len(updatedMessages))
			updatedMessages = append(updatedMessages, model.NewSystemMessage(
				fmt.Sprintf("[System] 研究在第 %d 轮因错误而中断: %v", 0, err),
			))
			// Fall through — still extract findings from whatever we collected.
		} else {
			log.Printf("[investigate] NodeFunc: runner.Run returned %d messages, extracting findings...", len(updatedMessages))
		}

		// Extract structured findings from tool results. This runs on BOTH
		// success and error paths, so Synthesize always has findings when
		// tool results were collected.
		findings := extractFindings(updatedMessages)
		log.Printf("[investigate] NodeFunc: extractFindings returned %d findings, writing to state", len(findings))

		return graph.State{
			types.StateKeyMessages: updatedMessages,
			types.StateKeyFindings: findings,
		}, nil
	}
}

// ─── Mock Investigate Runner ─────────────────────────────────────────────

// MockInvestigateRunner implements InvestigateRunner with pre-programmed
// round results for testing.
type MockInvestigateRunner struct {
	// Rounds is a sequence of simulated round results.
	Rounds []MockRoundResult
	// Error is returned immediately if set (before any rounds run).
	Error error
	// SleepEachRound simulates processing time per round.
	SleepEachRound time.Duration
}

// MockRoundResult simulates the output of one ReAct round.
type MockRoundResult struct {
	// Thought is the LLM's reasoning for this round.
	Thought string
	// ToolCalls are the tool invocations in this round (nil = no more tools).
	ToolCalls []MockToolCall
	// Progress is the progress marker content.
	Progress string
	// ShouldStop indicates the LLM decided to stop after this round.
	ShouldStop bool
}

// MockToolCall simulates a single tool invocation in a round.
type MockToolCall struct {
	ToolName string
	Args     map[string]any
	Result   string
	Error    string // if non-empty, the tool call failed
}

// Run executes the mock investigation loop.
func (m *MockInvestigateRunner) Run(ctx context.Context, state InvestigateState) ([]model.Message, error) {
	if m.Error != nil {
		return nil, m.Error
	}

	messages := append([]model.Message(nil), state.Messages...)
	consecutiveFailures := 0

	for i, round := range m.Rounds {
		// Stop conditions.
		if i >= state.MaxRounds {
			messages = append(messages, model.NewSystemMessage(
				fmt.Sprintf("[System] 研究达到最大轮数 %d，强制停止", state.MaxRounds),
			))
			break
		}

		select {
		case <-ctx.Done():
			return messages, ctx.Err()
		default:
		}

		// Simulate think time.
		if m.SleepEachRound > 0 {
			select {
			case <-time.After(m.SleepEachRound):
			case <-ctx.Done():
				return messages, ctx.Err()
			}
		}

		roundNum := i + 1

		// Report think start.
		if state.ReportRound != nil {
			state.ReportRound(roundNum, types.EventThinkStart,
				fmt.Sprintf("第 %d 轮搜索：正在分析...", roundNum))
		}

		// Append the LLM's thought.
		if round.Thought != "" {
			messages = append(messages, model.NewAssistantMessage(
				fmt.Sprintf("[Investigate - Think - Round %d]\n%s", roundNum, round.Thought),
			))
		}

		// Track failures for this round.
		roundFailures := 0
		roundTotal := len(round.ToolCalls)

		// Execute tool calls.
		for _, tc := range round.ToolCalls {
			if state.ReportRound != nil {
				state.ReportRound(roundNum, types.EventToolStart,
					fmt.Sprintf("正在调用 %s: %v", tc.ToolName, tc.Args))
			}

			// Append tool call to messages.
			argsJSON, _ := json.Marshal(tc.Args)
			messages = append(messages, model.NewAssistantMessage(
				fmt.Sprintf("[Investigate - ToolCall - Round %d]\nTool: %s\nArgs: %s",
					roundNum, tc.ToolName, string(argsJSON)),
			))

			if tc.Error != "" {
				roundFailures++
				messages = append(messages, model.NewUserMessage(
					fmt.Sprintf("[Tool Result: %s]\nERROR: %s", tc.ToolName, tc.Error),
				))
				if state.ReportRound != nil {
					state.ReportRound(roundNum, types.EventToolError,
						fmt.Sprintf("%s 失败: %s", tc.ToolName, tc.Error))
				}
			} else {
				// Store result without re-truncating the serialized JSON.
				messages = append(messages, model.NewUserMessage(
					fmt.Sprintf("[Tool Result: %s]\n%s", tc.ToolName, tc.Result),
				))
				if state.ReportRound != nil {
					state.ReportRound(roundNum, types.EventToolEnd,
						fmt.Sprintf("%s 完成", tc.ToolName))
				}
			}
		}

		// Check consecutive failures.
		if roundTotal > 0 && roundFailures == roundTotal {
			consecutiveFailures++
		} else {
			consecutiveFailures = 0
		}

		if consecutiveFailures >= 3 {
			messages = append(messages, model.NewSystemMessage(
				"[System] 连续 3 轮工具全部失败，研究中断",
			))
			if state.ReportRound != nil {
				state.ReportRound(roundNum, types.EventError,
					"工具连续不可用，研究中断，正在生成部分结果")
			}
			break
		}

		// Append progress marker.
		if round.Progress != "" {
			messages = append(messages, model.NewAssistantMessage(
				fmt.Sprintf("[Investigate - Progress - Round %d]\n%s", roundNum, round.Progress),
			))
			if state.ReportRound != nil {
				state.ReportRound(roundNum, types.EventProgress, round.Progress)
			}
		}

		// Check if the LLM wants to stop.
		if round.ShouldStop {
			if state.ReportRound != nil {
				state.ReportRound(roundNum, types.EventNodeComplete,
					fmt.Sprintf("研究完成，共 %d 轮搜索", roundNum))
			}
			break
		}
	}

	return messages, nil
}

// compile-time interface check
var _ InvestigateRunner = (*MockInvestigateRunner)(nil)
var _ InvestigateRunner = (*SimpleLLMInvestigateRunner)(nil)
var _ InvestigateRunner = (*RealInvestigateRunner)(nil)

// ─── Simple LLM-based Runner (demo fallback) ───────────────────────────────

// SimpleLLMInvestigateRunner does a single LLM call per round without real
// tools. It's used when no full InvestigateRunner is configured but a Model is
// available — this lets the research path work end-to-end without tool backends.
type SimpleLLMInvestigateRunner struct {
	llm     LLMClient
	prompts *types.PromptSet
}

// NewSimpleLLMInvestigateRunner creates a demo runner backed by the LLM.
func NewSimpleLLMInvestigateRunner(llm LLMClient, prompts *types.PromptSet) *SimpleLLMInvestigateRunner {
	return &SimpleLLMInvestigateRunner{llm: llm, prompts: prompts}
}

// Run executes a single LLM call to research the query, aggregates the result,
// sends a clean progress event, and returns the augmented messages.
func (r *SimpleLLMInvestigateRunner) Run(ctx context.Context, state InvestigateState) ([]model.Message, error) {
	messages := append([]model.Message(nil), state.Messages...)
	round := 1

	if state.ReportRound != nil {
		state.ReportRound(round, types.EventToolStart, "开始研究...")
	}

	// Use a no-tools prompt since we're doing LLM-only research.
	sysPrompt := types.PromptInvestigateSimple

	req := &model.Request{
		Messages: append(
			[]model.Message{model.NewSystemMessage(sysPrompt)},
			messages...,
		),
		GenerationConfig: model.GenerationConfig{Stream: true},
	}

	ctx, span := telemetry.SpanLLM(ctx, r.llm.Info().Name)
	defer span.End()

	eventCh, err := r.llm.GenerateContent(ctx, req)
	if err != nil {
		telemetry.RecordError(ctx, err)
		return messages, fmt.Errorf("investigate: LLM call failed: %w", err)
	}

	// Aggregate all content first.
	var content strings.Builder
	var lastUsage *model.Usage
	hasDeltas := false
	for rsp := range eventCh {
		if rsp.Usage != nil {
			lastUsage = rsp.Usage
		}
		if rsp.Error != nil {
			err := fmt.Errorf("investigate API error: %s: %s", rsp.Error.Type, rsp.Error.Message)
			telemetry.RecordError(ctx, err)
			return messages, err
		}
		if len(rsp.Choices) == 0 {
			continue
		}
		if rsp.IsPartial {
			delta := rsp.Choices[0].Delta
			if delta.Content != "" {
				content.WriteString(delta.Content)
				hasDeltas = true
			}
		} else {
			msg := rsp.Choices[0].Message
			if msg.Content != "" && !hasDeltas {
				content.WriteString(msg.Content)
			}
		}
	}

	if lastUsage != nil {
		telemetry.SetTokenUsage(ctx, lastUsage.PromptTokens, lastUsage.CompletionTokens)
	}

	fullContent := content.String()
	if fullContent == "" {
		fullContent = "(Investigation completed — no additional findings beyond initial analysis.)"
	}

	// Send the full research findings as a clean progress event.
	if state.ReportRound != nil {
		state.ReportRound(round, types.EventToolEnd, fullContent)
		state.ReportRound(round, types.EventNodeComplete, fmt.Sprintf("研究完成，共 %d 轮", round))
	}

	// Append investigation result to messages for the Synthesize node.
	messages = append(messages, model.NewAssistantMessage(
		fmt.Sprintf("[Investigate - Round %d]\n%s", round, fullContent),
	))

	return messages, nil
}

// ─── Real Investigate Runner (ReAct loop with tools) ────────────────────

// RealInvestigateRunner executes a ReAct loop: LLM → tool calls → results →
// next round. It does NOT use an inner runner.NewRunner (see plan risk #2).
// Instead it directly calls model.GenerateContent() and tool.Call().
type RealInvestigateRunner struct {
	llm     LLMClient
	tools   map[string]tool.Tool
	prompts *types.PromptSet
	config  *types.Config
}

// NewRealInvestigateRunner creates a ReAct runner with the given model and tools.
func NewRealInvestigateRunner(llm LLMClient, tools map[string]tool.Tool, prompts *types.PromptSet, config *types.Config) *RealInvestigateRunner {
	return &RealInvestigateRunner{llm: llm, tools: tools, prompts: prompts, config: config}
}

// callResult holds the aggregated result of a single LLM call.
type callResult struct {
	content   string
	toolCalls []model.ToolCall
}

// Run executes the ReAct loop.
func (r *RealInvestigateRunner) Run(ctx context.Context, state InvestigateState) ([]model.Message, error) {
	messages := append([]model.Message(nil), state.Messages...)
	consecutiveFailures := 0

	maxRounds := state.MaxRounds
	if maxRounds <= 0 {
		maxRounds = types.DefaultMaxRounds
	}

	// Resolve the tool set for this request. An explicit allowlist (even an
	// empty one) filters the pool; a nil allowlist means "no policy — use
	// all tools".
	tools := r.tools
	if state.AllowedTools != nil {
		tools = filterTools(r.tools, state.AllowedTools)
	}

	for round := 1; round <= maxRounds; round++ {
		select {
		case <-ctx.Done():
			return messages, ctx.Err()
		default:
		}

		// 1. Report round start.
		if state.ReportRound != nil {
			state.ReportRound(round, types.EventToolStart,
				fmt.Sprintf("第 %d 轮搜索...", round))
		}

		// 2. Build request with system prompt + tools + messages.
		sysPrompt := r.buildSystemPrompt(state, round, maxRounds, tools)
		req := &model.Request{
			Messages: append(
				[]model.Message{model.NewSystemMessage(sysPrompt)},
				messages...,
			),
			Tools:            tools,
			GenerationConfig: model.GenerationConfig{Stream: true},
		}

		// 3. Call LLM (aggregate streaming deltas).
		result, err := r.callLLM(ctx, req)
		if err != nil {
			// Round 1 LLM failure with no data yet — fatal.
			if round == 1 && len(messages) <= len(state.Messages) {
				return messages, fmt.Errorf("round %d LLM: %w", round, err)
			}
			// Later round LLM failure — log and break gracefully.
			// We already have tool results from previous rounds.
			log.Printf("[investigate] round %d LLM error (non-fatal, have %d messages): %v",
				round, len(messages), err)
			messages = append(messages, model.NewSystemMessage(
				fmt.Sprintf("[System] 第 %d 轮 LLM 调用失败: %v，使用已收集的研究数据继续", round, err),
			))
			break
		}

		// 4. Append LLM think/analysis.
		if result.content != "" {
			messages = append(messages, model.NewAssistantMessage(
				fmt.Sprintf("[Investigate - Think - Round %d]\n%s", round, result.content),
			))
		}

		// 5. No tool calls → LLM finished.
		if len(result.toolCalls) == 0 {
			content := result.content
			if content == "" {
				// LLM returned empty — synthesize a fallback summary from
				// tool results collected in previous rounds so Synthesize
				// has something to work with.
				content = r.synthesizeFallbackContent(messages, round)
			}
			messages = append(messages, model.NewAssistantMessage(
				fmt.Sprintf("[Investigate - Round %d]\n%s", round, content),
			))
			if state.ReportRound != nil {
				state.ReportRound(round, types.EventNodeComplete,
					fmt.Sprintf("研究完成，共 %d 轮", round))
				if content != "" {
					state.ReportRound(round, types.EventToolEnd, content)
				}
			}
			break
		}

		// 6. Execute tool calls sequentially.
		roundFailures := 0
		for _, tc := range result.toolCalls {
			if state.ReportRound != nil {
				state.ReportRound(round, types.EventToolStart,
					fmt.Sprintf("调用 %s...", tc.Function.Name))
			}

			toolResult, toolErr := r.executeTool(ctx, tools, tc)

			// Append tool call + result to messages.
			argsJSON, _ := json.Marshal(tc.Function.Arguments)
			messages = append(messages, model.NewAssistantMessage(
				fmt.Sprintf("[Investigate - ToolCall - Round %d]\nTool: %s\nArgs: %s",
					round, tc.Function.Name, string(argsJSON)),
			))

			if toolErr != nil {
				roundFailures++
				messages = append(messages, model.NewUserMessage(
					fmt.Sprintf("[Tool Result: %s]\nERROR: %v", tc.Function.Name, toolErr),
				))
				if state.ReportRound != nil {
					state.ReportRound(round, types.EventToolError,
						fmt.Sprintf("%s 失败: %v", tc.Function.Name, toolErr))
				}
			} else {
				// Store the raw JSON result WITHOUT re-truncating — the tool
				// wrapper already truncates individual fields (snippets).
				// Re-truncating the serialized JSON breaks the JSON structure.
				messages = append(messages, model.NewUserMessage(
					fmt.Sprintf("[Tool Result: %s]\n%s", tc.Function.Name, toolResult),
				))
				if state.ReportRound != nil {
					state.ReportRound(round, types.EventToolEnd,
						fmt.Sprintf("%s 完成", tc.Function.Name))
				}
			}
		}

		// 7. Consecutive failure check + fallback.
		if roundFailures > 0 {
			consecutiveFailures++
			if consecutiveFailures >= 3 {
				messages = append(messages, model.NewSystemMessage(
					"[System] 连续 3 轮工具失败，研究中断"))
				if state.ReportRound != nil {
					state.ReportRound(round, types.EventError,
						"工具连续不可用，研究中断")
				}
				break
			}
			// Re-prompt LLM WITHOUT tools this round so it can use its
			// own knowledge rather than retrying broken tools.
			messages = append(messages, model.NewSystemMessage(
				"[System] 工具不可用，请根据你的训练数据和已有分析直接回答研究问题"))
			if state.ReportRound != nil {
				state.ReportRound(round, types.EventProgress,
					"工具失败，使用模型知识回答")
			}
			result2, err2 := r.callLLMWithoutTools(ctx, messages, state.Query)
			if err2 != nil {
				return messages, fmt.Errorf("fallback LLM: %w", err2)
			}
			// Strip any hallucinated tool calls from the model's output.
			// Without tools configured, the model may still mimic the
			// [Investigate - ToolCall] pattern from prior messages.
			cleanContent := stripToolCallHallucination(result2.content)
			messages = append(messages, model.NewAssistantMessage(
				fmt.Sprintf("[Investigate - Round %d]\n%s", round+1, cleanContent),
			))
			if state.ReportRound != nil && cleanContent != "" {
				state.ReportRound(round+1, types.EventToolEnd, cleanContent)
				state.ReportRound(round+1, types.EventNodeComplete,
					fmt.Sprintf("研究完成（模型知识模式）"))
			}
			break
		}
		consecutiveFailures = 0
	}

	// Post-loop: findings are extracted from tool results by the NodeFunc
	// using extractFindings() — a pure-code parser that does not depend on
	// LLM availability. This keeps the ReAct loop self-contained and lets
	// the NodeFunc manage state writes.
	if state.ReportRound != nil {
		state.ReportRound(maxRounds, types.EventProgress, "研究完成，正在整理发现...")
	}

	return messages, nil
}

// callLLMWithoutTools calls the model without any tools, forcing it to use
// its own knowledge. Used as fallback when tool backends are unavailable.
func (r *RealInvestigateRunner) callLLMWithoutTools(ctx context.Context, messages []model.Message, query string) (*callResult, error) {
	sysPrompt := types.PromptInvestigateSimple
	req := &model.Request{
		Messages: append(
			[]model.Message{model.NewSystemMessage(sysPrompt)},
			messages...,
		),
		GenerationConfig: model.GenerationConfig{Stream: true},
	}
	return r.callLLM(ctx, req)
}

// callLLM calls the model and aggregates the streaming response.
func (r *RealInvestigateRunner) callLLM(ctx context.Context, req *model.Request) (*callResult, error) {
	ctx, span := telemetry.SpanLLM(ctx, r.llm.Info().Name)
	defer span.End()

	eventCh, err := r.llm.GenerateContent(ctx, req)
	if err != nil {
		telemetry.RecordError(ctx, err)
		return nil, fmt.Errorf("generate: %w", err)
	}

	result := &callResult{}
	var content strings.Builder
	var lastUsage *model.Usage
	hasDeltas := false

	for evt := range eventCh {
		if evt.Usage != nil {
			lastUsage = evt.Usage
		}
		if evt.Error != nil {
			err := fmt.Errorf("API: %s: %s", evt.Error.Type, evt.Error.Message)
			telemetry.RecordError(ctx, err)
			return result, err
		}
		if len(evt.Choices) == 0 {
			continue
		}

		if evt.IsPartial {
			delta := evt.Choices[0].Delta
			if delta.Content != "" {
				content.WriteString(delta.Content)
				hasDeltas = true
			}
		} else {
			msg := evt.Choices[0].Message
			if msg.Content != "" && !hasDeltas {
				content.WriteString(msg.Content)
			}
			result.toolCalls = msg.ToolCalls
		}
	}

	if lastUsage != nil {
		telemetry.SetTokenUsage(ctx, lastUsage.PromptTokens, lastUsage.CompletionTokens)
	}

	result.content = content.String()
	return result, nil
}

// executeTool looks up a tool by name in the (already filtered) tool set, calls
// it, and returns its result as JSON. Using the filtered set (rather than
// r.tools) is the defense-in-depth layer: even if the model hallucinates a tool
// name that was filtered out of the request, it cannot be invoked here.
func (r *RealInvestigateRunner) executeTool(ctx context.Context, tools map[string]tool.Tool, tc model.ToolCall) (string, error) {
	ctx, span := telemetry.SpanTool(ctx, tc.Function.Name)
	defer span.End()

	t, ok := tools[tc.Function.Name]
	if !ok {
		err := fmt.Errorf("tool not allowed or unknown: %s", tc.Function.Name)
		telemetry.RecordError(ctx, err)
		return "", err
	}

	callable, ok := t.(tool.CallableTool)
	if !ok {
		err := fmt.Errorf("tool %s is not callable", tc.Function.Name)
		telemetry.RecordError(ctx, err)
		return "", err
	}

	args := tc.Function.Arguments
	if len(args) == 0 {
		args = []byte("{}")
	}

	result, err := callable.Call(ctx, args)
	if err != nil {
		telemetry.RecordError(ctx, err)
		return "", err
	}

	resultBytes, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("%v", result), nil
	}
	raw := string(resultBytes)
	log.Printf("[investigate] executeTool name=%q raw_result(first 500 chars)=%q",
		tc.Function.Name, types.TruncateForLog(raw, 500))
	return raw, nil
}

// filterTools returns the subset of all whose names are in the allowed set.
// It is the enforcement point for per-tenant tool allow/blocklists: both the
// tools sent to the LLM and the tools looked up in executeTool come from this
// filtered map, so a disallowed tool can neither be offered nor invoked.
func filterTools(all map[string]tool.Tool, allowed []string) map[string]tool.Tool {
	out := make(map[string]tool.Tool, len(allowed))
	set := make(map[string]bool, len(allowed))
	for _, n := range allowed {
		set[n] = true
	}
	for name, t := range all {
		if set[name] {
			out[name] = t
		}
	}
	return out
}

// synthesizeFallbackContent extracts key findings from tool results collected
// in previous rounds. Used when the LLM returns empty content (no tool calls
// and no analysis), so Synthesize still has context to work with.
func (r *RealInvestigateRunner) synthesizeFallbackContent(messages []model.Message, round int) string {
	var parts []string
	for _, msg := range messages {
		switch {
		case strings.Contains(msg.Content, "[Tool Result:") && msg.Content != "":
			// Truncate long tool results for the fallback summary.
			snippet := msg.Content
			if len(snippet) > 500 {
				snippet = snippet[:500] + "..."
			}
			parts = append(parts, snippet)
		case strings.Contains(msg.Content, "[Investigate - ToolCall"):
			parts = append(parts, msg.Content)
		case strings.Contains(msg.Content, "[Investigate - Think"):
			parts = append(parts, msg.Content)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf(
			"(Investigation round %d completed. No tool calls were made and no prior findings are available. "+
				"Please answer the research question based on your training knowledge.)", round)
	}
	return fmt.Sprintf(
		"(Investigation round %d completed. LLM did not produce a summary. "+
			"Key findings collected from tool calls:\n\n%s)",
		round, strings.Join(parts, "\n\n"))
}

// toolDescriptions builds a human-readable list of the tools available to this
// request (i.e. the already filtered set).
func toolDescriptions(tools map[string]tool.Tool) string {
	if len(tools) == 0 {
		return "(No tools available — use your training knowledge to answer the question.)"
	}
	var b strings.Builder
	for name, t := range tools {
		fmt.Fprintf(&b, "- **%s**: %s\n", name, t.Declaration().Description)
	}
	return b.String()
}

// extractClarifyAnalysis extracts the Clarify node's analysis from messages.
func extractClarifyAnalysis(messages []model.Message) string {
	for _, msg := range messages {
		if strings.Contains(msg.Content, "[Clarify - Analysis]") {
			// Strip the tag prefix.
			content := msg.Content
			if idx := strings.Index(content, "\n"); idx >= 0 {
				return strings.TrimSpace(content[idx+1:])
			}
			return strings.TrimSpace(content)
		}
	}
	return "(No initial analysis available.)"
}

// synthesizeFindings calls the LLM (without tools) to distill raw tool results
// and think messages into a clean, human-readable findings summary. This ensures
// Synthesize always receives natural-language context, not raw machine logs.
func (r *RealInvestigateRunner) synthesizeFindings(ctx context.Context, messages []model.Message, state InvestigateState) string {
	// Build a compact prompt with the raw research data.
	var rawData strings.Builder
	rawData.WriteString(fmt.Sprintf("## Research Question\n%s\n\n", state.Query))
	rawData.WriteString("## Raw Research Data\n\n")
	for _, msg := range messages {
		switch {
		case strings.Contains(msg.Content, "[Investigate - Think"):
			// Extract think content without the tag.
			content := msg.Content
			if idx := strings.Index(content, "\n"); idx >= 0 {
				rawData.WriteString(content[idx+1:])
			} else {
				rawData.WriteString(content)
			}
			rawData.WriteString("\n\n")
		case strings.Contains(msg.Content, "[Tool Result:") && msg.Content != "":
			snippet := msg.Content
			if len(snippet) > 2000 {
				snippet = snippet[:2000] + "..."
			}
			rawData.WriteString(fmt.Sprintf("[Search Result from %s]\n%s\n\n", msg.ToolName, snippet))
		}
	}
	rawData.WriteString("\nBased on the above research data, summarize the key findings in Chinese. Include: 1) core facts and data found, 2) sources of information, 3) what's uncertain or not yet confirmed. Do NOT fabricate anything not present in the raw data above.")

	sysPrompt := "You are a research analyst summarizing investigation findings. Output only the findings summary — no meta-commentary, no greetings."
	req := &model.Request{
		Messages: []model.Message{
			model.NewSystemMessage(sysPrompt),
			model.NewUserMessage(rawData.String()),
		},
		GenerationConfig: model.GenerationConfig{Stream: true},
	}

	eventCh, err := r.llm.GenerateContent(ctx, req)
	if err != nil {
		// Fallback: use the simpler text-extraction method.
		return r.synthesizeFallbackContent(messages, 0)
	}

	var content strings.Builder
	hasDeltas := false
	for evt := range eventCh {
		if evt.Error != nil {
			return r.synthesizeFallbackContent(messages, 0)
		}
		if len(evt.Choices) == 0 {
			continue
		}
		if evt.IsPartial {
			if delta := evt.Choices[0].Delta; delta.Content != "" {
				content.WriteString(delta.Content)
				hasDeltas = true
			}
		} else {
			msg := evt.Choices[0].Message
			if msg.Content != "" && !hasDeltas {
				content.WriteString(msg.Content)
			}
		}
	}

	findings := content.String()
	if findings == "" {
		return r.synthesizeFallbackContent(messages, 0)
	}
	return findings
}

// ─── types.Finding Extraction ──────────────────────────────────────────────────

// extractFindings parses tool result messages and extracts structured Findings
// without calling an LLM. It handles web_search (infra.WebSearchOutput), web_fetch
// (infra.WebFetchOutput), and search_kb tool outputs.
//
// For tool outputs that cannot be parsed as known types, the raw content is
// preserved in types.StateKeyMessages and remains available to Synthesize via the
// existing prioritizeMessages path.
func extractFindings(messages []model.Message) []types.Finding {
	var findings []types.Finding
	var totalToolMsgs, parsedMsgs int

	for _, msg := range messages {
		if msg.Content == "" {
			continue
		}

		// Tool results are stored as user messages with prefix
		// "[Tool Result: <tool_name>]\n<json_payload>"
		const prefix = "[Tool Result: "
		if !strings.HasPrefix(msg.Content, prefix) {
			continue
		}
		totalToolMsgs++

		// Extract tool name and JSON payload.
		prefixEnd := strings.Index(msg.Content, "]\n")
		if prefixEnd < 0 {
			continue
		}
		toolName := msg.Content[len(prefix):prefixEnd]
		payload := msg.Content[prefixEnd+2:] // skip "]\n"

		switch toolName {
		case "web_search":
			var output infra.WebSearchOutput
			if err := json.Unmarshal([]byte(payload), &output); err == nil {
				parsedMsgs++
				for _, r := range output.Results {
					if r.Snippet == "" && r.Title == "" {
						continue
					}
					findings = append(findings, types.Finding{
						Claim:      r.Snippet,
						Evidence:   []types.Source{{Title: r.Title, URL: r.URL}},
						Confidence: "medium",
					})
				}
			} else {
				log.Printf("[extractFindings] web_search JSON parse error: %v, raw=%q",
					err, types.TruncateForLog(payload, 200))
			}
		case "web_fetch":
			var output infra.WebFetchOutput
			if err := json.Unmarshal([]byte(payload), &output); err == nil {
				parsedMsgs++
				snippet := output.Content
				if len(snippet) > 500 {
					snippet = snippet[:500] + "..."
				}
				if snippet == "" {
					snippet = output.Title
				}
				if snippet != "" {
					findings = append(findings, types.Finding{
						Claim:      snippet,
						Evidence:   []types.Source{{Title: output.Title, URL: output.URL}},
						Confidence: "medium",
					})
				}
			}
			// Future tool types (PDF, database, code) can be added here.
			// Unknown formats remain accessible via types.StateKeyMessages →
			// prioritizeMessages in Synthesize.
		}
	}

	log.Printf("[extractFindings] %d tool messages → %d parsed → %d findings",
		totalToolMsgs, parsedMsgs, len(findings))
	return findings
}

// formatFindingsForPrompt renders Findings as a structured markdown section
// suitable for injection into the Synthesize prompt.
func formatFindingsForPrompt(findings []types.Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 调查发现 (Structured Research Findings)\n\n")
	b.WriteString("以下是从搜索结果中提取的已验证事实，请基于这些发现撰写报告：\n\n")
	for i, f := range findings {
		fmt.Fprintf(&b, "**发现 %d** (可信度: %s)\n", i+1, f.Confidence)
		fmt.Fprintf(&b, "- 内容: %s\n", f.Claim)
		for _, src := range f.Evidence {
			if src.URL != "" {
				fmt.Fprintf(&b, "- 来源: [%s](%s)\n", src.Title, src.URL)
			} else if src.Title != "" {
				fmt.Fprintf(&b, "- 来源: %s\n", src.Title)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// buildSystemPrompt constructs the investigate system prompt with all
// placeholders replaced: {{query}}, {{tools}}, {{clarify_analysis}},
// {{round}}, {{max_rounds}}.
func (r *RealInvestigateRunner) buildSystemPrompt(state InvestigateState, round, maxRounds int, tools map[string]tool.Tool) string {
	if r.prompts != nil && r.prompts.InvestigateSystem != "" {
		prompt := r.prompts.InvestigateSystem
		prompt = strings.ReplaceAll(prompt, "{{query}}", state.Query)
		prompt = strings.ReplaceAll(prompt, "{{tools}}", toolDescriptions(tools))
		prompt = strings.ReplaceAll(prompt, "{{clarify_analysis}}", extractClarifyAnalysis(state.Messages))
		prompt = strings.ReplaceAll(prompt, "{{round}}", fmt.Sprintf("%d", round))
		prompt = strings.ReplaceAll(prompt, "{{max_rounds}}", fmt.Sprintf("%d", maxRounds))
		return prompt
	}
	return types.PromptInvestigateSimple
}

// stripToolCallHallucination removes hallucinated tool-call-formatted text
// from LLM output. When the model has been exposed to [Investigate - ToolCall]
// messages in prior turns, it may mimic this format even without tools
// configured. This function strips those blocks so Synthesize receives clean
// content.
func stripToolCallHallucination(content string) string {
	// Find the earliest occurrence of any hallucinated marker.
	const markerTC = "[Investigate - ToolCall"
	const markerFA = "[Investigate - FinalAnswer]"

	cutIdx := len(content)
	if idx := strings.Index(content, markerTC); idx >= 0 && idx < cutIdx {
		cutIdx = idx
	}
	if idx := strings.Index(content, markerFA); idx >= 0 && idx < cutIdx {
		cutIdx = idx
	}

	if cutIdx < len(content) {
		cleaned := strings.TrimSpace(content[:cutIdx])
		if cleaned != "" {
			return cleaned
		}
		// The entire content starts with a hallucinated marker.
		// Return a brief fallback so Synthesize still has context.
		return "(研究完成，模型根据已有信息进行了分析。)"
	}
	return content
}
