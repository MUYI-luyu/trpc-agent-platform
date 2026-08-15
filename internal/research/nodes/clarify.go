package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/telemetry"
)

// ─── Clarify types ───────────────────────────────────────────────────────

// ClarifyActionSchemaJSON is the raw JSON Schema string for Clarify's
// structured output: { "action": "reject" | "answer" | "research" }.
const ClarifyActionSchemaJSON = `{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["reject", "answer", "research"],
      "description": "The routing decision for this query"
    },
    "content": {
      "type": "string",
      "description": "The user-facing response: an answer for 'answer' action, a polite decline with scope explanation for 'reject', or a brief research plan for 'research'"
    }
  },
  "required": ["action"]
}`

// clarifyActionSchema is the parsed JSON Schema used in the model request.
var clarifyActionSchema = mustParseJSONSchemaMap(ClarifyActionSchemaJSON)

// ClarifyAction is the structured output from Clarify's LLM call.
type ClarifyAction struct {
	Action string `json:"action"`
}

// ClarifyResult holds the full output of the Clarify node.
type ClarifyResult struct {
	Action     string  // "reject" | "answer" | "research"
	Reasoning  string  // LLM's reasoning text
	Confidence float64 // logprobs-based confidence (0 if unavailable)
}

// LLMClient abstracts the model.Model interface for testability.
type LLMClient interface {
	GenerateContent(ctx context.Context, req *model.Request) (<-chan *model.Response, error)
	Info() model.Info
}

// compile-time check: model.Model satisfies LLMClient
var _ LLMClient = (model.Model)(nil)

// ─── Clarify NodeFunc ────────────────────────────────────────────────────

// NewClarifyNodeFunc creates a Graph NodeFunc for the Clarify node.
//
// It uses the LLM with Structured Output (JSON Schema) to classify the query
// into reject/answer/research. For answer and reject actions, the reasoning
// is streamed to the client via types.StreamWriter. For research, the reasoning
// is appended to Messages for the Investigate node to use.
func NewClarifyNodeFunc(llm LLMClient, prompts *types.PromptSet, config *types.Config) graph.NodeFunc {
	return func(ctx context.Context, state graph.State) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		query, ok := graph.GetStateValue[string](state, types.StateKeyQuery)
		if !ok || query == "" {
			return nil, fmt.Errorf("clarify: missing query in state")
		}

		// Extract types.StreamWriter before the LLM call so we can stream in real-time.
		sw, _ := graph.GetStateValue[types.StreamWriter](state, types.StateKeyStreamWriter)

		// Restore prior-turn history (user queries + assistant answers) so
		// follow-up questions are classified in context. May be empty on a
		// session's first turn.
		history, _ := graph.GetStateValue[[]model.Message](state, types.StateKeyHistory)

		// Build and execute the LLM request with real-time streaming.
		req := buildClarifyRequest(prompts.ClarifySystem, query, history, config)
		result, err := executeClarifyAndStream(ctx, llm, req, sw)
		if err != nil {
			return nil, fmt.Errorf("clarify: LLM call failed: %w", err)
		}

		// Apply logprobs confidence threshold if available.
		if result.Confidence > 0 {
			result.Action = ApplyConfidenceThreshold(result.Action, result.Confidence, config.EffectiveClarifyConfidenceMin())
		}

		// Update Messages (append-only).
		messages, _ := graph.GetStateValue[[]model.Message](state, types.StateKeyMessages)
		messages = append(messages, model.NewUserMessage(query))
		if result.Reasoning != "" {
			messages = append(messages, model.NewAssistantMessage(
				fmt.Sprintf("[Clarify - Analysis]\n%s", result.Reasoning),
			))
		}

		return graph.State{
			types.StateKeyAction:   result.Action,
			types.StateKeyMessages: messages,
		}, nil
	}
}

// ─── Request building ────────────────────────────────────────────────────

