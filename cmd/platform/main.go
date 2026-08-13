// Command platform is the unified entry point for the Agent Platform.
// It starts the Gateway, Worker Pool, Admin API, and all infrastructure
// services (telemetry, audit, backends, channel adapters).
//
// Quick start (single-tenant):
//
//	# Without LLM (heuristic classification only):
//	go run ./cmd/platform/
//
//	# With DeepSeek (research agent with tools):
//	export DEEPSEEK_API_KEY="sk-..."
//	go run ./cmd/platform/
//
//	# With WeChat Work:
//	export WECOM_CORP_ID=...
//	export WECOM_TOKEN=...
//	export WECOM_ENCODING_AES_KEY=...
//	export WECOM_APP_SECRET=...
//	export WECOM_AGENT_ID=...
//	go run ./cmd/platform/
//
// # Test with curl:
//
//	# Simple question (Clarify → Answer):
//	curl -X POST http://localhost:8080/api/v1/agents/research-agent \
//	  -H "Content-Type: application/json" \
//	  -d '{"query":"Raft是什么"}'
//
//	# Research question (Clarify → Investigate → Synthesize):
//	curl -X POST http://localhost:8080/api/v1/agents/research-agent \
//	  -H "Content-Type: application/json" \
//	  -d '{"query":"Raft和Paxos的区别"}'
//
//	# Rejected question:
//	curl -X POST http://localhost:8080/api/v1/agents/research-agent \
//	  -H "Content-Type: application/json" \
//	  -d '{"query":"怎么做红烧肉"}'
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/audit"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/backend"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/channel"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/platform"
	researchgraph "github.com/MUYI-luyu/trpc-agent-platform/internal/research/graph"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/infra"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/telemetry"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/tenant"
)

