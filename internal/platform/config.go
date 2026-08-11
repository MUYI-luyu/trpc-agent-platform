// Package platform provides the Agent Platform server — the orchestration
// layer that wires Gateway, Worker Pool, Filter Chain, Data Backends,
// Channel Adapters, Telemetry, and Audit into a single runnable binary.
package platform

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/telemetry"
)

// ErrNoAgent is returned when no agent is configured.
var ErrNoAgent = fmt.Errorf("platform: no agent configured (set DEEPSEEK_API_KEY)")

// Config is the complete platform configuration. It can be loaded from
// environment variables or a YAML file.
type Config struct {
	// Server is the HTTP server configuration.
	Server ServerConfig

	// Telemetry is the OpenTelemetry configuration.
	Telemetry telemetry.Config

	// DataBackend is the default data backend for tenants that don't
	// specify their own.
	DataBackend DataBackendSection

	// Audit is the audit log configuration.
	Audit AuditSection

	// LLM configures the default model provider credentials.
	LLM LLMSection

	// Tenants contains the per-tenant configuration. If nil, the
	// platform runs in single-tenant mode.
	Tenants []TenantConfig
}

// ServerConfig holds the HTTP server configuration.
type ServerConfig struct {
	// Addr is the listen address (e.g. ":8080").
	Addr string

	// ReadTimeout is the maximum duration for reading a request.
	ReadTimeout time.Duration

	// WriteTimeout is the maximum duration for writing a response.
	WriteTimeout time.Duration

	// ShutdownTimeout is how long to wait for graceful shutdown.
	ShutdownTimeout time.Duration
}

// DataBackendSection selects the default storage backend.
type DataBackendSection struct {
	// Type is "inmemory", "sqlite", "redis", or "postgres".
	Type string

	// DSN is the connection string or file path.
	DSN string
}

// AuditSection configures audit logging.
type AuditSection struct {
	// Type is "sqlite", "none".
	Type string

	// DSN is the database connection string for audit logs.
	DSN string
}

// LLMSection holds the default LLM provider credentials.
type LLMSection struct {
	// Provider is "deepseek", "openai", "anthropic".
	Provider string

	// APIKey is the provider API key.
	APIKey string

	// BaseURL overrides the API base URL.
	BaseURL string

	// DefaultModel is the model name used when a tenant doesn't specify one.
	DefaultModel string
}

// TenantConfig is a tenant definition for bootstrapping. In production
// these are loaded from the Admin API or a database.
type TenantConfig struct {
	ID            string
	Name          string
	ModelName     string
	ToolMode      string
	AllowedTools  []string
	BackendType   string
	BackendDSN    string
	AuditLevel    string
	RateLimitRPS  int
	ChannelType   string
	ChannelCorpID string
}

// ─── Loading from environment ────────────────────────────────────────

// LoadConfigFromEnv builds a Config from environment variables.
// Suitable for development and Docker Compose deployments.
func LoadConfigFromEnv() *Config {
	cfg := &Config{
		Server: ServerConfig{
			Addr:            envOrDefault("SERVER_ADDR", ":8080"),
			ReadTimeout:     15 * time.Second,
			WriteTimeout:    60 * time.Second,
			ShutdownTimeout: 10 * time.Second,
		},
		Telemetry: telemetry.Config{
			ServiceName: envOrDefault("OTEL_SERVICE_NAME", "agent-platform"),
			Exporter:    telemetry.ExporterType(envOrDefault("OTEL_EXPORTER", "none")),
			Endpoint:    envOrDefault("OTEL_ENDPOINT", "localhost:4317"),
			SampleRate:  envFloatOrDefault("OTEL_SAMPLE_RATE", 1.0),
		},
		DataBackend: DataBackendSection{
			Type: envOrDefault("DB_BACKEND", "inmemory"),
			DSN:  envOrDefault("SQLITE_DSN", "file:platform.db?_journal_mode=WAL&_busy_timeout=5000"),
		},
		Audit: AuditSection{
			Type: envOrDefault("AUDIT_WRITER", "none"),
			DSN:  envOrDefault("AUDIT_DSN", "file:audit.db?_journal_mode=WAL"),
		},
		LLM: LLMSection{
			Provider:     envOrDefault("LLM_PROVIDER", "deepseek"),
			APIKey:       os.Getenv("DEEPSEEK_API_KEY"),
			BaseURL:      envOrDefault("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1"),
			DefaultModel: envOrDefault("DEEPSEEK_MODEL", "deepseek-v4-flash"),
		},
	}

	// Bootstrap tenants from env if defined.
	if tid := os.Getenv("PLATFORM_TENANT_ID"); tid != "" {
		cfg.Tenants = append(cfg.Tenants, TenantConfig{
			ID:           tid,
			Name:         envOrDefault("PLATFORM_TENANT_NAME", tid),
			ModelName:    cfg.LLM.DefaultModel,
			ToolMode:     envOrDefault("PLATFORM_TOOL_MODE", "allowlist"),
			BackendType:  cfg.DataBackend.Type,
			BackendDSN:   cfg.DataBackend.DSN,
			AuditLevel:   envOrDefault("PLATFORM_AUDIT_LEVEL", "minimal"),
			RateLimitRPS: envIntOrDefault("PLATFORM_RATE_LIMIT", 10),
		})
	}

	return cfg
}

// ─── Helpers ─────────────────────────────────────────────────────────

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envFloatOrDefault(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return n
}
