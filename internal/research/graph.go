package research

import (
	"context"
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// BuildGraph constructs the 3-Node Research Agent graph.
//
// Topology:
//
//	Entry → Clarify → [reject|answer|research]
//	  reject  → END
//	  answer  → END
//	  research → Investigate → Synthesize → END
//
// In Phase 1, all three nodes are skeleton implementations that pass
// state through without calling an LLM. Real implementations replace
// them in later phases.
func BuildGraph(opts ...GraphOption) (*graph.Graph, error) {
	cfg := defaultGraphConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	schema := NewResearchStateSchema()
	sg := graph.NewStateGraph(schema)

	// Register the three nodes.
	sg.AddNode("clarify", clarifyNodeSkeleton(cfg), graph.WithName("Clarify"))
	sg.AddNode("investigate", investigateNodeSkeleton(cfg), graph.WithName("Investigate"))
	sg.AddNode("synthesize", synthesizeNodeSkeleton(cfg), graph.WithName("Synthesize"))

	// Routing.
	sg.SetEntryPoint("clarify")

	// Clarify → conditional edge based on Action.
	sg.AddConditionalEdges("clarify", clarifyRouteCondition, map[string]string{
		ActionReject:   graph.End,
		ActionAnswer:   graph.End,
		ActionResearch: "investigate",
	})

	// Investigate → Synthesize (linear).
	sg.AddEdge("investigate", "synthesize")

	// Synthesize → END.
	sg.SetFinishPoint("synthesize")

	return sg.Compile()
}

// ─── Graph configuration ─────────────────────────────────────────────────

// GraphConfig holds dependencies injected into the graph nodes.
type GraphConfig struct {
	// Model is the LLM used by Clarify, Investigate, and Synthesize.
	// In Phase 1 this is optional (skeleton nodes don't call LLM).
	Model model.Model

	// Tools is the pool of available tools.
	Tools map[string]tool.Tool

	// Prompts holds the system prompt constants. If nil, defaults are used.
	Prompts *PromptSet

	// Config holds tunable parameters (temperature, max tokens, etc.).
	// If nil, DefaultConfig() is used internally by each node.
	Config *Config

	// InvestigateRunner executes the ReAct loop. If nil (and Model is set),
	// a simple single-call LLM runner is used as fallback.
	InvestigateRunner InvestigateRunner
}

// PromptSet bundles the system prompts for all three nodes.
type PromptSet struct {
	ClarifySystem    string
	InvestigateSystem string
	SynthesizeSystem  string
}

// DefaultPrompts returns a PromptSet populated with the built-in prompt constants.
func DefaultPrompts() *PromptSet {
	return &PromptSet{
		ClarifySystem:    PromptClarifySystem,
		InvestigateSystem: PromptInvestigateSystem,
		SynthesizeSystem:  PromptSynthesizeSystem,
	}
}

// GraphOption is a functional option for BuildGraph.
type GraphOption func(*GraphConfig)

// WithModel sets the LLM model for all nodes.
func WithModel(m model.Model) GraphOption {
	return func(cfg *GraphConfig) { cfg.Model = m }
}

// WithTools sets the tool pool.
func WithTools(tools map[string]tool.Tool) GraphOption {
	return func(cfg *GraphConfig) { cfg.Tools = tools }
}

// WithPrompts sets custom prompts.
func WithPrompts(p *PromptSet) GraphOption {
	return func(cfg *GraphConfig) { cfg.Prompts = p }
}

// WithConfig sets tunable parameters.
func WithConfig(c *Config) GraphOption {
	return func(cfg *GraphConfig) { cfg.Config = c }
}

// WithInvestigateRunner sets a custom InvestigateRunner.
func WithInvestigateRunner(r InvestigateRunner) GraphOption {
	return func(cfg *GraphConfig) { cfg.InvestigateRunner = r }
}

func defaultGraphConfig() *GraphConfig {
	return &GraphConfig{
		Prompts: DefaultPrompts(),
		Tools:   make(map[string]tool.Tool),
		Config:  DefaultConfig(),
	}
}

// ─── Conditional routing ─────────────────────────────────────────────────

// clarifyRouteCondition reads StateKeyAction and maps it to the next node.
// It implements graph.ConditionalFunc.
func clarifyRouteCondition(ctx context.Context, state graph.State) (string, error) {
	action, ok := graph.GetStateValue[string](state, StateKeyAction)
	if !ok || action == "" {
		// Default: treat as answer if no action was set.
		return ActionAnswer, nil
	}
	return action, nil
}

// ─── Skeleton node functions (Phase 1) ───────────────────────────────────

// clarifyNodeSkeleton is a pass-through that sets Action to "answer".
// In Phase 2 this will be replaced by the real Clarify LLM node.
func clarifyNodeSkeleton(cfg *GraphConfig) graph.NodeFunc {
	return func(ctx context.Context, state graph.State) (any, error) {
		// Check for context cancellation.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		query, _ := graph.GetStateValue[string](state, StateKeyQuery)
		if query == "" {
			return nil, fmt.Errorf("clarify: missing query in state")
		}

			// If a Model is configured, use the real Clarify LLM node.
		if cfg.Model != nil {
			clarifyFn := NewClarifyNodeFunc(cfg.Model, cfg.Prompts, cfg.Config)
			return clarifyFn(ctx, state)
		}

		// Fallback: use heuristic classification.
		action := ClassifyQuery(query)
		messages, _ := graph.GetStateValue[[]model.Message](state, StateKeyMessages)
		messages = append(messages, model.NewUserMessage(query))

		return graph.State{
			StateKeyAction:   action,
			StateKeyMessages: messages,
		}, nil
	}
}

// investigateNodeSkeleton delegates to the real Investigate node if a runner or
// model is configured. Otherwise it adds a placeholder message.
func investigateNodeSkeleton(cfg *GraphConfig) graph.NodeFunc {
	// Use the real implementation if a runner is configured.
	if cfg.InvestigateRunner != nil {
		return NewInvestigateNodeFunc(cfg.InvestigateRunner)
	}
	// Fallback: if model is available but no runner, create a SimpleLLM runner.
	// If tools are also configured, create a RealInvestigateRunner with ReAct loop.
	if cfg.Model != nil {
		var runner InvestigateRunner
		if len(cfg.Tools) > 0 {
			runner = NewRealInvestigateRunner(cfg.Model, cfg.Tools, cfg.Prompts, cfg.Config)
		} else {
			runner = NewSimpleLLMInvestigateRunner(cfg.Model, cfg.Prompts)
		}
		return NewInvestigateNodeFunc(runner)
	}
	// Skeleton fallback.
	return func(ctx context.Context, state graph.State) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		messages, _ := graph.GetStateValue[[]model.Message](state, StateKeyMessages)
		messages = append(messages, model.NewAssistantMessage("[Skeleton: investigation would happen here]"))
		return graph.State{StateKeyMessages: messages}, nil
	}
}

