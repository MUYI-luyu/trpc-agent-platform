package nodes

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/infra"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

// ─── Synthesize NodeFunc ─────────────────────────────────────────────────

// NewSynthesizeNodeFunc creates a Graph NodeFunc for the Synthesize node.
//
// It extracts Messages from state, prioritizes high-signal content (Progress
// markers + ToolResults over old Think rounds), calls the LLM for report
// generation, streams the report to the client, and runs the post-processing
// pipeline (number validation, entity check, source freshness).
func NewSynthesizeNodeFunc(llm LLMClient, prompts *types.PromptSet, config *types.Config) graph.NodeFunc {
	return func(ctx context.Context, state graph.State) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		messages, messagesOk := graph.GetStateValue[[]model.Message](state, types.StateKeyMessages)
		query, _ := graph.GetStateValue[string](state, types.StateKeyQuery)
		findings, findingsOk := graph.GetStateValue[[]types.Finding](state, types.StateKeyFindings)
		sw, _ := graph.GetStateValue[types.StreamWriter](state, types.StateKeyStreamWriter)

		log.Printf("[synthesize] state: messages=%d (ok=%v) findings=%d (ok=%v) query=%q",
			len(messages), messagesOk, len(findings), findingsOk, query)

		// Prioritize messages for the synthesis prompt.
		filtered := prioritizeMessages(messages)

		// Prepend structured findings as a user message so the LLM sees
		// verified claims first, before raw conversation messages.
		if findingsText := formatFindingsForPrompt(findings); findingsText != "" {
			findingsMsg := model.NewUserMessage(findingsText)
			filtered = append([]model.Message{findingsMsg}, filtered...)
		}

		// Build the synthesis request.
		req := buildSynthesizeRequest(prompts.SynthesizeSystem, query, filtered, config)

		// Call LLM for report generation.
		report, err := generateReport(ctx, llm, req, sw)
		if err != nil {
			return nil, fmt.Errorf("synthesize: LLM generation failed: %w", err)
		}

		// Run post-processing pipeline.
		sourceTexts := extractSourceTexts(messages)
		ppResult := infra.PostProcessReport(report, sourceTexts)

		return graph.State{
			types.StateKeyReport: ppResult.Report,
		}, nil
	}
}

// ─── Message prioritization ──────────────────────────────────────────────

// prioritizeMessages selects high-signal messages for the synthesis prompt.
//
// Priority order:
//  0. User query + Clarify analysis — essential context (prepended)
//  1. Findings summary (all rounds) — clean human-readable results
//  2. Progress markers (all rounds) — high signal density
//  3. ToolResult content (all rounds) — factual sources for citations
//  4. Think reasoning (all rounds) — understanding the full analysis chain
//  5. ToolCall params (all rounds) — understanding search strategy evolution
func prioritizeMessages(all []model.Message) []model.Message {
	// Separate messages by type.
	var queryClarify, findings, progress, toolResults, allThink, allToolCalls []model.Message

	// Only keep the LAST user message (current query) to avoid
	// session pollution from previous unrelated conversations.
	var lastUserMsg model.Message
	for _, msg := range all {
		if msg.Role == model.RoleUser {
			lastUserMsg = msg
		}
	}
	if lastUserMsg.Content != "" {
		queryClarify = append(queryClarify, lastUserMsg)
	}

	for _, msg := range all {
		content := msg.Content

		switch {
		case msg.Role == model.RoleUser:
			// skip — only last user message kept above
		case strings.Contains(content, "[Clarify - Analysis]"):
			queryClarify = append(queryClarify, msg)
		case strings.Contains(content, "[Investigate - Findings]"):
			findings = append(findings, msg)
		case strings.Contains(content, "[Investigate - Progress"),
			strings.Contains(content, "[Investigate - Round"):
			progress = append(progress, msg)
		case strings.Contains(content, "[Tool Result:"):
			toolResults = append(toolResults, msg)
		case strings.Contains(content, "[Investigate - Think"):
			allThink = append(allThink, msg)
		case strings.Contains(content, "[Investigate - ToolCall"):
			allToolCalls = append(allToolCalls, msg)
		}
	}

	// Build prioritized list: Query/Clarify → Findings → Progress → ToolResults → Think.
	// ToolCall messages are deliberately excluded — they contain base64-encoded JSON
	// args that confuse the LLM into echoing tool calls instead of writing a report.
	result := make([]model.Message, 0,
		len(queryClarify)+len(findings)+len(progress)+len(toolResults)+len(allThink))
	result = append(result, queryClarify...)
	result = append(result, findings...)
	result = append(result, progress...)
	result = append(result, toolResults...)
	result = append(result, allThink...)

	return result
}

