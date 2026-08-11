package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/channel"
	"github.com/MUYI-luyu/trpc-agent-platform/internal/telemetry"
)

// Gateway is the HTTP entry point for the Agent Platform. It handles:
//   - Webhook verification (GET) and message delivery (POST) for IM channels
//   - HTTP API requests (POST /api/v1/agents/:name)
//   - Health check (GET /health)
//   - Admin API (under /admin)
type Gateway struct {
	// worker executes agent requests.
	worker *Worker

	// admin provides tenant management endpoints.
	admin *AdminAPI

	// adapters maps channel type to its adapter instance.
	adapters map[string]channel.ChannelAdapter

	// server is the underlying HTTP server.
	server *http.Server

	// config holds the server configuration.
	config ServerConfig
}

// NewGateway creates a new Gateway.
func NewGateway(cfg ServerConfig, w *Worker) *Gateway {
	g := &Gateway{
		worker:   w,
		adapters: make(map[string]channel.ChannelAdapter),
		config:   cfg,
	}
	return g
}

// SetAdmin attaches the Admin API handler to this gateway.
func (g *Gateway) SetAdmin(admin *AdminAPI) {
	g.admin = admin
}

// RegisterChannel adds a ChannelAdapter for a specific channel type.
func (g *Gateway) RegisterChannel(adp channel.ChannelAdapter) {
	g.adapters[adp.Type()] = adp
	log.Printf("gateway: registered channel %q", adp.Type())
}