// synthesizeNodeSkeleton delegates to the real Synthesize node if a model is
// configured. Otherwise it produces a placeholder report.
func synthesizeNodeSkeleton(cfg *GraphConfig) graph.NodeFunc {
	if cfg.Model != nil {
		return NewSynthesizeNodeFunc(cfg.Model, cfg.Prompts, cfg.Config)
	}
	// Skeleton fallback.
	return func(ctx context.Context, state graph.State) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		messages, _ := graph.GetStateValue[[]model.Message](state, StateKeyMessages)
		query, _ := graph.GetStateValue[string](state, StateKeyQuery)
		report := fmt.Sprintf("# Research Report: %s\n\n[Skeleton: %d messages in context]", query, len(messages))
		return graph.State{
			StateKeyReport:   report,
			StateKeyMessages: messages,
		}, nil
	}
}

// ─── Simple query classifier (Phase 1 heuristic) ─────────────────────────

// ClassifyQuery is a public heuristic classifier used as a fallback when no
// LLM is configured. It identifies reject/answer/research patterns from the
// query text. For production use, the Clarify Node's LLM-based classification
// (NewClarifyNodeFunc) provides higher accuracy.
func ClassifyQuery(query string) string {
	// Reject patterns: obvious off-topic requests.
	rejectPatterns := []string{
		"写代码", "写个爬虫", "帮我写", "怎么做菜", "天气",
		"推荐小说", "推荐电影", "玩什么", "买什么",
	}
	for _, p := range rejectPatterns {
		if len(query) >= len(p) && containsSubstring(query, p) {
			return ActionReject
		}
	}

	// Research patterns: comparison, deep analysis, how-it-works.
	researchPatterns := []string{
		"区别", "对比", "比较", "vs", "VS",
		"优缺点", "trade", "取舍", "选哪个",
		"实现", "原理", "源码", "架构",
		"怎么用", "如何配置", "最佳实践",
		"性能", "benchmark", "延迟",
	}
	for _, p := range researchPatterns {
		if len(query) >= len(p) && containsSubstring(query, p) {
			return ActionResearch
		}
	}

	// Default: simple definition or fact → answer.
	return ActionAnswer
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
