package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
)

// ─── search_kb ───────────────────────────────────────────────────────────

// SearchKBInput is the input schema for the search_kb tool.
type SearchKBInput struct {
	Query string `json:"query" jsonschema:"description=Search query for the knowledge base"`
}

// SearchKBOutput is the output schema for the search_kb tool.
type SearchKBOutput struct {
	Documents []SearchKBDocument `json:"documents"`
}

// SearchKBDocument represents a single document fragment from the KB.
type SearchKBDocument struct {
	Title   string  `json:"title"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// ─── web_search ──────────────────────────────────────────────────────────

// WebSearchInput is the input schema for the web_search tool.
type WebSearchInput struct {
	Query string `json:"query" jsonschema:"description=Search query for the internet"`
}

// WebSearchOutput is the output schema for the web_search tool.
type WebSearchOutput struct {
	Results []WebSearchResult `json:"results"`
}

// WebSearchResult represents a single search result from the web.
type WebSearchResult struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	URL     string `json:"url"`
}

// ─── web_fetch ───────────────────────────────────────────────────────────

// WebFetchInput is the input schema for the web_fetch tool.
type WebFetchInput struct {
	URL string `json:"url" jsonschema:"description=The URL of the web page to fetch"`
}

// WebFetchOutput is the output schema for the web_fetch tool.
type WebFetchOutput struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// ─── Tool construction ───────────────────────────────────────────────────

// ToolDeps provides the dependencies needed to construct research tools.
type ToolDeps struct {
	// SearchKBFn is the backing implementation for search_kb.
	// If nil, a no-op implementation is used (returns empty results).
	SearchKBFn func(ctx context.Context, query string) ([]SearchKBDocument, error)
	// WebSearchFn is the backing implementation for web_search.
	WebSearchFn func(ctx context.Context, query string) ([]WebSearchResult, error)
	// WebFetchFn is the backing implementation for web_fetch.
	WebFetchFn func(ctx context.Context, url string) (*WebFetchOutput, error)
}

// NewTools creates research tools from dependencies. Tools with nil
// backing functions are omitted (not registered), so the LLM won't
// try to call them.
func NewTools(deps *ToolDeps) map[string]tool.Tool {
	if deps == nil {
		deps = &ToolDeps{}
	}
	tools := make(map[string]tool.Tool)
	if deps.SearchKBFn != nil {
		tools["search_kb"] = newSearchKBTool(deps.SearchKBFn)
	}
	if deps.WebSearchFn != nil {
		tools["web_search"] = newWebSearchTool(deps.WebSearchFn)
	}
	if deps.WebFetchFn != nil {
		tools["web_fetch"] = newWebFetchTool(deps.WebFetchFn)
	}
	return tools
}

// newSearchKBTool creates the search_kb tool.
func newSearchKBTool(fn func(ctx context.Context, query string) ([]SearchKBDocument, error)) tool.CallableTool {
	if fn == nil {
		fn = func(_ context.Context, _ string) ([]SearchKBDocument, error) {
			return nil, fmt.Errorf("search_kb: no knowledge base backend configured")
		}
	}
	wrapped := func(ctx context.Context, in SearchKBInput) (SearchKBOutput, error) {
		docs, err := fn(ctx, in.Query)
		if err != nil {
			return SearchKBOutput{}, err
		}
		// Apply truncation to each document's content.
		for i := range docs {
			docs[i].Content = EnforceTruncation("search_kb", docs[i].Content)
		}
		return SearchKBOutput{Documents: docs}, nil
	}
	return function.NewFunctionTool(wrapped,
		function.WithName("search_kb"),
		function.WithDescription(types.ToolDescSearchKB),
	)
}

// newWebSearchTool creates the web_search tool.
func newWebSearchTool(fn func(ctx context.Context, query string) ([]WebSearchResult, error)) tool.CallableTool {
	if fn == nil {
		fn = func(_ context.Context, _ string) ([]WebSearchResult, error) {
			return []WebSearchResult{}, nil
		}
	}
	wrapped := func(ctx context.Context, in WebSearchInput) (WebSearchOutput, error) {
		results, err := fn(ctx, in.Query)
		if err != nil {
			log.Printf("[web_search] query=%q ERROR: %v", in.Query, err)
			return WebSearchOutput{}, err
		}
		// Apply truncation to each snippet.
		for i := range results {
			results[i].Snippet = EnforceTruncation("web_search", results[i].Snippet)
		}
		log.Printf("[web_search] query=%q returned %d results", in.Query, len(results))
		for i, r := range results {
			snippet := r.Snippet
			if len(snippet) > 120 {
				snippet = snippet[:120] + "..."
			}
			log.Printf("[web_search]   [%d] title=%q url=%q snippet=%q", i, r.Title, r.URL, snippet)
		}
		return WebSearchOutput{Results: results}, nil
	}
	return function.NewFunctionTool(wrapped,
		function.WithName("web_search"),
		function.WithDescription(types.ToolDescWebSearch),
	)
}

// newWebFetchTool creates the web_fetch tool.
func newWebFetchTool(fn func(ctx context.Context, url string) (*WebFetchOutput, error)) tool.CallableTool {
	if fn == nil {
		fn = func(_ context.Context, _ string) (*WebFetchOutput, error) {
			return &WebFetchOutput{Title: "", Content: ""}, nil
		}
	}
	wrapped := func(ctx context.Context, in WebFetchInput) (WebFetchOutput, error) {
		out, err := fn(ctx, in.URL)
		if err != nil {
			return WebFetchOutput{}, err
		}
		// Apply head+tail truncation for web pages:
		// Keep first ~75% and last ~25% of the truncated content.
		if out != nil {
			out.Content = enforceWebFetchTruncation(out.Content)
		}
		return *out, nil
	}
	return function.NewFunctionTool(wrapped,
		function.WithName("web_fetch"),
		function.WithDescription(types.ToolDescWebFetch),
	)
}

// enforceWebFetchTruncation applies head+tail truncation for web pages.
// Keeps the first 2250 tokens worth + last 750 tokens worth, roughly matching
// the design spec of "前 1500 tokens + 尾 500 tokens" with CJK accounting.
func enforceWebFetchTruncation(content string) string {
	limit := TruncLimitWebFetch
	if EstimateTokens(content) <= limit {
		return content
	}
	// Keep first ~75% of limit and last ~25% of limit.
	headTokens := limit * 3 / 4 // ~2250 tokens
	tailTokens := limit / 4     // ~750 tokens

	headChars := headTokens * 2
	tailChars := tailTokens * 2

	runes := []rune(content)
	if len(runes) <= headChars+tailChars {
		return TruncateContent(content, limit)
	}

	head := string(runes[:headChars])
	tail := string(runes[len(runes)-tailChars:])

	return head + fmt.Sprintf("\n\n[... 中间部分已截断，原始长度 %d tokens ...]\n\n", EstimateTokens(content)) + tail
}

// ─── Default tool backends ──────────────────────────────────────────────

// defaultHTTPClient returns an HTTP client with SSRF protection:
//   - Connection-level IP validation (DNS rebinding resistant)
//   - No automatic redirect following
//   - 10-second timeout
//   - Respects HTTP_PROXY/HTTPS_PROXY for routing through VPN/proxy
func defaultHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	proxyURL := proxyFromEnv()
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := dialer.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				// When a proxy is configured, skip SSRF IP validation —
				// the proxy (trusted VPN/socks) handles outbound security.
				if proxyURL != nil {
					return conn, nil
				}
				// Validate the connected IP for direct connections.
				host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
				if err != nil {
					conn.Close()
					return nil, fmt.Errorf("web_fetch: cannot parse remote addr: %w", err)
				}
				if isPrivateIP(net.ParseIP(host)) {
					conn.Close()
					return nil, fmt.Errorf("web_fetch: blocked private IP %s", host)
				}
				return conn, nil
			},
		},
		// Do not follow redirects — each target must be explicitly validated.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// proxyFromEnv reads HTTPS_PROXY/HTTP_PROXY from environment and returns the
// parsed URL, or nil if no proxy is configured.
func proxyFromEnv() *url.URL {
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		if raw := os.Getenv(key); raw != "" {
			if u, err := url.Parse(raw); err == nil {
				return u
			}
		}
	}
	return nil
}

// privateNetworks defines the CIDR ranges blocked by isPrivateIP.
var privateNetworks = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"::1/128",
	"fc00::/7",
}

// isPrivateIP returns true if ip is in a private/reserved range.
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true // block unresolvable
	}
	for _, cidr := range privateNetworks {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// DefaultWebFetch uses Jina Reader (r.jina.ai) to convert any web page into
// clean, LLM-friendly Markdown. This is far superior to raw HTML scraping.
// Jina Reader is free to use and handles JavaScript rendering, ad removal,
// and content extraction automatically.
func DefaultWebFetch(ctx context.Context, rawURL string) (*WebFetchOutput, error) {
	// Validate URL scheme.
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("web_fetch: unsupported scheme %q", u.Scheme)
	}

	// Validate hostname IP before making request (defense-in-depth).
	if ip := net.ParseIP(u.Hostname()); ip != nil && isPrivateIP(ip) {
		return nil, fmt.Errorf("web_fetch: blocked IP address %s", u.Hostname())
	}

	// Use Jina Reader to get clean Markdown content.
	jinaURL := "https://r.jina.ai/" + rawURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: create request: %w", err)
	}
	req.Header.Set("Accept", "text/markdown")
	req.Header.Set("User-Agent", "ResearchAgent/1.0")

	resp, err := defaultHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Limit response size to 256KB (Markdown is much smaller than HTML).
	const maxSize = 256 * 1024
	limited := io.LimitReader(resp.Body, maxSize)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: read body: %w", err)
	}

	content := string(body)
	title := extractMarkdownTitle(content)

	return &WebFetchOutput{
		URL:     rawURL,
		Title:   title,
		Content: content,
	}, nil
}

// extractMarkdownTitle extracts the first H1 heading from Markdown content.
func extractMarkdownTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

// extractTitle heuristically extracts a <title> from HTML.
func extractTitle(html string) string {
	lower := strings.ToLower(html)
	start := strings.Index(lower, "<title>")
	if start < 0 {
		return ""
	}
	start += len("<title>")
	end := strings.Index(lower[start:], "</title>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(html[start : start+end])
}

// extractText performs a minimal HTML-to-text conversion.
func extractText(html string) string {
	// Remove script and style blocks.
	for _, tag := range []string{"script", "style"} {
		lower := strings.ToLower(html)
		for {
			start := strings.Index(lower, "<"+tag)
			if start < 0 {
				break
			}
			end := strings.Index(lower[start:], "</"+tag+">")
			if end < 0 {
				break
			}
			end += len("</" + tag + ">")
			html = html[:start] + html[start+end:]
			lower = strings.ToLower(html)
		}
	}
	// Remove HTML tags.
	var result strings.Builder
	inTag := false
	for _, r := range html {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			result.WriteRune(' ')
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	// Collapse whitespace.
	text := strings.Join(strings.Fields(result.String()), " ")
	return text
}

// ─── Search backend configuration ─────────────────────────────────────

// SearchBackendConfig holds configuration parsed from environment variables.
//
//	SEARCH_BACKEND=tavily|bing|searxng|builtin   (empty = web scrape, unreliable)
//	TAVILY_API_KEY=...                            (required for tavily, free tier 1000/mo)
//	BING_API_KEY=...                              (required for bing API)
//	SEARXNG_BASE_URL=https://...                  (required for searxng)
type SearchBackendConfig struct {
	Backend   string // "tavily", "bing", "searxng", "builtin", "" (web scrape)
	TavilyKey string
	BingKey   string
	SearXNG   string
}

// LoadSearchBackendConfig reads search backend config from environment variables.
func LoadSearchBackendConfig() SearchBackendConfig {
	return SearchBackendConfig{
		Backend:   strings.ToLower(os.Getenv("SEARCH_BACKEND")),
		TavilyKey: os.Getenv("TAVILY_API_KEY"),
		BingKey:   os.Getenv("BING_API_KEY"),
		SearXNG:   os.Getenv("SEARXNG_BASE_URL"),
	}
}

// NewWebSearchFunc returns a WebSearchFn based on the configured backend.
// If no backend is configured, returns nil (caller should use LLM-only fallback).
func NewWebSearchFunc(cfg SearchBackendConfig) func(ctx context.Context, query string) ([]WebSearchResult, error) {
	switch cfg.Backend {
	case "tavily":
		if cfg.TavilyKey == "" {
			return bingWebSearch // no key → degrade to web scrape
		}
		return func(ctx context.Context, query string) ([]WebSearchResult, error) {
			return tavilySearch(ctx, cfg.TavilyKey, query)
		}
	case "bing":
		if cfg.BingKey == "" {
			return bingWebSearch // no key → web scrape
		}
		// Try API first, fall back to web scrape on auth failure.
		return func(ctx context.Context, query string) ([]WebSearchResult, error) {
			r, err := bingSearch(ctx, cfg.BingKey, query)
			if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403")) {
				return bingWebSearch(ctx, query)
			}
			return r, err
		}
	case "searxng":
		if cfg.SearXNG == "" {
			return bingWebSearch
		}
		return func(ctx context.Context, query string) ([]WebSearchResult, error) {
			return searxngSearch(ctx, cfg.SearXNG, query)
		}
	case "builtin":
		return duckduckgoSearch
	default:
		return duckduckgoSearch
	}
}

// ─── Bing Web Search (scrape cn.bing.com, no API key) ──────────────────

func bingWebSearch(ctx context.Context, query string) ([]WebSearchResult, error) {
	searchURL := fmt.Sprintf("https://cn.bing.com/search?q=%s&setlang=zh-Hans",
		url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bing_web: create request: %w", err)
	}
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bing_web: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, fmt.Errorf("bing_web: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bing_web: HTTP %d", resp.StatusCode)
	}

	return parseBingWeb(string(body)), nil
}

// parseBingWeb extracts search results from cn.bing.com HTML.
func parseBingWeb(html string) []WebSearchResult {
	var results []WebSearchResult
	lower := strings.ToLower(html)

	// Collect h2 blocks: <h2><a href="URL">TITLE</a></h2>
	type rawResult struct{ url, title string }
	var raws []rawResult
	pos := 0
	for len(raws) < 10 {
		start := strings.Index(lower[pos:], "<h2")
		if start < 0 {
			break
		}
		start += pos
		end := strings.Index(lower[start:], "</h2>")
		if end < 0 {
			pos = start + 1
			continue
		}
		end += start + len("</h2>")
		block := html[start:end]
		urlStr := extractAttr(block, "href")
		title := stripTags(extractBetween(block, "<a", "</a>"))
		if !strings.Contains(urlStr, "bing.com") && !strings.Contains(urlStr, "microsoft.com") &&
			!strings.Contains(urlStr, "msn.com") && title != "" {
			raws = append(raws, rawResult{url: urlStr, title: title})
		}
		pos = end
	}

	// Collect snippets from <p class="b_lineclamp...">SNIPPET</p>
	snippets := extractSnippets(html)

	for i, r := range raws {
		snip := ""
		if i < len(snippets) {
			snip = snippets[i]
		}
		if r.title == "" {
			continue
		}
		results = append(results, WebSearchResult{Title: r.title, Snippet: snip, URL: r.url})
		if len(results) >= 5 {
			break
		}
	}
	if len(results) == 0 {
		return nil
	}
	return results
}

func extractAttr(html, attr string) string {
	lower := strings.ToLower(html)
	start := strings.Index(lower, attr+"=\"")
	if start < 0 {
		return ""
	}
	start += len(attr) + 2
	end := strings.Index(html[start:], "\"")
	if end < 0 {
		return ""
	}
	return html[start : start+end]
}

func extractBetween(html, open, close string) string {
	start := strings.Index(strings.ToLower(html), strings.ToLower(open))
	if start < 0 {
		return ""
	}
	end := strings.Index(strings.ToLower(html[start:]), strings.ToLower(close))
	if end < 0 {
		return ""
	}
	return html[start : start+end+len(close)]
}

func extractSnippets(html string) []string {
	var out []string
	lower := strings.ToLower(html)
	pos := 0
	for len(out) < 10 {
		m := strings.Index(lower[pos:], "class=\"b_lineclamp")
		if m < 0 {
			break
		}
		close := strings.Index(lower[pos+m:], ">")
		if close < 0 {
			pos++
			continue
		}
		start := pos + m + close + 1
		end := strings.Index(lower[start:], "</p>")
		if end < 0 {
			pos++
			continue
		}
		s := stripTags(html[start : start+end])
		if s != "" {
			out = append(out, s)
		}
		pos = start + end + len("</p>")
	}
	return out
}

// stripTags removes HTML tags from a string.
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			b.WriteRune(' ')
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}

// ─── Tavily Search (AI-optimized, free tier 1000/month) ────────────────

// tavilyResponse is the JSON response from api.tavily.com/search.
type tavilyResponse struct {
	Results []tavilyResult `json:"results"`
}

type tavilyResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func tavilySearch(ctx context.Context, apiKey, query string) ([]WebSearchResult, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"api_key":             apiKey,
		"query":               query,
		"search_depth":        "advanced",
		"max_results":         5,
		"include_raw_content": false,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.tavily.com/search", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("tavily: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ResearchAgent/1.0")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("tavily: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var tr tavilyResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("tavily: parse response: %w", err)
	}

	var results []WebSearchResult
	for _, r := range tr.Results {
		if r.Title == "" {
			continue
		}
		results = append(results, WebSearchResult{
			Title:   r.Title,
			Snippet: r.Content, // Tavily returns cleaned page content, not just snippet
			URL:     r.URL,
		})
		if len(results) >= 5 {
			break
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("tavily: no results for %q", query)
	}
	return results, nil
}

// ─── Bing Web Search ──────────────────────────────────────────────────

// bingResult is the JSON structure returned by Bing Web Search API v7.
type bingResult struct {
	WebPages struct {
		Value []struct {
			Name    string `json:"name"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"value"`
	} `json:"webPages"`
}

func bingSearch(ctx context.Context, apiKey, query string) ([]WebSearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.bing.microsoft.com/v7.0/search", nil)
	if err != nil {
		return nil, fmt.Errorf("bing: create request: %w", err)
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", apiKey)
	req.Header.Set("User-Agent", "ResearchAgent/1.0")

	q := req.URL.Query()
	q.Set("q", query)
	q.Set("count", "5")
	q.Set("mkt", "zh-CN")
	req.URL.RawQuery = q.Encode()

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bing: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("bing: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bing: HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var br bingResult
	if err := json.Unmarshal(body, &br); err != nil {
		return nil, fmt.Errorf("bing: parse response: %w", err)
	}

	var results []WebSearchResult
	for _, v := range br.WebPages.Value {
		results = append(results, WebSearchResult{
			Title:   v.Name,
			Snippet: v.Snippet,
			URL:     v.URL,
		})
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("bing: no results found for %q", query)
	}
	return results, nil
}

// ─── SearXNG Search ───────────────────────────────────────────────────

// searxngResult is the JSON structure returned by SearXNG API.
type searxngResult struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func searxngSearch(ctx context.Context, baseURL, query string) ([]WebSearchResult, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	searchURL := fmt.Sprintf("%s/search?format=json&q=%s",
		baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("searxng: create request: %w", err)
	}
	req.Header.Set("User-Agent", "ResearchAgent/1.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("searxng: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("searxng: HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var sr searxngResult
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("searxng: parse response: %w", err)
	}

	var results []WebSearchResult
	for _, r := range sr.Results {
		if len(results) >= 5 {
			break
		}
		results = append(results, WebSearchResult{
			Title:   r.Title,
			Snippet: r.Content,
			URL:     r.URL,
		})
	}
	return results, nil
}

// ─── DuckDuckGo (builtin, blocked in China) ────────────────────────────

func duckduckgoSearch(ctx context.Context, query string) ([]WebSearchResult, error) {
	searchURL := fmt.Sprintf("https://lite.duckduckgo.com/lite/?q=%s",
		url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("web_search: create request: %w", err)
	}
	req.Header.Set("User-Agent", "ResearchAgent/1.0")

	resp, err := defaultHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("web_search: read body: %w", err)
	}

	bodyStr := string(body)
	log.Printf("[duckduckgo] query=%q status=%d body_len=%d body_preview=%q",
		query, resp.StatusCode, len(bodyStr), types.TruncateForLog(bodyStr, 300))

	results := parseDuckDuckGoLite(bodyStr)
	log.Printf("[duckduckgo] parsed %d results", len(results))
	return results, nil
}

// parseDuckDuckGoLite extracts results from DuckDuckGo Lite HTML.
//
// DuckDuckGo Lite returns each result as a block of 3 <tr> rows:
//
//	<tr><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=<encoded>" class='result-link'>Title</a></td></tr>
//	<tr><td class='result-snippet'>Snippet</td></tr>
//	<tr><td><span class='link-text'>display_url</span></td></tr>
//
// The href is a redirect wrapper — we extract the real target URL from
// the uddg= query parameter.
func parseDuckDuckGoLite(html string) []WebSearchResult {
	var results []WebSearchResult

	// Match result-link anchors. In DDG Lite HTML, href= comes BEFORE class='result-link':
	//   <a rel="nofollow" href="//duckduckgo.com/l/?uddg=..." class='result-link'>TITLE</a>
	linkRe := regexp.MustCompile(`<a[^>]*href="([^"]*)"[^>]*class='result-link'[^>]*>\s*(.+?)\s*</a>`)
	linkMatches := linkRe.FindAllStringSubmatchIndex(html, -1)

	for _, m := range linkMatches {
		if len(results) >= 5 {
			break
		}

		redirectURL := html[m[2]:m[3]]
		title := html[m[4]:m[5]]
		anchorEnd := m[1] // position right after </a>

		// Extract the real target URL from DuckDuckGo's redirect wrapper.
		realURL := extractDDGURL(redirectURL)
		if realURL == "" {
			continue
		}

		// Find the snippet in the next result-snippet td.
		snippet := ""
		snipRe := regexp.MustCompile(`class='result-snippet'\s*>\s*(.*?)\s*</td>`)
		if sm := snipRe.FindStringSubmatch(html[anchorEnd:]); len(sm) >= 2 {
			snippet = stripTags(sm[1])
		}

		title = cleanHTML(title)
		if title == "" {
			continue
		}

		results = append(results, WebSearchResult{
			Title:   title,
			Snippet: snippet,
			URL:     realURL,
		})
	}

	return results
}

// extractDDGURL extracts the real target URL from a DuckDuckGo redirect URL.
// Format: //duckduckgo.com/l/?uddg=<url-encoded-target>&rut=<hash>
// Returns the decoded target URL, or the original string if not a DDG redirect.
func extractDDGURL(redirectURL string) string {
	if !strings.Contains(redirectURL, "uddg=") {
		// Not a DDG redirect — may be a direct link (rare but handle it).
		return redirectURL
	}
	idx := strings.Index(redirectURL, "uddg=")
	rest := redirectURL[idx+len("uddg="):]
	if ampIdx := strings.IndexAny(rest, "&;"); ampIdx >= 0 {
		rest = rest[:ampIdx]
	}
	decoded, err := url.QueryUnescape(rest)
	if err != nil {
		return ""
	}
	return decoded
}

// cleanHTML unescapes basic HTML entities.
func cleanHTML(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#x27;", "'")
	return strings.TrimSpace(s)
}
