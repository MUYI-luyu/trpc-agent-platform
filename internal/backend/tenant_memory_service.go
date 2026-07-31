package backend

import (
	"context"
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/tenant"
)

// compile-time interface conformance check
var _ memory.Service = (*TenantMemoryService)(nil)

// TenantMemoryService decorates a [memory.Service] with multi-tenant
// isolation. It follows the same zero-invasion pattern as
// [TenantSessionService]:
//
//  1. extract the tenant ID from context
//  2. resolve the tenant's dedicated backend via [Router]
//  3. inject the tenant prefix into memory keys (AppName)
//  4. delegate to the resolved backend
//
// When no tenant ID is present in the context, the request falls back to
// defaultBackend (single-tenant / testing mode).
type TenantMemoryService struct {
	router         *Router
	defaultBackend DataBackend // used when ctx has no tenant ID
}

// NewTenantMemoryService creates a memory.Service wrapper that provides
// tenant-level data isolation. The router maps tenant IDs to their configured
// backends; defaultBackend is used for requests without a tenant context.
func NewTenantMemoryService(router *Router, defaultBackend DataBackend) *TenantMemoryService {
	return &TenantMemoryService{
		router:         router,
		defaultBackend: defaultBackend,
	}
}

// resolve extracts the tenant ID from ctx, looks up the corresponding
// backend via the router, and returns the backend's memory.Service along
// with the tenant ID. Falls back to defaultBackend when no tenant ID is set.
func (s *TenantMemoryService) resolve(ctx context.Context) (memory.Service, string, error) {
	tenantID, ok := tenant.TenantIDFrom(ctx)
	if !ok || tenantID == "" {
		if s.defaultBackend != nil {
			return s.defaultBackend.MemoryService(), "", nil
		}
		return nil, "", fmt.Errorf("tenant ID not found in context and no default backend configured")
	}
	backend, err := s.router.Resolve(tenantID)
	if err != nil {
		return nil, tenantID, err
	}
	return backend.MemoryService(), tenantID, nil
}

// ─── memory.UserKey methods (AppName is inside the userKey) ─────────

// AddMemory implements memory.Service.
func (s *TenantMemoryService) AddMemory(ctx context.Context, userKey memory.UserKey, mem string, topics []string, opts ...memory.AddOption) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	userKey.AppName = tenant.BuildTenantAppName(tenantID, userKey.AppName)
	return svc.AddMemory(ctx, userKey, mem, topics, opts...)
}

// ClearMemories implements memory.Service.
func (s *TenantMemoryService) ClearMemories(ctx context.Context, userKey memory.UserKey) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	userKey.AppName = tenant.BuildTenantAppName(tenantID, userKey.AppName)
	return svc.ClearMemories(ctx, userKey)
}

// ReadMemories implements memory.Service.
func (s *TenantMemoryService) ReadMemories(ctx context.Context, userKey memory.UserKey, limit int) ([]*memory.Entry, error) {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	userKey.AppName = tenant.BuildTenantAppName(tenantID, userKey.AppName)
	return svc.ReadMemories(ctx, userKey, limit)
}

// SearchMemories implements memory.Service.
func (s *TenantMemoryService) SearchMemories(ctx context.Context, userKey memory.UserKey, query string, opts ...memory.SearchOption) ([]*memory.Entry, error) {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	userKey.AppName = tenant.BuildTenantAppName(tenantID, userKey.AppName)
	return svc.SearchMemories(ctx, userKey, query, opts...)
}

// ─── memory.Key methods (AppName is inside the key) ─────────────────

// UpdateMemory implements memory.Service.
func (s *TenantMemoryService) UpdateMemory(ctx context.Context, memoryKey memory.Key, mem string, topics []string, opts ...memory.UpdateOption) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	memoryKey.AppName = tenant.BuildTenantAppName(tenantID, memoryKey.AppName)
	return svc.UpdateMemory(ctx, memoryKey, mem, topics, opts...)
}

// DeleteMemory implements memory.Service.
func (s *TenantMemoryService) DeleteMemory(ctx context.Context, memoryKey memory.Key) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	memoryKey.AppName = tenant.BuildTenantAppName(tenantID, memoryKey.AppName)
	return svc.DeleteMemory(ctx, memoryKey)
}

// ─── Pass-through methods (no AppName to scope) ─────────────────────

// Tools implements memory.Service.
// Tools are not tenant-specific — every backend exposes the same set of
// memory tool definitions (memory_add, memory_update, etc.). We return
// tools from the default backend if available; otherwise we fall back to
// any registered tenant backend. Returns nil when no backend is available.
func (s *TenantMemoryService) Tools() []tool.Tool {
	if s.defaultBackend != nil {
		return s.defaultBackend.MemoryService().Tools()
	}
	for _, tid := range s.router.TenantIDs() {
		if backend, err := s.router.Resolve(tid); err == nil {
			return backend.MemoryService().Tools()
		}
	}
	return nil
}

// EnqueueAutoMemoryJob implements memory.Service.
func (s *TenantMemoryService) EnqueueAutoMemoryJob(ctx context.Context, sess *session.Session) error {
	svc, _, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	return svc.EnqueueAutoMemoryJob(ctx, sess)
}

// ─── Lifecycle ─────────────────────────────────────────────────────

// Close closes all backend memory services managed by this wrapper.
func (s *TenantMemoryService) Close() error {
	var firstErr error
	if s.defaultBackend != nil {
		if err := s.defaultBackend.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.router.CloseAll(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
