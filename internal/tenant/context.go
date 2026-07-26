package tenant

import (
	"context"
	"fmt"
	"strings"
)

// contextKey is the unexported type used for tenant context keys to avoid
// collisions with keys defined in other packages.
type contextKey string

const tenantIDKey contextKey = "tenant_id"

// WithTenantID injects a tenant identifier into the context. This should be
// called at the earliest entry point (Gateway middleware or webhook handler)
// so that downstream code can extract it with TenantIDFrom.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

// TenantIDFrom extracts the tenant identifier from the context. The bool
// return value is false when no tenant was set, which indicates the request
// is in single-tenant (default) mode.
func TenantIDFrom(ctx context.Context) (string, bool) {
	v := ctx.Value(tenantIDKey)
	if v == nil {
		return "", false
	}
	tenantID, ok := v.(string)
	return tenantID, ok
}

// appNameSeparator joins the tenant prefix with the original app name.
// It is deliberately NOT ":" because that would conflict with common
// AppName values and make ParseTenantAppName ambiguous.
const appNameSeparator = "|"

// BuildTenantAppName produces a tenant-scoped app name by prepending the
// tenant identifier. This is the primary isolation mechanism: every
// session.Service and memory.Service call receives this scoped app name
// instead of the original, so different tenants never see each other's
// sessions or memories even when using the same logical AppName.
//
//	tenant_001 + myapp  →  "tenant_001|myapp"
//
// A zero-value tenantID returns the app name unchanged (single-tenant mode).
func BuildTenantAppName(tenantID, appName string) string {
	if tenantID == "" {
		return appName
	}
	return fmt.Sprintf("%s%s%s", tenantID, appNameSeparator, appName)
}

// ParseTenantAppName reverses BuildTenantAppName, extracting the tenant ID
// and original app name from a scoped app name.
//
//	"tenant_001|myapp"  →  ("tenant_001", "myapp")
//	"myapp"             →  ("", "myapp")           (single-tenant mode)
func ParseTenantAppName(scoped string) (tenantID, appName string) {
	idx := strings.Index(scoped, appNameSeparator)
	if idx == -1 {
		return "", scoped
	}
	return scoped[:idx], scoped[idx+len(appNameSeparator):]
}
