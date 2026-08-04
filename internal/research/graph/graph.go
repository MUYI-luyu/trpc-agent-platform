package graph

import (
	"context"
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/nodes"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
)

// BuildGraph constructs the 3-Node Research Agent graph.
//
// Topology:
//
//	Entry → Clarify → [reject|answer|research]
//	  reject  → END
//	  answer  → END
//	  research → Investigate → Synthesize → END
func BuildGraph(opts ...GraphOption) (*graph.Graph, error) {
	cfg := defaultGraphConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	schema := types.NewResearchStateSchema()
	sg := graph.NewStateGraph(schema)

	// Register the three nodes.
	sg.AddNode("clarify", clarifyNodeSkeleton(cfg), graph.WithName("Clarify"))
	sg.AddNode("investigate", investigateNodeSkeleton(cfg), graph.WithName("Investigate"))
	sg.AddNode("synthesize", synthesizeNodeSkeleton(cfg), graph.WithName("Synthesize"))

	// Routing.
	sg.SetEntryPoint("clarify")

	// Clarify → conditional edge based on Action.
	sg.AddConditionalEdges("clarify", clarifyRouteCondition, map[string]string{
		types.ActionReject:   graph.End,
		types.ActionAnswer:   graph.End,
		types.ActionResearch: "investigate",
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
	Model model.Model

	// Tools is the pool of available tools.
	Tools map[string]tool.Tool

	// Prompts holds the system prompt constants. If nil, defaults are used.
	Prompts *types.PromptSet

	// Config holds tunable parameters (temperature, max tokens, etc.).
	// If nil, DefaultConfig() is used internally by each node.
	Config *types.Config

	// InvestigateRunner executes the ReAct loop. If nil (and Model is set),
	// a simple single-call LLM runner is used as fallback.
	InvestigateRunner nodes.InvestigateRunner
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
func WithPrompts(p *types.PromptSet) GraphOption {
	return func(cfg *GraphConfig) { cfg.Prompts = p }
}

// WithConfig sets tunable parameters.
func WithConfig(c *types.Config) GraphOption {
	return func(cfg *GraphConfig) { cfg.Config = c }
}

// WithInvestigateRunner sets a custom InvestigateRunner.
func WithInvestigateRunner(r nodes.InvestigateRunner) GraphOption {
	return func(cfg *GraphConfig) { cfg.InvestigateRunner = r }
}

func defaultGraphConfig() *GraphConfig {
	return &GraphConfig{
		Prompts: types.DefaultPrompts(),
		Tools:   make(map[string]tool.Tool),
		Config:  types.DefaultConfig(),
	}
}

// ─── Conditional routing ─────────────────────────────────────────────────

// clarifyRouteCondition reads StateKeyAction and maps it to the next node.
func clarifyRouteCondition(ctx context.Context, state graph.State) (string, error) {
	action, ok := graph.GetStateValue[string](state, types.StateKeyAction)
	if !ok || action == "" {
		return types.ActionAnswer, nil
	}
	return action, nil
}

// ─── Skeleton node functions ──────────────────────────────────────────────

func clarifyNodeSkeleton(cfg *GraphConfig) graph.NodeFunc {
	return func(ctx context.Context, state graph.State) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		query, _ := graph.GetStateValue[string](state, types.StateKeyQuery)
		if query == "" {
			return nil, fmt.Errorf("clarify: missing query in state")
		}

		if cfg.Model != nil {
			clarifyFn := nodes.NewClarifyNodeFunc(cfg.Model, cfg.Prompts, cfg.Config)
			return clarifyFn(ctx, state)
		}

		// Fallback: use heuristic classification.
		action := ClassifyQuery(query)
		messages, _ := graph.GetStateValue[[]model.Message](state, types.StateKeyMessages)
		messages = append(messages, model.NewUserMessage(query))

		return graph.State{
			types.StateKeyAction:   action,
			types.StateKeyMessages: messages,
		}, nil
	}
}

func investigateNodeSkeleton(cfg *GraphConfig) graph.NodeFunc {
	if cfg.InvestigateRunner != nil {
		return nodes.NewInvestigateNodeFunc(cfg.InvestigateRunner)
	}
	if cfg.Model != nil {
		var runner nodes.InvestigateRunner
		if len(cfg.Tools) > 0 {
			runner = nodes.NewRealInvestigateRunner(cfg.Model, cfg.Tools, cfg.Prompts, cfg.Config)
		} else {
			runner = nodes.NewSimpleLLMInvestigateRunner(cfg.Model, cfg.Prompts)
		}
		return nodes.NewInvestigateNodeFunc(runner)
	}
	// Skeleton fallback.
	return func(ctx context.Context, state graph.State) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		messages, _ := graph.GetStateValue[[]model.Message](state, types.StateKeyMessages)
		messages = append(messages, model.NewAssistantMessage("[Skeleton: investigation would happen here]"))
		return graph.State{types.StateKeyMessages: messages}, nil
	}
}

func synthesizeNodeSkeleton(cfg *GraphConfig) graph.NodeFunc {
	if cfg.Model != nil {
		return nodes.NewSynthesizeNodeFunc(cfg.Model, cfg.Prompts, cfg.Config)
	}
	return func(ctx context.Context, state graph.State) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		messages, _ := graph.GetStateValue[[]model.Message](state, types.StateKeyMessages)
		query, _ := graph.GetStateValue[string](state, types.StateKeyQuery)
		report := fmt.Sprintf("# Research Report: %s\n\n[Skeleton: %d messages in context]", query, len(messages))
		return graph.State{
			types.StateKeyReport:   report,
			types.StateKeyMessages: messages,
		}, nil
	}
}

// ─── Simple query classifier (heuristic) ──────────────────────────────────

// ClassifyQuery is a public heuristic classifier used as a fallback when no
// LLM is configured.
func ClassifyQuery(query string) string {
	rejectPatterns := []string{
		"写代码", "写个爬虫", "帮我写", "怎么做菜", "天气",
		"推荐小说", "推荐电影", "玩什么", "买什么",
	}
	for _, p := range rejectPatterns {
		if len(query) >= len(p) && containsSubstring(query, p) {
			return types.ActionReject
		}
	}

	researchPatterns := []string{
		"区别", "对比", "比较", "vs", "VS",
		"优缺点", "trade", "取舍", "选哪个",
		"实现", "原理", "源码", "架构",
		"怎么用", "如何配置", "最佳实践",
		"性能", "benchmark", "延迟",
	}
	for _, p := range researchPatterns {
		if len(query) >= len(p) && containsSubstring(query, p) {
			return types.ActionResearch
		}
	}

	return types.ActionAnswer
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