// ListenAndServe starts the HTTP server and blocks until it returns.
func (g *Gateway) ListenAndServe() error {
	mux := http.NewServeMux()

	// Health check.
	mux.HandleFunc("/health", g.handleHealth)

	// Webhook endpoints for IM channels.
	// /webhook → default (wecom), /webhook/wecom → explicit
	mux.HandleFunc("/webhook", g.handleWebhook)
	mux.HandleFunc("/webhook/", g.handleWebhook)

	// HTTP API for direct agent calls.
	mux.HandleFunc("/api/v1/", g.handleAPI)

	// Admin API — delegate to AdminAPI if configured.
	if g.admin != nil {
		g.admin.RegisterRoutes(mux)
	}

	g.server = &http.Server{
		Addr:         g.config.Addr,
		Handler:      withLogging(mux),
		ReadTimeout:  g.config.ReadTimeout,
		WriteTimeout: g.config.WriteTimeout,
	}

	log.Printf("gateway: listening on %s", g.config.Addr)
	return g.server.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (g *Gateway) Shutdown(ctx context.Context) error {
	return g.server.Shutdown(ctx)
}

// ─── Handlers ────────────────────────────────────────────────────────

func (g *Gateway) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// handleWebhook routes webhook requests to the correct channel adapter.
//
// URL patterns:
//
//	/webhook/wecom           → WeChat Work (single-tenant)
//	/webhook/wecom/:tenantID → WeChat Work (multi-tenant)
func (g *Gateway) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Extract channel type from URL: /webhook/<channel>[/...]
	// /webhook → default to first registered adapter
	path := strings.TrimPrefix(r.URL.Path, "/webhook")
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	channelType := parts[0]

	// Default to wecom if no channel specified.
	if channelType == "" {
		if adp, ok := g.adapters["wecom"]; ok {
			channelType = "wecom"
			_ = adp
		}
	}

	adp, ok := g.adapters[channelType]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown channel: %s", channelType), http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// URL verification (WeChat Work).
		if wc, ok := adp.(*channel.WeComAdapter); ok {
			g.handleWeComVerify(w, r, wc)
			return
		}
		http.Error(w, "GET not supported for this channel", http.StatusMethodNotAllowed)

	case http.MethodPost:
		g.handleWebhookPost(w, r, adp, channelType)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWebhookPost processes an incoming IM message.
func (g *Gateway) handleWebhookPost(w http.ResponseWriter, r *http.Request, adp channel.ChannelAdapter, channelType string) {
	ctx := r.Context()

	// ── OTel: Gateway span ─────────────────────────────────────
	tenantID := extractTenantFromPath(r.URL.Path, channelType)
	ctx, span := telemetry.SpanGateway(ctx, channelType, tenantID)
	defer span.End()

	// ── Read body ──────────────────────────────────────────────
	body, err := readBodyLimit(r, 1<<20) // 1 MB
	if err != nil {
		telemetry.RecordError(ctx, err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// ── Verify signature ───────────────────────────────────────
	// WeChat Work sends sig params as query string, not headers.
	// Inject them so the adapter can read uniformly.
	for key, vals := range r.URL.Query() {
		switch key {
		case "msg_signature":
			r.Header.Set("X-WX-Signature", vals[0])
		case "timestamp":
			r.Header.Set("X-WX-Timestamp", vals[0])
		case "nonce":
			r.Header.Set("X-WX-Nonce", vals[0])
		}
	}
	if err := adp.VerifySignature(body, r.Header); err != nil {
		telemetry.RecordError(ctx, err)
		log.Printf("gateway: signature verification failed for channel %q: %v", channelType, err)
		http.Error(w, "signature verification failed", http.StatusForbidden)
		return
	}

	// ── Decrypt ────────────────────────────────────────────────
	plain, err := adp.Decrypt(body)
	if err != nil {
		telemetry.RecordError(ctx, err)
		log.Printf("gateway: decrypt failed for channel %q: %v", channelType, err)
		http.Error(w, "decrypt failed", http.StatusBadRequest)
		return
	}

	// ── Parse message ──────────────────────────────────────────
	msg, err := adp.ParseMessage(plain)
	if err != nil {
		telemetry.RecordError(ctx, err)
		log.Printf("gateway: parse message failed for channel %q: %v", channelType, err)
		http.Error(w, "parse failed", http.StatusBadRequest)
		return
	}

	// ── Identify tenant ────────────────────────────────────────
	if tid, err := adp.IdentifyTenant(r); err == nil {
		msg.TenantID = tid
	}
	if msg.TenantID == "" {
		msg.TenantID = tenantID
	}

	// ── Route to Worker ────────────────────────────────────────
	userID := channel.GenerateUserID(msg)
	sessionID := channel.GenerateSessionID(msg)
	userMsg := model.NewUserMessage(msg.Text)

	// Use a detached context so the LLM call survives past the HTTP response.
	runCtx := context.Background()
	eventCh, err := g.worker.Run(runCtx, msg.TenantID, userID, sessionID, userMsg)
	if err != nil {
		telemetry.RecordError(ctx, err)
		log.Printf("gateway: worker run failed: %v", err)
		http.Error(w, "agent execution failed", http.StatusInternalServerError)
		return
	}

	// ── Collect response, then reply via channel API ───────────
	// WeChat Work webhooks must return 200 within 5s, so we defer
	// the actual LLM reply to a goroutine. The full response is
	// collected and sent as a single message.
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))

	go func() {
		// Use a detached context so the reply isn't cancelled when
		// the HTTP request completes.
		bgCtx := context.Background()
		var fullText strings.Builder
		eventCount := 0
		for evt := range eventCh {
			eventCount++
			if evt == nil {
				log.Printf("gateway: event#%d nil event, breaking", eventCount)
				continue
			}
			if evt.Response == nil {
				log.Printf("gateway: event#%d Response=nil author=%q object=%q done=%v",
					eventCount, evt.Author, evt.Object, evt.Done)
				if evt.IsRunnerCompletion() {
					break
				}
				continue
			}
			for _, choice := range evt.Response.Choices {
				if evt.Response.IsPartial {
					fullText.WriteString(choice.Delta.Content)
				} else {
					fullText.WriteString(choice.Message.Content)
				}
			}
			log.Printf("gateway: event#%d choices=%d partial=%v contentLen=%d",
				eventCount, len(evt.Response.Choices), evt.Response.IsPartial, fullText.Len())
			if evt.IsRunnerCompletion() {
				break
			}
		}

		text := strings.TrimSpace(fullText.String())
		if text == "" {
			log.Printf("gateway: empty response for wecom msg %q", msg.MsgID)
			return
		}

		msg.Text = text
		if err := adp.SendMessage(bgCtx, msg); err != nil {
			log.Printf("gateway: wecom send failed: %v", err)
		} else {
			log.Printf("gateway: wecom reply sent (len=%d, to=%s)", len(text), msg.ChannelUserID)
		}
	}()
}

// handleWeComVerify handles the GET verification request from WeChat Work.
func (g *Gateway) handleWeComVerify(w http.ResponseWriter, r *http.Request, adp *channel.WeComAdapter) {
	sig := r.URL.Query().Get("msg_signature")
	timestamp := r.URL.Query().Get("timestamp")
	nonce := r.URL.Query().Get("nonce")
	echostr := r.URL.Query().Get("echostr")

	if sig == "" || timestamp == "" || nonce == "" || echostr == "" {
		http.Error(w, "missing query parameters", http.StatusBadRequest)
		return
	}

	plain, err := adp.VerifyURL(sig, timestamp, nonce, echostr)
	if err != nil {
		log.Printf("gateway: wecom URL verification failed: %v", err)
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(plain))
}

// handleAPI handles direct HTTP API calls to agents.
//
//	POST /api/v1/agents/:name
//	Body: {"query": "...", "tenant_id": "...", "session_id": "..."}
func (g *Gateway) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract agent name from URL: /api/v1/agents/<name>
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	agentName := strings.Trim(path, "/")
	if agentName == "" {
		agentName = "research-agent"
	}

	var req struct {
		Query     string `json:"query"`
		TenantID  string `json:"tenant_id"`
		SessionID string `json:"session_id"`
	}
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
		req.SessionID = fmt.Sprintf("api_%d", time.Now().UnixNano())
	}

	ctx := r.Context()
	userMsg := model.NewUserMessage(req.Query)
	eventCh, err := g.worker.Run(ctx, req.TenantID, req.TenantID, req.SessionID, userMsg)
	if err != nil {
		http.Error(w, fmt.Sprintf("agent execution failed: %v", err), http.StatusInternalServerError)
		return
	}

	// SSE streaming.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	_ = agentName // used in future for agent routing

	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}

		// Send text deltas as SSE.
		if len(evt.Response.Choices) > 0 {
			choice := evt.Response.Choices[0]
			content := ""
			if evt.Response.IsPartial {
				content = choice.Delta.Content
			} else {
				content = choice.Message.Content
			}
			if content != "" {
				data, _ := json.Marshal(map[string]string{"content": content})
				fmt.Fprintf(w, "event: content\ndata: %s\n\n", data)
				flusher.Flush()
			}
		}

		// Check errors.
		if evt.Response.Error != nil {
			data, _ := json.Marshal(map[string]string{"message": evt.Response.Error.Message})
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
			flusher.Flush()
		}

		if evt.IsRunnerCompletion() {
			break
		}
	}

	fmt.Fprintf(w, "event: done\ndata: %s\n\n", `{"content":"complete"}`)
	flusher.Flush()
}

