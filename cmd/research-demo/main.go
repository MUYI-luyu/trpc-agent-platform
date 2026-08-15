// Research Agent Demo
//
// A minimal HTTP server that runs the 3-Node Research Agent Graph with real
// LLM streaming via SSE.
//
// # Quick start (with Tavily search — recommended)
//
//	export DEEPSEEK_API_KEY="sk-..."
//	export TAVILY_API_KEY="tvly-..."       # free tier: https://tavily.com
//	export SEARCH_BACKEND=tavily
//	go run ./cmd/research-demo/
//
// # With Bing API
//
//	export DEEPSEEK_API_KEY="sk-..."
//	export BING_API_KEY="..."             # Azure/Bing Search API key
//	export SEARCH_BACKEND=bing
//	go run ./cmd/research-demo/
//
// # Without search API (unreliable web scraping fallback)
//
//	export DEEPSEEK_API_KEY="sk-..."
//	go run ./cmd/research-demo/
//
// # Test
//
//	curl -X POST http://localhost:8080/research \
//	  -H "Content-Type: application/json" \
//	  -d '{"query":"Raft是什么"}'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/graph"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/infra"
)

// ─── Environment variable configuration ───────────────────────────────────
//
//	DEEPSEEK_API_KEY   — required for real LLM mode
//	DEEPSEEK_BASE_URL  — optional override, defaults from trpc-agent-go
//	DEEPSEEK_MODEL     — defaults to deepseek-v4-flash
//	PORT               — HTTP listen port, defaults to 8080

func main() {
	cfg := loadConfig()

	// Create the Research Service.
	svcCfg := research.ServiceConfig{
		MaxRounds: 3,
	}

	if cfg.model != nil {
		svcCfg.Model = cfg.model
		// Wire search backend from environment variables.
		// Only register tools that have actual backends.
		searchCfg := infra.LoadSearchBackendConfig()
		searchFn := infra.NewWebSearchFunc(searchCfg)
		log.Printf("[search] backend=%q tavily_key=%s bing_key=%s search_fn=%v",
			searchCfg.Backend,
			map[bool]string{true: "set", false: "not set"}[searchCfg.TavilyKey != ""],
			map[bool]string{true: "set", false: "not set"}[searchCfg.BingKey != ""],
			searchFn != nil)
		deps := &infra.ToolDeps{
			WebFetchFn:  infra.DefaultWebFetch,
			// SearchKBFn stays nil (not registered without a KB backend).
		}
		if searchFn != nil {
			deps.WebSearchFn = searchFn
			log.Printf("Using real LLM: %s (search: %s)", cfg.modelName, searchCfg.Backend)
		} else {
			log.Printf("Using real LLM: %s (search: off — set SEARCH_BACKEND=bing|searxng|builtin)", cfg.modelName)
		}
		svcCfg.Tools = infra.NewTools(deps)
	} else {
		log.Println("No DEEPSEEK_API_KEY set — using heuristic classifier (no LLM)")
	}

	svc, err := research.NewService(svcCfg)
	if err != nil {
		log.Fatalf("create research service: %v", err)
	}
	defer func() {
		if err := svc.Close(); err != nil {
			log.Printf("close service: %v", err)
		}
	}()

	// HTTP server.
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/research", handleResearch(svc, cfg.model != nil))

	addr := fmt.Sprintf(":%s", cfg.port)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("Research Agent Demo listening on http://localhost%s", addr)
		log.Printf("  POST /research  —  submit a research query (SSE streaming)")
		log.Printf("  GET  /health    —  health check")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

// ─── Config ───────────────────────────────────────────────────────────────

type demoConfig struct {
	apiKey    string
	baseURL   string
	modelName string
	model     model.Model
	port      string
}

func loadConfig() demoConfig {
	cfg := demoConfig{
		apiKey:    os.Getenv("DEEPSEEK_API_KEY"),
		baseURL:   os.Getenv("DEEPSEEK_BASE_URL"),
		modelName: envOrDefault("DEEPSEEK_MODEL", "deepseek-v4-flash"),
		port:      envOrDefault("PORT", "8080"),
	}

	if cfg.apiKey != "" {
		// Always use the OpenAI-compatible endpoint, not the Anthropic one.
		// The openai-go SDK sends OpenAI-format requests (e.g. /chat/completions)
		// which only work with the /v1 base path.
		baseURL := cfg.baseURL
		if baseURL == "" || strings.Contains(baseURL, "/anthropic") {
			baseURL = "https://api.deepseek.com/v1"
		}
		opts := []openai.Option{
			openai.WithAPIKey(cfg.apiKey),
			openai.WithChannelBufferSize(256),
			openai.WithBaseURL(baseURL),
			// VariantDeepSeek converts StructuredOutput to json_object format
			// (DeepSeek does not support OpenAI's native json_schema format).
			openai.WithVariant(openai.VariantDeepSeek),
		}
		cfg.model = openai.New(cfg.modelName, opts...)
	}

	return cfg
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ─── HTTP handler ─────────────────────────────────────────────────────────

type researchRequest struct {
	Query     string `json:"query"`
	TenantID  string `json:"tenant_id"`
	SessionID string `json:"session_id"`
}

func handleResearch(svc *research.Service, hasModel bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req researchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
			return
		}
		if req.Query == "" {
			http.Error(w, "query is required", http.StatusBadRequest)
			return
		}
		if req.TenantID == "" {
			req.TenantID = "default"
		}
		if req.SessionID == "" {
			req.SessionID = fmt.Sprintf("session_%d", time.Now().UnixNano())
		}

		log.Printf("[demo] query=%q tenant=%s session=%s model=%v",
			req.Query, req.TenantID, req.SessionID, hasModel)

		// SSE headers.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// Create a SafeStreamWriter for per-request progress events.
		sw, err := infra.NewSafeStreamWriter(w, r.Context())
		if err != nil {
			http.Error(w, "stream setup failed", http.StatusInternalServerError)
			return
		}

		// Send initial event.
		sendSSEvent(w, flusher, "start", fmt.Sprintf("Processing: %s", req.Query))

		if hasModel {
			// ── Real LLM path: execute the Graph via Service ──────────
			handleRealPath(r.Context(), svc, req, sw, w, flusher)
		} else {
			// ── Heuristic path: simulate Graph for demo purposes ──────
			handleHeuristicPath(req, sw, w, flusher)
		}

		// Done.
		sendSSEvent(w, flusher, "done", "研究完成")
		log.Printf("[demo] completed query=%q", req.Query)
	}
}

