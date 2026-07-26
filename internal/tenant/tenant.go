// Package tenant provides the core tenant data model for the multi-tenant
// AI Agent platform. A tenant is the top-level isolation unit, owning its
// own model configuration, tool permissions, IM channel bindings, storage
// backend selection, audit policy, and rate limits.
package tenant

import "time"

// Status represents the lifecycle state of a tenant.
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusDeleted   Status = "deleted"
)

// BackendType enumerates supported storage backends.
type BackendType string

const (
	BackendInMemory BackendType = "inmemory"
	BackendSQLite   BackendType = "sqlite"
	BackendRedis    BackendType = "redis"
	BackendPostgres BackendType = "postgres"
	BackendMySQL    BackendType = "mysql"
)

// AuditLevel controls how much detail is recorded in audit logs.
type AuditLevel string

const (
	AuditOff     AuditLevel = "off"
	AuditMinimal AuditLevel = "minimal" // request/response metadata only
	AuditFull    AuditLevel = "full"    // includes tool arguments and model prompts
)

// Tenant is the core multi-tenancy unit. Each tenant has its own model
// configuration, tool permissions, IM channel bindings, storage backend
// selection, audit policy, and rate limits.
type Tenant struct {
	// ID is the unique tenant identifier (e.g. "tenant_001").
	ID string `json:"id" yaml:"id"`

	// Name is the human-readable display name.
	Name string `json:"name" yaml:"name"`

	// Status controls whether the tenant can process requests.
	Status Status `json:"status" yaml:"status"`

	// ModelConfig selects the LLM model and its parameters for this tenant.
	ModelConfig ModelConfig `json:"model_config" yaml:"model_config"`

	// ToolPermissions defines which tools this tenant's agents may use.
	ToolPermissions ToolPermissions `json:"tool_permissions" yaml:"tool_permissions"`

	// IMChannelConfigs holds the IM platform integration settings.
	IMChannelConfigs []IMChannelConfig `json:"im_channel_configs,omitempty" yaml:"im_channel_configs,omitempty"`

	// DataBackendConfig selects the storage backend and its connection parameters.
	DataBackendConfig DataBackendConfig `json:"data_backend_config" yaml:"data_backend_config"`

	// AuditPolicy controls audit log detail and retention.
	AuditPolicy AuditPolicy `json:"audit_policy" yaml:"audit_policy"`

	// RateLimits defines the per-tenant request rate caps.
	RateLimits RateLimits `json:"rate_limits" yaml:"rate_limits"`

	// CreatedAt is the tenant creation timestamp.
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`

	// UpdatedAt is the last modification timestamp.
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

// IsActive returns true if the tenant is in active state.
func (t *Tenant) IsActive() bool { return t.Status == StatusActive }

// ModelConfig specifies the LLM model and its generation parameters for a
// tenant.
type ModelConfig struct {
	// ModelName is the model identifier (e.g. "deepseek-chat", "gpt-4o").
	ModelName string `json:"model_name" yaml:"model_name"`

	// MaxTokens limits the total tokens per completion. 0 means use the
	// model default.
	MaxTokens int `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`

	// Temperature controls randomness (0.0–2.0). Default is model-specific.
	Temperature float64 `json:"temperature,omitempty" yaml:"temperature,omitempty"`

	// TimeoutSeconds is the per-request timeout for model calls. 0 means
	// use the system default (60s).
	TimeoutSeconds int `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`

	// FallbackModel is used when the primary model times out. Empty means
	// no fallback.
	FallbackModel string `json:"fallback_model,omitempty" yaml:"fallback_model,omitempty"`

	// FallbackMessage is returned to the user when all models fail.
	FallbackMessage string `json:"fallback_message,omitempty" yaml:"fallback_message,omitempty"`
}

// ToolPermissions controls which tools a tenant's agents may invoke.
type ToolPermissions struct {
	// Mode is either "allowlist" (only listed tools) or "blocklist"
	// (all tools except listed).
	Mode string `json:"mode" yaml:"mode"` // "allowlist" | "blocklist"

	// Tools is the list of tool names for the allow/block list.
	Tools []string `json:"tools,omitempty" yaml:"tools,omitempty"`

	// DangerousTools lists tools that require a secondary confirmation
	// before execution (e.g. "code_exec", "host_exec").
	DangerousTools []string `json:"dangerous_tools,omitempty" yaml:"dangerous_tools,omitempty"`

	// RequireConfirmationForDangerous enables the two-step confirmation
	// flow for dangerous tools.
	RequireConfirmationForDangerous bool `json:"require_confirmation_for_dangerous" yaml:"require_confirmation_for_dangerous"`
}

// IMChannelConfig holds the integration settings for one IM platform.
type IMChannelConfig struct {
	// ChannelType identifies the IM platform.
	ChannelType string `json:"channel_type" yaml:"channel_type"` // "wecom" | "wechat_mp"

	// WebhookURL is the callback URL registered with the IM platform.
	WebhookURL string `json:"webhook_url,omitempty" yaml:"webhook_url,omitempty"`

	// Token is used for webhook signature verification (WeChat Work).
	Token string `json:"token,omitempty" yaml:"token,omitempty"`

	// EncodingAESKey is the message encryption key (WeChat Work).
	EncodingAESKey string `json:"encoding_aes_key,omitempty" yaml:"encoding_aes_key,omitempty"`

	// CorpID is the WeChat Work corporation identifier.
	CorpID string `json:"corp_id,omitempty" yaml:"corp_id,omitempty"`

	// AppSecret is used to obtain the access_token for active API calls.
	AppSecret string `json:"app_secret,omitempty" yaml:"app_secret,omitempty"`

	// Enabled controls whether this channel is currently active.
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// DataBackendConfig selects the storage backend type and provides its
// connection parameters.
type DataBackendConfig struct {
	// Type is the backend type.
	Type BackendType `json:"type" yaml:"type"`

	// DSN is the data source name (SQLite file path, Postgres connection
	// string, Redis address, etc.). Not used for inmemory.
	DSN string `json:"dsn,omitempty" yaml:"dsn,omitempty"`

	// MaxConnections is the maximum number of connections in the pool.
	// 0 means use the backend default.
	MaxConnections int `json:"max_connections,omitempty" yaml:"max_connections,omitempty"`

	// ConnectTimeoutSeconds is the timeout for establishing a connection.
	ConnectTimeoutSeconds int `json:"connect_timeout_seconds,omitempty" yaml:"connect_timeout_seconds,omitempty"`
}

// AuditPolicy controls what is recorded and how long it is kept.
type AuditPolicy struct {
	// Level controls the audit detail.
	Level AuditLevel `json:"level" yaml:"level"`

	// RetentionDays is how many days audit logs are kept before deletion.
	// 0 means indefinite.
	RetentionDays int `json:"retention_days,omitempty" yaml:"retention_days,omitempty"`

	// LogToolArguments records tool call arguments in the audit log when
	// true. Sensitive arguments should be masked separately.
	LogToolArguments bool `json:"log_tool_arguments" yaml:"log_tool_arguments"`

	// MaskSensitiveData enables automatic redaction of phone numbers,
	// ID cards, and API keys in audit entries.
	MaskSensitiveData bool `json:"mask_sensitive_data" yaml:"mask_sensitive_data"`
}

// RateLimits defines per-tenant throughput caps.
type RateLimits struct {
	// RequestsPerSecond is the maximum requests per second.
	RequestsPerSecond int `json:"requests_per_second,omitempty" yaml:"requests_per_second,omitempty"`

	// TokensPerMonth is the monthly token consumption budget. 0 means
	// unlimited.
	TokensPerMonth int64 `json:"tokens_per_month,omitempty" yaml:"tokens_per_month,omitempty"`

	// ConcurrentSessions is the maximum number of concurrent sessions.
	// 0 means unlimited.
	ConcurrentSessions int `json:"concurrent_sessions,omitempty" yaml:"concurrent_sessions,omitempty"`
}

// ConfigChange describes a change in tenant configuration.
type ConfigChange struct {
	// Type is the kind of change.
	Type ChangeType `json:"type"`

	// TenantID identifies the affected tenant.
	TenantID string `json:"tenant_id"`

	// Before is the previous tenant state (nil for Create).
	Before *Tenant `json:"before,omitempty"`

	// After is the new tenant state (nil for Delete).
	After *Tenant `json:"after,omitempty"`
}

// ChangeType enumerates the kinds of configuration changes.
type ChangeType string

const (
	ChangeCreate ChangeType = "create"
	ChangeUpdate ChangeType = "update"
	ChangeDelete ChangeType = "delete"
)
