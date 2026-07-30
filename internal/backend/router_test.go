package backend

import (
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	sessinmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/tenant"
)

func TestNewRouter(t *testing.T) {
	r := NewRouter()
	if r == nil {
		t.Fatal("NewRouter returned nil")
	}
	if ids := r.TenantIDs(); len(ids) != 0 {
		t.Errorf("empty router should have no tenants, got %v", ids)
	}
}

func TestRouter_RegisterAndResolve(t *testing.T) {
	r := NewRouter()

	b := &defaultBackend{
		session: sessinmemory.NewSessionService(),
		memory:  inmemory.NewMemoryService(),
	}
	defer b.Close()

	r.Register("tenant_001", b)

	got, err := r.Resolve("tenant_001")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil backend")
	}

	_, err = r.Resolve("nonexistent")
	if err == nil {
		t.Error("expected error for unknown tenant")
	}
}

func TestRouter_Remove(t *testing.T) {
	r := NewRouter()
	b := &defaultBackend{
		session: sessinmemory.NewSessionService(),
		memory:  inmemory.NewMemoryService(),
	}
	defer b.Close()

	r.Register("tenant_001", b)
	r.Remove("tenant_001")

	_, err := r.Resolve("tenant_001")
	if err == nil {
		t.Error("expected error after Remove")
	}
}

func TestRouter_TenantIDs(t *testing.T) {
	r := NewRouter()
	b := &defaultBackend{
		session: sessinmemory.NewSessionService(),
		memory:  inmemory.NewMemoryService(),
	}
	defer b.Close()

	r.Register("t1", b)
	r.Register("t2", b)

	ids := r.TenantIDs()
	if len(ids) != 2 {
		t.Errorf("len(TenantIDs) = %d, want 2", len(ids))
	}
}

func TestNewFactory_Defaults(t *testing.T) {
	f := NewFactory()

	cfg := tenant.DataBackendConfig{Type: tenant.BackendInMemory}

	b, err := f.Create(cfg)
	if err != nil {
		t.Fatalf("Create(inmemory): %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
	defer b.Close()

	if err := b.HealthCheck(); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestFactory_UnknownType(t *testing.T) {
	f := NewFactory()

	cfg := tenant.DataBackendConfig{Type: "unknown"}
	_, err := f.Create(cfg)
	if err == nil {
		t.Error("expected error for unknown backend type")
	}
}