// ─── Real LLM path ────────────────────────────────────────────────────────

func handleRealPath(ctx context.Context, svc *research.Service, req researchRequest,
	sw *infra.SafeStreamWriter, w http.ResponseWriter, flusher http.Flusher) {

	// Execute the graph. The service returns a channel of framework events.
	eventCh, err := svc.RunStreaming(ctx, req.Query, req.TenantID, req.SessionID, sw)
	if err != nil {
		sendSSError(w, flusher, fmt.Sprintf("execution error: %v", err))
		return
	}

	// Consume the event channel and translate to SSE.
	for evt := range eventCh {
		if evt == nil {
			continue
		}

		// Emit text deltas from the model as SSE content events.
		if evt.Response != nil && len(evt.Response.Choices) > 0 {
			choice := evt.Response.Choices[0]

			// Streaming delta from a node (Clarify answer or Synthesize report).
			if evt.Response.IsPartial && choice.Delta.Content != "" {
				sendSSEvent(w, flusher, "content",
					choice.Delta.Content,
				)
			}

			// Full message (non-streaming or final).
			if !evt.Response.IsPartial && choice.Message.Content != "" {
				sendSSEvent(w, flusher, "content",
					choice.Message.Content,
				)
			}
		}

		// Check for errors.
		if evt.Response != nil && evt.Response.Error != nil {
			sendSSError(w, flusher, evt.Response.Error.Message)
		}

		// Runner completion signals the end of graph execution.
		if evt.IsRunnerCompletion() {
			break
		}
	}
}

// ─── Heuristic path (no LLM) ──────────────────────────────────────────────

func handleHeuristicPath(req researchRequest, sw types.StreamWriter,
	w http.ResponseWriter, flusher http.Flusher) {

	action := graph.ClassifyQuery(req.Query)

	_ = sw.Write(types.NewStreamEvent("clarify_result", "clarify",
		fmt.Sprintf("Action: %s (heuristic)", action)))

	switch action {
	case types.ActionReject:
		sendSSEvent(w, flusher, "content",
			"抱歉，这个问题不在我的研究范围内。我专注于分布式系统、数据库和tRPC框架相关的技术问题。")

	case types.ActionAnswer:
		sendSSEvent(w, flusher, "content",
			fmt.Sprintf("关于「%s」：当前为启发式模式（未配置 DEEPSEEK_API_KEY）。设置环境变量后，Clarify Node 会用 LLM 生成完整答案。", req.Query))

	case types.ActionResearch:
		// Simulate Investigate progress.
		for round := 1; round <= 2; round++ {
			_ = sw.Write(types.NewStreamEvent(types.EventToolStart, "investigate",
				fmt.Sprintf("第%d轮搜索：正在搜索知识库...", round)))
			time.Sleep(150 * time.Millisecond)

			_ = sw.Write(types.NewStreamEvent(types.EventToolEnd, "investigate",
				fmt.Sprintf("第%d轮搜索：找到相关文档", round)))

			_ = sw.Write(types.NewStreamEvent(types.EventProgress, "investigate",
				fmt.Sprintf("第%d轮进度：已确认关键发现", round)))
		}

		// Simulate Synthesize report.
		sendSSEvent(w, flusher, "content",
			fmt.Sprintf("# 研究报告：%s\n\n## 摘要\n当前为启发式模式（未配置 DEEPSEEK_API_KEY）。\n\n设置环境变量后，Investigate Node 会执行真实的 ReAct 搜索循环，Synthesize Node 会基于搜索结果生成报告。\n\n## 如何启用真实模式\n```bash\nexport DEEPSEEK_API_KEY=\"sk-your-key-here\"\ngo run ./cmd/research-demo/\n```", req.Query))
	}
}

// ─── SSE helpers ──────────────────────────────────────────────────────────

func sendSSEvent(w http.ResponseWriter, flusher http.Flusher, eventType, content string) {
	data, _ := json.Marshal(map[string]string{
		"content": content,
	})
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	flusher.Flush()
}

func sendSSError(w http.ResponseWriter, flusher http.Flusher, message string) {
	data, _ := json.Marshal(map[string]string{
		"message": message,
	})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	flusher.Flush()
}

// compile-time: ensure model types are used
var _ = (*model.Request)(nil)
