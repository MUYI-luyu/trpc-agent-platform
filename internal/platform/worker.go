package platform

import (
	"context"
	"fmt"
	"log"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/audit"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/backend"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/filter"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/telemetry"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/tenant"
)

// Worker orchestrates the execution of an Agent for a single request.
// It is stateless — any Worker can handle any tenant's requests.
//
// The Worker's job:
//  1. Load tenant configuration
//  2. Resolve the DataBackend (session + memory)
//  3. Build the Filter Chain
//  4. Create/Lookup the Runner
//  5. Execute the Agent
//  6. Apply post-processing filters & audit
type Worker struct {
	// tenantMgr provides tenant configuration.
	tenantMgr tenant.Manager

	// backendFactory creates DataBackend instances per tenant.
	backendFactory *backend.Factory

	// auditWriter receives audit log entries (nil if audit is disabled).
	auditWriter audit.Writer

	// agentProvider is a function that returns the Agent instance for a
	// given tenant. In production this would be a registry.
	agentProvider AgentProvider

	// runnerCache stores per-tenant runners keyed by tenant ID.
	runnerCache map[string]runner.Runner
	cacheMu     sync.RWMutex
}

// AgentProvider returns an Agent instance for a given tenant.
// The Agent is typically a GraphAgent built from a StateGraph.
type AgentProvider func(ctx context.Context, t *tenant.Tenant) (agent.Agent, error)

// NewWorker creates a new Worker.
func NewWorker(tm tenant.Manager, bf *backend.Factory, aw audit.Writer, ap AgentProvider) *Worker {
	return &Worker{
		tenantMgr:      tm,
		backendFactory: bf,
		auditWriter:    aw,
		agentProvider:  ap,
		runnerCache:    make(map[string]runner.Runner),
	}
}

// Run executes an agent request for a given tenant/user/session. It
// returns the event channel from the Runner.
//
// tenantID may be empty for single-tenant mode.
func (w *Worker) Run(ctx context.Context, tenantID, userID, sessionID string, msg model.Message) (<-chan *event.Event, error) {
	// ── 1. Load tenant config ───────────────────────────────────
	var t *tenant.Tenant
	if tenantID != "" {
		t2, err := w.tenantMgr.Get(ctx, tenantID)
		if err != nil {
			// Tenant not found — fall back to single-tenant mode.
			log.Printf("worker: tenant %q not found, falling back to single-tenant: %v", tenantID, err)
			tenantID = ""
		} else if !t2.IsActive() {
			return nil, fmt.Errorf("worker: tenant %q is %s", tenantID, t2.Status)
		} else {
			t = t2
		}
	}

	// ── 2. Resolve backend ──────────────────────────────────────
	be := w.resolveBackend(t)

	// ── 3. Build filter chain ───────────────────────────────────
	chain := filter.BuildTenantChain(t)

	// ── 4. Inject tenant ID into context ────────────────────────
	if tenantID != "" {
		ctx = tenant.WithTenantID(ctx, tenantID)
	}

	// ── 5. OTel: start worker span ──────────────────────────────
	ctx, span := telemetry.SpanWorker(ctx, tenantID, sessionID, "platform-agent")
	defer span.End()

	// ── 6. Execute BeforeRequest filters ────────────────────────
	var err error
	ctx, err = chain.ExecuteBeforeRequest(ctx, msg)
	if err != nil {
		telemetry.RecordError(ctx, err)
		w.audit(ctx, tenantID, "", userID, sessionID, "platform-agent", "", audit.DecisionDeny, 0, err)
		return nil, fmt.Errorf("worker: before request: %w", err)
	}

	// ── 7. Get or create Runner ─────────────────────────────────
	r, err := w.getOrCreateRunner(ctx, t, be, chain)
	if err != nil {
		telemetry.RecordError(ctx, err)
		w.audit(ctx, tenantID, "", userID, sessionID, "platform-agent", "", audit.DecisionError, 0, err)
		return nil, fmt.Errorf("worker: create runner: %w", err)
	}

	// ── 8. Execute ──────────────────────────────────────────────
	scopedAppName := tenant.BuildTenantAppName(tenantID, "platform")
	eventCh, err := r.Run(ctx, userID, sessionID, msg,
		agent.WithRuntimeState(map[string]any{
			"app_name": scopedAppName,
		}),
	)
	if err != nil {
		telemetry.RecordError(ctx, err)
		w.audit(ctx, tenantID, "", userID, sessionID, "platform-agent", "", audit.DecisionError, 0, err)
		return nil, fmt.Errorf("worker: run: %w", err)
	}

	// Wrap the event channel to apply OnEvent filters.
	return w.wrapEvents(ctx, chain, eventCh, tenantID, userID, sessionID), nil
}