func buildClarifyRequest(systemPrompt, query string, history []model.Message, config *types.Config) *model.Request {
	t := config.EffectiveClarifyTemperature()
	maxT := config.EffectiveClarifyMaxTokens()
	messages := make([]model.Message, 0, len(history)+2)
	messages = append(messages, model.NewSystemMessage(systemPrompt))
	messages = append(messages, history...)
	messages = append(messages, model.NewUserMessage(query))
	return &model.Request{
		Messages: messages,
		GenerationConfig: model.GenerationConfig{
			Stream:      true,
			Temperature: &t,
			MaxTokens:   &maxT,
		},
	}
}

// ─── LLM execution ───────────────────────────────────────────────────────

// executeClarifyAndStream calls the LLM, aggregates the full response, then
// sends a clean answer (or rejection/analysis) through sw as a single event.
func executeClarifyAndStream(ctx context.Context, llm LLMClient, req *model.Request, sw types.StreamWriter) (*ClarifyResult, error) {
	ctx, span := telemetry.SpanLLM(ctx, llm.Info().Name)
	defer span.End()

	eventCh, err := llm.GenerateContent(ctx, req)
	if err != nil {
		telemetry.RecordError(ctx, err)
		return nil, fmt.Errorf("generate content: %w", err)
	}

	if sw != nil {
		_ = sw.Write(types.NewStreamEvent(types.EventThinkStart, "clarify", "正在分析..."))
	}

	result := &ClarifyResult{Action: types.ActionAnswer} // default safe path
	var reasoning strings.Builder
	var lastContent string
	var lastUsage *model.Usage
	hasStreamed := false

	for rsp := range eventCh {
		if rsp.Usage != nil {
			lastUsage = rsp.Usage
		}
		if rsp.Error != nil {
			err := fmt.Errorf("clarify API error: %s: %s", rsp.Error.Type, rsp.Error.Message)
			telemetry.RecordError(ctx, err)
			return nil, err
		}

		if len(rsp.Choices) == 0 {
			continue
		}

		if rsp.IsPartial {
			delta := rsp.Choices[0].Delta
			if delta.Content != "" {
				reasoning.WriteString(delta.Content)
				hasStreamed = true
			}
		} else {
			// Final message. Only use it if we haven't already accumulated streaming deltas.
			msg := rsp.Choices[0].Message
			if msg.Content != "" {
				if !hasStreamed {
					reasoning.WriteString(msg.Content)
				}
				lastContent = msg.Content
			}
		}
	}

	if lastUsage != nil {
		telemetry.SetTokenUsage(ctx, lastUsage.PromptTokens, lastUsage.CompletionTokens)
	}

	result.Reasoning = reasoning.String()

	// Parse action from the JSON output.
	action := parseActionFromContent(lastContent)
	if action != "" {
		result.Action = action
	} else {
		result.Action = extractActionFromText(result.Reasoning)
	}

	// Send a clean, human-readable output through the types.StreamWriter.
	if sw != nil {
		// Try to extract a friendly text from the JSON response.
		display := extractDisplayText(result.Reasoning, result.Action)
		if display == "" {
			display = result.Reasoning
		}
		_ = sw.Write(types.NewStreamEvent(types.EventThinkEnd, "clarify", display))
	}

	return result, nil
}