// ─── Request building ────────────────────────────────────────────────────

func buildSynthesizeRequest(systemPrompt, query string, messages []model.Message, config *types.Config) *model.Request {
	t := config.EffectiveSynthesizeTemperature()
	maxT := config.EffectiveSynthesizeMaxTokens()
	sysPrompt := strings.ReplaceAll(systemPrompt, "{{query}}", query)
	reqMessages := []model.Message{model.NewSystemMessage(sysPrompt)}
	reqMessages = append(reqMessages, messages...)
	return &model.Request{
		Messages: reqMessages,
		GenerationConfig: model.GenerationConfig{
			Stream:      true,
			Temperature: &t,
			MaxTokens:   &maxT,
		},
	}
}

// ─── Report generation ───────────────────────────────────────────────────

func generateReport(ctx context.Context, llm LLMClient, req *model.Request, sw types.StreamWriter) (string, error) {
	ctx, span := telemetry.SpanLLM(ctx, llm.Info().Name)
	defer span.End()

	eventCh, err := llm.GenerateContent(ctx, req)
	if err != nil {
		telemetry.RecordError(ctx, err)
		return "", fmt.Errorf("generate content: %w", err)
	}

	var report strings.Builder
	hasDeltas := false
	nodeName := "synthesize"

	if sw != nil {
		_ = sw.Write(types.NewStreamEvent(types.EventThinkStart, nodeName, "正在生成报告..."))
	}

	var finishReason *string
	var lastUsage *model.Usage
	for rsp := range eventCh {
		if rsp.Usage != nil {
			lastUsage = rsp.Usage
		}
		if rsp.Error != nil {
			err := fmt.Errorf("synthesize API error: %s: %s", rsp.Error.Type, rsp.Error.Message)
			telemetry.RecordError(ctx, err)
			return report.String(), err
		}

		if len(rsp.Choices) == 0 {
			continue
		}
		if !rsp.IsPartial {
			finishReason = rsp.Choices[0].FinishReason
		}

		if rsp.IsPartial {
			delta := rsp.Choices[0].Delta
			if delta.Content != "" {
				report.WriteString(delta.Content)
				hasDeltas = true
			}
		} else {
			msg := rsp.Choices[0].Message
			if msg.Content != "" && !hasDeltas {
				report.WriteString(msg.Content)
			}
		}
	}

	fullReport := report.String()

	if lastUsage != nil {
		telemetry.SetTokenUsage(ctx, lastUsage.PromptTokens, lastUsage.CompletionTokens)
	}

	if finishReason != nil {
		log.Printf("[synthesize] finish_reason=%q reportLen=%d", *finishReason, len(fullReport))
	}

	// Send the complete report as a single event.
	if sw != nil && fullReport != "" {
		_ = sw.Write(types.NewStreamEvent(types.EventThinkEnd, nodeName, fullReport))
	}

	return fullReport, nil
}

// ─── types.Source extraction ───────────────────────────────────────────────────

// extractSourceTexts extracts content from Messages suitable for
// post-processing checks (ToolResults + system messages).
func extractSourceTexts(messages []model.Message) []string {
	var texts []string
	for _, msg := range messages {
		content := msg.Content
		// Include tool results and system messages as source material.
		if msg.Role == model.RoleTool || strings.Contains(content, "[Investigate - ToolResult") {
			texts = append(texts, content)
		}
	}
	return texts
}

// ─── Round extraction helper ─────────────────────────────────────────────

// extractRound extracts the round number from a message content string.
// Looks for patterns like "Round N" or "Round N:".
func extractRound(content string) int {
	for i := 0; i < len(content)-6; i++ {
		if content[i] == 'R' && strings.HasPrefix(content[i:], "Round ") {
			n := 0
			j := i + len("Round ")
			for j < len(content) && content[j] >= '0' && content[j] <= '9' {
				n = n*10 + int(content[j]-'0')
				j++
			}
			if n > 0 {
				return n
			}
		}
	}
	return 0
}
