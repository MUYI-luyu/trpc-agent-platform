package research

import (
	"context"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// ServiceConfig holds all dependencies for creating a ResearchService.
type ServiceConfig struct {
	// Model is the LLM used by all three nodes. Required for production use;
	// if nil, the graph uses skeleton (heuristic) nodes.
	Model model.Model

	// SessionService provides session persistence. If nil, an in-memory
	// service is created automatically by the Runner.
	SessionService session.Service

	// Tools are the research tools (search_kb, web_search, web_fetch).
	// If nil, empty (no-op) tools are used.
	Tools map[string]tool.Tool

	// ToolDeps provides backing implementations for research tools.
	ToolDeps *ToolDeps

	// Prompts allow custom system prompts. If nil, defaults are used.
	Prompts *PromptSet

	// LockManager provides session-level mutual exclusion.
	// If nil, a new InMemoryLockManager is created.
	LockManager LockManager

	// Config holds tunable parameters (temperature, max tokens, timeouts).
	// If nil, DefaultConfig() is used.
	Config *Config

	// MaxRounds is the default max search rounds. 0 means use DefaultMaxRounds.
	// Deprecated: prefer Config.MaxRounds.
	MaxRounds int
}

// Service is the top-level API for the Research Agent. It manages the
// compiled Graph, the GraphAgent wrapper, and the Runner.
type Service struct {
	graph      *graph.Graph
	runner     runner.Runner
	llm        model.Model
	sessionSvc session.Service
	lockMgr    LockManager
	prompts    *PromptSet
	maxRounds  int
}

// NewService creates a ResearchService from the given configuration.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Prompts == nil {
		cfg.Prompts = DefaultPrompts()
	}
	if cfg.LockManager == nil {
		cfg.LockManager = NewInMemoryLockManager()
	}
	if cfg.Config == nil {
		cfg.Config = DefaultConfig()
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = cfg.Config.EffectiveMaxRounds()
	}
	if cfg.SessionService == nil {
		cfg.SessionService = inmemory.NewSessionService()
	}

	g, err := BuildGraph(
		WithModel(cfg.Model),
		WithPrompts(cfg.Prompts),
		WithTools(cfg.Tools),
		WithConfig(cfg.Config),
	)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}

	// Wrap the compiled graph as a GraphAgent (framework standard path).
	graphAgent, err := graphagent.New("research-agent", g,
		graphagent.WithDescription("Multi-node research pipeline: Clarify → Investigate → Synthesize"),
	)
	if err != nil {
		return nil, fmt.Errorf("create graph agent: %w", err)
	}

	// Create the Runner with session service for persistence and telemetry.
	r := runner.NewRunner("research-agent", graphAgent,
		runner.WithSessionService(cfg.SessionService),
	)

	return &Service{
		graph:      g,
		runner:     r,
		llm:        cfg.Model,
		sessionSvc: cfg.SessionService,
		lockMgr:    cfg.LockManager,
		prompts:    cfg.Prompts,
		maxRounds:  cfg.MaxRounds,
	}, nil
}

// Close releases resources held by the Service.
func (s *Service) Close() error {
	if err := s.runner.Close(); err != nil {
		return err
	}
	return s.lockMgr.Close()
}

// Run executes the Research Agent on a query and returns a channel of framework
// events. Use RunStreaming if you need custom progress events (SSE).
//
// The sessionID is used to acquire a session-level lock to prevent concurrent
// requests from corrupting the conversation history.
func (s *Service) Run(ctx context.Context, query, tenantID, sessionID string) (<-chan *event.Event, error) {
	return s.RunWithLock(ctx, query, tenantID, sessionID, 30*time.Second, NopStreamWriter{})
}

// RunStreaming is like Run but pushes progress events (tool_start, tool_end,
// progress) through the provided StreamWriter during execution.
func (s *Service) RunStreaming(ctx context.Context, query, tenantID, sessionID string, sw StreamWriter) (<-chan *event.Event, error) {
	return s.RunWithLock(ctx, query, tenantID, sessionID, 30*time.Second, sw)
}

// RunWithLock executes the Research Agent with an explicit lock timeout
// and an optional StreamWriter for progress events.
func (s *Service) RunWithLock(ctx context.Context, query, tenantID, sessionID string, lockTimeout time.Duration, sw StreamWriter) (<-chan *event.Event, error) {
	// Acquire session lock.
	lock, err := s.lockMgr.TryLock(ctx, sessionID)
	if err != nil {
		if err == ErrLockHeld {
			return nil, ErrSessionLocked
		}
		return nil, fmt.Errorf("acquire session lock: %w", err)
	}
	defer lock.Unlock(ctx)

	if sw == nil {
		sw = NopStreamWriter{}
	}

	// Build initial graph state. This is merged with the GraphAgent's
	// initial state and the framework's auto-seeded keys (user_input,
	// messages, session) via agent.WithRuntimeState().
	initialState := NewResearchState(
		query, tenantID, sessionID,
		s.maxRounds, nil,
		sw,
	)

	// Run through the framework's standard path (Runner → GraphAgent → Executor).
	// The Runner handles session persistence, telemetry, and event lifecycle.
	userMsg := model.NewUserMessage(query)
	return s.runner.Run(ctx, tenantID, sessionID, userMsg,
		agent.WithRuntimeState(initialState),
	)
}
