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

	researchgraph "github.com/MUYI-luyu/trpc-agent-platform/internal/research/graph"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/infra"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
)

// ServiceConfig holds all dependencies for creating a ResearchService.
type ServiceConfig struct {
	Model          model.Model
	SessionService session.Service
	Tools          map[string]tool.Tool
	ToolDeps       *infra.ToolDeps
	Prompts        *types.PromptSet
	LockManager    researchgraph.LockManager
	Config         *types.Config
	MaxRounds      int
}

// Service is the top-level API for the Research Agent.
type Service struct {
	graph      *graph.Graph
	runner     runner.Runner
	llm        model.Model
	sessionSvc session.Service
	lockMgr    researchgraph.LockManager
	prompts    *types.PromptSet
	maxRounds  int
}

// NewService creates a ResearchService from the given configuration.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Prompts == nil {
		cfg.Prompts = types.DefaultPrompts()
	}
	if cfg.LockManager == nil {
		cfg.LockManager = researchgraph.NewInMemoryLockManager()
	}
	if cfg.Config == nil {
		cfg.Config = types.DefaultConfig()
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = cfg.Config.EffectiveMaxRounds()
	}
	if cfg.SessionService == nil {
		cfg.SessionService = inmemory.NewSessionService()
	}

	g, err := researchgraph.BuildGraph(
		researchgraph.WithModel(cfg.Model),
		researchgraph.WithPrompts(cfg.Prompts),
		researchgraph.WithTools(cfg.Tools),
		researchgraph.WithConfig(cfg.Config),
	)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}

	graphAgent, err := graphagent.New("research-agent", g,
		graphagent.WithDescription("Multi-node research pipeline: Clarify → Investigate → Synthesize"),
	)
	if err != nil {
		return nil, fmt.Errorf("create graph agent: %w", err)
	}

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

func (s *Service) Close() error {
	if err := s.runner.Close(); err != nil {
		return err
	}
	return s.lockMgr.Close()
}

func (s *Service) Run(ctx context.Context, query, tenantID, sessionID string) (<-chan *event.Event, error) {
	return s.RunWithLock(ctx, query, tenantID, sessionID, 30*time.Second, types.NopStreamWriter{})
}

func (s *Service) RunStreaming(ctx context.Context, query, tenantID, sessionID string, sw types.StreamWriter) (<-chan *event.Event, error) {
	return s.RunWithLock(ctx, query, tenantID, sessionID, 30*time.Second, sw)
}

func (s *Service) RunWithLock(ctx context.Context, query, tenantID, sessionID string, lockTimeout time.Duration, sw types.StreamWriter) (<-chan *event.Event, error) {
	lk, err := s.lockMgr.TryLock(ctx, sessionID)
	if err != nil {
		if err == researchgraph.ErrLockHeld {
			return nil, types.ErrSessionLocked
		}
		return nil, fmt.Errorf("acquire session lock: %w", err)
	}
	defer lk.Unlock(ctx)

	if sw == nil {
		sw = types.NopStreamWriter{}
	}

	initialState := types.NewResearchState(
		query, tenantID, sessionID,
		s.maxRounds, nil,
		sw,
	)

	userMsg := model.NewUserMessage(query)
	return s.runner.Run(ctx, tenantID, sessionID, userMsg,
		agent.WithRuntimeState(initialState),
	)
}