func main() {
	// ── 1. Load configuration ──────────────────────────────────
	cfg := platform.LoadConfigFromEnv()
	log.Printf("platform: starting (addr=%s, backend=%s, telemetry=%s, audit=%s)",
		cfg.Server.Addr, cfg.DataBackend.Type, cfg.Telemetry.Exporter, cfg.Audit.Type)

	// ── 2. Initialize telemetry ────────────────────────────────
	ctx := context.Background()
	shutdownTelemetry, err := telemetry.Init(ctx, cfg.Telemetry)
	if err != nil {
		log.Printf("platform: telemetry init failed (continuing without traces): %v", err)
	}
	if shutdownTelemetry != nil {
		defer func() {
			if err := shutdownTelemetry(ctx); err != nil {
				log.Printf("platform: telemetry shutdown: %v", err)
			}
		}()
	}

	// ── 3. Tenant manager ─────────────────────────────────────
	tenantMgr := tenant.NewInMemoryManager()

	for _, tc := range cfg.Tenants {
		t := tenantConfigToTenant(tc)
		if err := tenantMgr.Create(ctx, &t); err != nil {
			log.Printf("platform: bootstrap tenant %q: %v", tc.ID, err)
		} else {
			log.Printf("platform: bootstrapped tenant %q (backend=%s, rate=%d rps)",
				t.ID, t.DataBackendConfig.Type, t.RateLimits.RequestsPerSecond)
		}
	}

	// ── 4. Audit writer ────────────────────────────────────────
	var auditWriter audit.Writer = audit.NewNoopWriter()
	if cfg.Audit.Type == "sqlite" {
		aw, err := audit.NewSQLiteWriter(cfg.Audit.DSN)
		if err != nil {
			log.Printf("platform: audit init failed (continuing without audit): %v", err)
		} else {
			auditWriter = aw
			log.Printf("platform: audit writer ready (dsn=%s)", cfg.Audit.DSN)
		}
	}

	// ── 5. Backend factory ─────────────────────────────────────
	backendFactory := backend.NewFactory()

	// ── 6. LLM model ───────────────────────────────────────────
	var llmModel model.Model
	if cfg.LLM.APIKey != "" {
		opts := []openai.Option{
			openai.WithAPIKey(cfg.LLM.APIKey),
			openai.WithChannelBufferSize(256),
			openai.WithBaseURL(cfg.LLM.BaseURL),
			openai.WithVariant(openai.VariantDeepSeek),
		}
		llmModel = openai.New(cfg.LLM.DefaultModel, opts...)
		log.Printf("platform: LLM ready (provider=%s, model=%s)", cfg.LLM.Provider, cfg.LLM.DefaultModel)
	} else {
		log.Println("platform: no LLM configured (set DEEPSEEK_API_KEY)")
	}

	// ── 7. Research tools ─────────────────────────────────────
	searchCfg := infra.LoadSearchBackendConfig()
	searchFn := infra.NewWebSearchFunc(searchCfg)
	deps := &infra.ToolDeps{
		WebFetchFn: infra.DefaultWebFetch,
	}
	if searchFn != nil {
		deps.WebSearchFn = searchFn
		log.Printf("platform: search backend=%q", searchCfg.Backend)
	}
	researchTools := infra.NewTools(deps)

	log.Printf("platform: %d research tools loaded", len(researchTools))

	// ── 8. Build Research GraphAgent ───────────────────────────
	// The Research Agent is a 3-node GraphAgent:
	//   Clarify (confidence-based routing) → answer/reject/research
	//   Investigate (multi-round ReAct with tools) → Synthesize (report)
	var graphAgent agent.Agent
	if llmModel != nil {
		g, err := researchgraph.BuildGraph(
			researchgraph.WithModel(llmModel),
			researchgraph.WithTools(researchTools),
		)
		if err != nil {
			log.Fatalf("platform: build research graph: %v", err)
		}

		ga, err := graphagent.New("research-agent", g,
			graphagent.WithDescription(
				"Research Agent — 3-node graph (Clarify → Investigate → Synthesize) "+
					"with web search and web fetch tools",
			),
		)
		if err != nil {
			log.Fatalf("platform: create graph agent: %v", err)
		}
		graphAgent = ga
		log.Printf("platform: research GraphAgent ready (maxRounds=%d, tools=%d)",
			5, len(researchTools))
	}

	// ── 9. Agent provider ─────────────────────────────────────
	// The provider returns a researchAgentAdapter that injects dynamic
	// state (query, tenant, session) into the GraphAgent at runtime.
	agentProv := func(ctx context.Context, t *tenant.Tenant) (agent.Agent, error) {
		if graphAgent == nil {
			return nil, platform.ErrNoAgent
		}
		return &researchAgentAdapter{
			inner:     graphAgent,
			maxRounds: 5,
		}, nil
	}

	// ── 10. Worker Pool ────────────────────────────────────────
	worker := platform.NewWorker(tenantMgr, backendFactory, auditWriter, agentProv)

	// ── 11. Gateway ─────────────────────────────────────────────
	gateway := platform.NewGateway(cfg.Server, worker)

	// ── 11b. Admin API ─────────────────────────────────────────
	adminAPI := platform.NewAdminAPI(tenantMgr, func(change tenant.ConfigChange) {
		worker.InvalidateCache(change.TenantID)
	})
	gateway.SetAdmin(adminAPI)

	// ── 12. Register channel adapters ───────────────────────────
	if corpID := os.Getenv("WECOM_CORP_ID"); corpID != "" {
		wecomCfg := channel.WeComConfig{
			CorpID:         corpID,
			AgentID:        os.Getenv("WECOM_AGENT_ID"),
			Token:          os.Getenv("WECOM_TOKEN"),
			EncodingAESKey: os.Getenv("WECOM_ENCODING_AES_KEY"),
			AppSecret:      os.Getenv("WECOM_APP_SECRET"),
		}
		adp, err := channel.NewWeComAdapter(wecomCfg)
		if err != nil {
			log.Printf("platform: wecom adapter init failed: %v", err)
		} else {
			gateway.RegisterChannel(adp)
		}
	}

	// ── 13. Watch tenant changes and invalidate caches ─────────
	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	go func() {
		ch, err := tenantMgr.Watch(watchCtx)
		if err != nil {
			log.Printf("platform: tenant watch failed: %v", err)
			return
		}
		for change := range ch {
			log.Printf("platform: tenant change: type=%s tenant=%s", change.Type, change.TenantID)
			worker.InvalidateCache(change.TenantID)
		}
	}()

	// ── 14. Start server ───────────────────────────────────────
	go func() {
		if err := gateway.ListenAndServe(); err != nil {
			log.Fatalf("platform: server error: %v", err)
		}
	}()

	// ── 15. Wait for shutdown signal ───────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("platform: received %v, shutting down...", sig)

	shutdownCtx, cancel := context.WithTimeout(ctx, cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := gateway.Shutdown(shutdownCtx); err != nil {
		log.Printf("platform: gateway shutdown: %v", err)
	}
	if err := auditWriter.Close(); err != nil {
		log.Printf("platform: audit close: %v", err)
	}

	log.Println("platform: stopped")
}

// ─── Research Agent Adapter ─────────────────────────────────────────────

// accumulatingStreamWriter implements types.StreamWriter by accumulating
// all written content into a strings.Builder. It is safe for concurrent use.
type accumulatingStreamWriter struct {
	mu      sync.Mutex
	text    strings.Builder
	written int // number of Write calls
}

func (w *accumulatingStreamWriter) Write(evt types.StreamEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.written++
	if evt.Content != "" {
		w.text.WriteString(evt.Content)
		w.text.WriteByte('\n')
	}
	log.Printf("platform: accumulatingStreamWriter.Write #%d type=%q contentLen=%d",
		w.written, evt.Type, len(evt.Content))
	return nil
}

func (w *accumulatingStreamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.text.String())
}

// researchAgentAdapter wraps a research GraphAgent and injects dynamic
// state (query, tenantID, sessionID) at runtime. It also replaces the
// NopStreamWriter with an accumulating writer and injects the collected
// output into the event stream after the GraphAgent completes.
type researchAgentAdapter struct {
	inner     agent.Agent // the *graphagent.GraphAgent
	maxRounds int
}

