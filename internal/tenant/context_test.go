package tenant

import (
	"context"
	"testing"
)

func TestWithTenantID_TenantIDFrom(t *testing.T) {
	ctx := context.Background()
	ctx = WithTenantID(ctx, "tenant_001")

	got, ok := TenantIDFrom(ctx)
	if !ok {
		t.Fatal("expected tenant ID to be present")
	}
	if got != "tenant_001" {
		t.Errorf("tenant ID = %q, want %q", got, "tenant_001")
	}
}

func TestTenantIDFrom_Empty(t *testing.T) {
	ctx := context.Background()
	_, ok := TenantIDFrom(ctx)
	if ok {
		t.Error("expected false for empty context")
	}
}

func TestBuildTenantAppName(t *testing.T) {
	tests := []struct {
		tenantID string
		appName  string
		want     string
	}{
		{"tenant_001", "myapp", "tenant_001|myapp"},
		{"", "myapp", "myapp"},
		{"tenant_002", "app", "tenant_002|app"},
	}

	for _, tt := range tests {
		got := BuildTenantAppName(tt.tenantID, tt.appName)
		if got != tt.want {
			t.Errorf("BuildTenantAppName(%q, %q) = %q, want %q",
				tt.tenantID, tt.appName, got, tt.want)
		}
	}
}

func TestParseTenantAppName(t *testing.T) {
	tests := []struct {
		scoped       string
		wantTenantID string
		wantAppName  string
	}{
		{"tenant_001|myapp", "tenant_001", "myapp"},
		{"myapp", "", "myapp"},
		{"tenant_002|app", "tenant_002", "app"},
		{"no_separator_here", "", "no_separator_here"},
	}

	for _, tt := range tests {
		tenantID, appName := ParseTenantAppName(tt.scoped)
		if tenantID != tt.wantTenantID || appName != tt.wantAppName {
			t.Errorf("ParseTenantAppName(%q) = (%q, %q), want (%q, %q)",
				tt.scoped, tenantID, appName, tt.wantTenantID, tt.wantAppName)
		}
	}
}

func TestBuildAndParse_RoundTrip(t *testing.T) {
	tid := "tenant_001"
	app := "myapp"

	scoped := BuildTenantAppName(tid, app)
	gotTID, gotApp := ParseTenantAppName(scoped)

	if gotTID != tid || gotApp != app {
		t.Errorf("round-trip failed: (%q, %q) → %q → (%q, %q)",
			tid, app, scoped, gotTID, gotApp)
	}
}