// wrapEvents applies OnEvent filters to each event in the stream and
// writes audit entries for tool calls.
func (w *Worker) wrapEvents(ctx context.Context, chain *filter.Chain, ch <-chan *event.Event, tenantID, userID, sessionID string) <-chan *event.Event {
	out := make(chan *event.Event, 64)

	go func() {
		defer close(out)

		wcount := 0
		for evt := range ch {
			wcount++
			if evt == nil {
				log.Printf("worker.wrapEvents: event#%d nil, skipping", wcount)
				continue
			}

			hasResp := evt.Response != nil
			nChoices := -1
			done := false
			if hasResp {
				nChoices = len(evt.Response.Choices)
				done = evt.Response.Done
			}
			log.Printf("worker.wrapEvents: event#%d hasResp=%v choices=%d done=%v comp=%v",
				wcount, hasResp, nChoices, done, evt.IsRunnerCompletion())

			// Apply OnEvent filters (masking, etc.).
			evt = chain.ExecuteOnEvent(ctx, evt)
			if evt == nil {
				log.Printf("worker.wrapEvents: event#%d FILTERED OUT", wcount)
				continue
			}

			// Audit tool_call events.
			if evt.Response != nil && len(evt.Response.Choices) > 0 {
				for _, choice := range evt.Response.Choices {
					for _, tc := range choice.Message.ToolCalls {
						w.audit(ctx, tenantID, "", userID, sessionID,
							"platform-agent", tc.Function.Name,
							audit.DecisionAllow, 0, nil)
					}
				}
			}

			select {
			case out <- evt:
			case <-ctx.Done():
				return
			}
		}
		log.Printf("worker.wrapEvents: input channel closed after %d events", wcount)
	}()

	return out
}

// getOrCreateRunner returns a cached runner or builds a new one.
func (w *Worker) getOrCreateRunner(ctx context.Context, t *tenant.Tenant, be backend.DataBackend, chain *filter.Chain) (runner.Runner, error) {
	tid := ""
	if t != nil {
		tid = t.ID
	}

	// Fast path: cache hit.
	w.cacheMu.RLock()
	if r, ok := w.runnerCache[tid]; ok {
		w.cacheMu.RUnlock()
		return r, nil
	}
	w.cacheMu.RUnlock()

	// Slow path: build runner.
	ag, err := w.agentProvider(ctx, t)
	if err != nil {
		return nil, err
	}

	runnerOpts := []runner.Option{
		runner.WithSessionService(be.SessionService()),
	}

	r := runner.NewRunner("platform-agent", ag, runnerOpts...)

	w.cacheMu.Lock()
	w.runnerCache[tid] = r
	w.cacheMu.Unlock()

	_ = chain // chain wrapping of model/tool is done at agent construction time

	return r, nil
}

// resolveBackend resolves the DataBackend for a tenant.
func (w *Worker) resolveBackend(t *tenant.Tenant) backend.DataBackend {
	cfg := tenant.DataBackendConfig{
		Type: tenant.BackendInMemory,
	}
	if t != nil && t.DataBackendConfig.Type != "" {
		cfg = t.DataBackendConfig
	}

	be, err := w.backendFactory.Create(cfg)
	if err != nil {
		log.Printf("worker: failed to create backend for type %q: %v — falling back to inmemory", cfg.Type, err)
		be, _ = w.backendFactory.Create(tenant.DataBackendConfig{Type: tenant.BackendInMemory})
	}
	return be
}

// audit writes an audit log entry asynchronously if audit is enabled.
func (w *Worker) audit(ctx context.Context, tenantID, channel, userID, sessionID, agentName, toolName string, decision audit.Decision, latencyMs int, err error) {
	if w.auditWriter == nil {
		return
	}

	entry := audit.Entry{
		TenantID:  tenantID,
		Channel:   channel,
		UserID:    userID,
		SessionID: sessionID,
		AgentName: agentName,
		ToolName:  toolName,
		Decision:  decision,
		LatencyMs: latencyMs,
	}
	if err != nil {
		entry.ErrorType = fmt.Sprintf("%T", err)
	}

	w.auditWriter.Log(ctx, entry)
}

// InvalidateCache removes the cached runner for a tenant. Call this
// when tenant configuration changes (via Watch).
func (w *Worker) InvalidateCache(tenantID string) {
	w.cacheMu.Lock()
	delete(w.runnerCache, tenantID)
	w.cacheMu.Unlock()
	log.Printf("worker: invalidated runner cache for tenant %q", tenantID)
}