// extractDisplayText extracts a human-readable message from the raw model
// output. For structured JSON responses, it strips the JSON wrapper and
// returns only the content. Falls back to the raw text or a sensible default.
func extractDisplayText(raw string, action string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Try to parse as JSON and extract a content/answer/message field.
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		delete(obj, "action")
		for _, key := range []string{"content", "answer", "message", "response", "reasoning", "analysis"} {
			if v, ok := obj[key]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		if len(obj) == 1 {
			for _, v := range obj {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	// JSON parse failed — content may be truncated. Try heuristic extraction.
	if strings.Contains(raw, `"content"`) || strings.Contains(raw, `"answer"`) {
		for _, key := range []string{`"content"`, `"answer"`, `"message"`} {
			if idx := strings.Index(raw, key); idx >= 0 {
				// Find the value after the key: "content": "VALUE"
				after := raw[idx+len(key):]
				if colon := strings.Index(after, ":"); colon >= 0 {
					val := strings.TrimSpace(after[colon+1:])
					val = strings.Trim(val, `"`)
					// Take everything until the next unescaped quote or end.
					if endQuote := strings.Index(val, `"`); endQuote > 0 {
						// Check it's not escaped.
						if endQuote == 0 || val[endQuote-1] != '\\' {
							val = val[:endQuote]
						}
					}
					if val != "" {
						return val
					}
				}
			}
		}
	}
	// Fallback.
	if action == types.ActionReject {
		return "抱歉，这个问题不在我的研究范围内。我专注于分布式系统、数据库和tRPC框架相关的技术问题。"
	}
	return raw
}

// ─── Action parsing ──────────────────────────────────────────────────────

// parseActionFromContent tries to extract the action from a content string
// that may be JSON (structured output) or plain text.
func parseActionFromContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	// Try JSON parse first.
	var a ClarifyAction
	if json.Unmarshal([]byte(content), &a) == nil && a.Action != "" {
		return a.Action
	}
	// Try extracting JSON substring.
	if idx := strings.Index(content, `{"action"`); idx >= 0 {
		end := strings.Index(content[idx:], "}")
		if end >= 0 {
			jsonStr := content[idx : idx+end+1]
			if json.Unmarshal([]byte(jsonStr), &a) == nil && a.Action != "" {
				return a.Action
			}
		}
	}
	return ""
}

// extractActionFromText heuristically finds the action in plain text.
func extractActionFromText(text string) string {
	text = strings.TrimSpace(strings.ToLower(text))
	if strings.Contains(text, `"reject"`) || strings.Contains(text, "reject") {
		return types.ActionReject
	}
	if strings.Contains(text, `"research"`) || strings.Contains(text, "research") {
		return types.ActionResearch
	}
	if strings.Contains(text, `"answer"`) || strings.Contains(text, "answer") {
		return types.ActionAnswer
	}
	return types.ActionAnswer
}

// ─── Logprobs confidence ─────────────────────────────────────────────────

// LogprobsInfo holds token-level log probabilities.
// This is provider-specific — only available when the model API supports it.
type LogprobsInfo struct {
	TopLogprobs []LogprobToken `json:"top_logprobs"`
}

// LogprobToken is a single token with its log probability.
type LogprobToken struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

// ComputeConfidence calculates a confidence score from top logprobs.
//
//	margin = topLogprobs[0].Logprob - topLogprobs[1].Logprob
//	confidence = 1 / (1 + e^(-margin))
//
// Returns 0 if insufficient data is available.
func ComputeConfidence(logprobs *LogprobsInfo) float64 {
	if logprobs == nil || len(logprobs.TopLogprobs) < 2 {
		return 0
	}
	l1 := logprobs.TopLogprobs[0].Logprob
	l2 := logprobs.TopLogprobs[1].Logprob
	margin := l1 - l2
	return 1.0 / (1.0 + math.Exp(-margin))
}

// ApplyConfidenceThreshold downgrades an answer action to research if the
// confidence is below the safety threshold.
func ApplyConfidenceThreshold(action string, confidence float64, threshold float64) string {
	if action == types.ActionAnswer && confidence > 0 && confidence < threshold {
		return types.ActionResearch
	}
	return action
}

// ─── Helpers ─────────────────────────────────────────────────────────────

func mustParseJSONSchemaMap(schema string) map[string]any {
	var parsed map[string]any
	defer func() {
		if r := recover(); r != nil {
			// Should never happen with a well-formed schema constant,
			// but provide a safe fallback rather than crashing the process.
			parsed = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type": "string",
						"enum": []string{"reject", "answer", "research"},
					},
				},
				"required": []string{"action"},
			}
		}
	}()
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		// Should never happen with a well-formed schema constant.
		panic(fmt.Sprintf("invalid clarify schema: %v", err))
	}
	return parsed
}