// Run implements agent.Agent.
func (a *researchAgentAdapter) Run(ctx context.Context, inv *agent.Invocation) (<-chan *event.Event, error) {
	query := inv.Message.Content

	tenantID := ""
	if tid, ok := tenant.TenantIDFrom(ctx); ok {
		tenantID = tid
	}

	sessionID := ""
	if inv.Session != nil {
		sessionID = inv.Session.ID
	}

	if inv.RunOptions.RuntimeState == nil {
		inv.RunOptions.RuntimeState = make(map[string]any)
	}

	sw := &accumulatingStreamWriter{}

	inv.RunOptions.RuntimeState[types.StateKeyQuery] = query
	inv.RunOptions.RuntimeState[types.StateKeyTenantID] = tenantID
	inv.RunOptions.RuntimeState[types.StateKeySessionID] = sessionID
	inv.RunOptions.RuntimeState[types.StateKeyMaxRounds] = a.maxRounds
	inv.RunOptions.RuntimeState[types.StateKeyStreamWriter] = sw
	inv.RunOptions.RuntimeState[types.StateKeyAllowedTools] = []string{"web_search", "web_fetch"}

	log.Printf("platform: research adapter injecting state (query=%q, tenant=%q, session=%q)",
		types.TruncateForLog(query, 80), tenantID, sessionID)

	innerCh, err := a.inner.Run(ctx, inv)
	if err != nil {
		return nil, err
	}

	// Wrap the event channel: pass through all events from the GraphAgent,
	// then inject the accumulated StreamWriter text after the channel closes.
	out := make(chan *event.Event, 64)
	go func() {
		defer func() {
			log.Printf("platform: research adapter goroutine exiting, closing out")
			close(out)
		}()
		fwdCount := 0
		for evt := range innerCh {
			fwdCount++
			hasResp := evt != nil && evt.Response != nil
			nChoices := -1
			if hasResp {
				nChoices = len(evt.Response.Choices)
				if evt.Response.Error != nil {
					log.Printf("platform: research adapter ERROR: code=%q message=%q",
						evt.Response.Error.Code, evt.Response.Error.Message)
				}
			}
			log.Printf("platform: research adapter fwd#%d hasResp=%v choices=%d comp=%v",
				fwdCount, hasResp, nChoices, evt != nil && evt.IsRunnerCompletion())
			out <- evt
		}
		log.Printf("platform: research adapter innerCh closed after %d events, sw(writes=%d textLen=%d)",
			fwdCount, sw.written, len(sw.String()))
		text := sw.String()
		if text != "" {
			textEvt := &event.Event{
				Response: &model.Response{
					Choices: []model.Choice{{
						Message: model.Message{Content: text},
					}},
				},
			}
			log.Printf("platform: research adapter SENDING text event (len=%d)", len(text))
			out <- textEvt
			log.Printf("platform: research adapter text event SENT")
		}
		complEvt := &event.Event{
			Response: &model.Response{
				Done:   true,
				Object: model.ObjectTypeRunnerCompletion,
			},
		}
		log.Printf("platform: research adapter SENDING completion event")
		out <- complEvt
		log.Printf("platform: research adapter completion event SENT")
	}()

	return out, nil
}

// Tools delegates to the inner GraphAgent.
func (a *researchAgentAdapter) Tools() []tool.Tool {
	return a.inner.Tools()
}

// Info returns the research agent metadata.
func (a *researchAgentAdapter) Info() agent.Info {
	return agent.Info{
		Name:        "research-agent",
		Description: "Research Agent — 3-node graph (Clarify → Investigate → Synthesize) with web search and web fetch tools",
	}
}

// SubAgents delegates to the inner GraphAgent.
func (a *researchAgentAdapter) SubAgents() []agent.Agent {
	return a.inner.SubAgents()
}

// FindSubAgent delegates to the inner GraphAgent.
func (a *researchAgentAdapter) FindSubAgent(name string) agent.Agent {
	return a.inner.FindSubAgent(name)
}

// ─── Tenant config conversion ───────────────────────────────────────────

func tenantConfigToTenant(tc platform.TenantConfig) tenant.Tenant {
	t := tenant.Tenant{
		ID:     tc.ID,
		Name:   tc.Name,
		Status: tenant.StatusActive,
		ModelConfig: tenant.ModelConfig{
			ModelName: tc.ModelName,
		},
		ToolPermissions: tenant.ToolPermissions{
			Mode:  tc.ToolMode,
			Tools: tc.AllowedTools,
		},
		DataBackendConfig: tenant.DataBackendConfig{
			Type: tenant.BackendType(tc.BackendType),
			DSN:  tc.BackendDSN,
		},
		AuditPolicy: tenant.AuditPolicy{
			Level: tenant.AuditLevel(tc.AuditLevel),
		},
		RateLimits: tenant.RateLimits{
			RequestsPerSecond: tc.RateLimitRPS,
		},
	}
	if tc.ChannelType != "" {
		t.IMChannelConfigs = []tenant.IMChannelConfig{
			{
				ChannelType: tc.ChannelType,
				CorpID:      tc.ChannelCorpID,
				Enabled:     true,
			},
		}
	}
	return t
}