// ─── Helpers ─────────────────────────────────────────────────────────

// extractTenantFromPath extracts a tenant ID from the webhook URL.
//
//	/webhook/wecom/tenant_001 → "tenant_001"
//	/webhook/wecom            → ""
func extractTenantFromPath(urlPath, channelType string) string {
	prefix := "/webhook/" + channelType + "/"
	rest := strings.TrimPrefix(urlPath, prefix)
	if rest == urlPath || rest == "" {
		return ""
	}
	return strings.SplitN(rest, "/", 2)[0]
}

// readBodyLimit reads the request body with a size limit.
// Uses io.ReadAll on an io.LimitReader to guarantee complete reads.
func readBodyLimit(r *http.Request, maxBytes int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxBytes))
}

// translateEventsToStream converts Runner events to StreamChunks for
// the IM channel adapter.
func translateEventsToStream(eventCh <-chan *event.Event, streamCh chan<- *channel.StreamChunk) {
	defer close(streamCh)

	first := true
	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}

		resp := evt.Response
		chunk := &channel.StreamChunk{IsFirst: first}
		first = false

		if len(resp.Choices) > 0 {
			choice := resp.Choices[0]
			if resp.IsPartial {
				chunk.Content = choice.Delta.Content
			} else {
				chunk.Content = choice.Message.Content
				chunk.IsFinal = true
			}
		}

		if resp.Error != nil {
			chunk.IsFinal = true
		}

		// Check for tool calls.
		if len(resp.Choices) > 0 {
			for _, tc := range resp.Choices[0].Message.ToolCalls {
				chunk.ToolCall = &channel.ToolCallStatus{
					Name:   tc.Function.Name,
					Status: "done",
				}
			}
		}

		streamCh <- chunk

		if chunk.IsFinal {
			return
		}
	}
}

// withLogging wraps an HTTP handler with request logging.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("http: %s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
