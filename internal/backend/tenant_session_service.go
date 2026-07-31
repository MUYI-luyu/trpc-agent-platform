package backend

import (
	"context"
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/session"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/tenant"
)

// compile-time interface conformance check
var _ session.Service = (*TenantSessionService)(nil)

// TenantSessionService decorates a [session.Service] with multi-tenant
// isolation. It implements the full session.Service interface by:
//
//  1. extracting the tenant ID from context (set at the gateway layer)
//  2. resolving the tenant's dedicated backend via [Router]
//  3. injecting the tenant prefix into session keys (AppName)
//  4. delegating to the resolved backend
//
// When no tenant ID is present in the context, the request falls back to
// defaultBackend (single-tenant / testing mode). This is the zero-invasion
// mechanism — upstream tRPC-Agent-Go interfaces are not modified.
type TenantSessionService struct {
	router         *Router
	defaultBackend DataBackend // used when ctx has no tenant ID
}

// NewTenantSessionService creates a session.Service wrapper that provides
// tenant-level data isolation. The router maps tenant IDs to their configured
// backends; defaultBackend is used for requests without a tenant context
// (single-tenant or testing mode — can be nil if all requests are
// multi-tenant).
func NewTenantSessionService(router *Router, defaultBackend DataBackend) *TenantSessionService {
	return &TenantSessionService{
		router:         router,
		defaultBackend: defaultBackend,
	}
}

// resolve extracts the tenant ID from ctx, looks up the corresponding
// backend via the router, and returns the backend's session.Service along
// with the tenant ID. Falls back to defaultBackend when no tenant ID is set.
func (s *TenantSessionService) resolve(ctx context.Context) (session.Service, string, error) {
	tenantID, ok := tenant.TenantIDFrom(ctx)
	if !ok || tenantID == "" {
		if s.defaultBackend != nil {
			return s.defaultBackend.SessionService(), "", nil
		}
		return nil, "", fmt.Errorf("tenant ID not found in context and no default backend configured")
	}
	backend, err := s.router.Resolve(tenantID)
	if err != nil {
		return nil, tenantID, err
	}
	return backend.SessionService(), tenantID, nil
}

// ─── session.Key methods (AppName is inside the key) ────────────────

// CreateSession implements session.Service.
func (s *TenantSessionService) CreateSession(ctx context.Context, key session.Key, state session.StateMap, options ...session.Option) (*session.Session, error) {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	key.AppName = tenant.BuildTenantAppName(tenantID, key.AppName)
	return svc.CreateSession(ctx, key, state, options...)
}

// GetSession implements session.Service.
func (s *TenantSessionService) GetSession(ctx context.Context, key session.Key, options ...session.Option) (*session.Session, error) {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	key.AppName = tenant.BuildTenantAppName(tenantID, key.AppName)
	return svc.GetSession(ctx, key, options...)
}

// DeleteSession implements session.Service.
func (s *TenantSessionService) DeleteSession(ctx context.Context, key session.Key, options ...session.Option) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	key.AppName = tenant.BuildTenantAppName(tenantID, key.AppName)
	return svc.DeleteSession(ctx, key, options...)
}

// UpdateSessionState implements session.Service.
func (s *TenantSessionService) UpdateSessionState(ctx context.Context, key session.Key, state session.StateMap) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	key.AppName = tenant.BuildTenantAppName(tenantID, key.AppName)
	return svc.UpdateSessionState(ctx, key, state)
}

// ─── session.UserKey methods (AppName is inside the userKey) ───────

// ListSessions implements session.Service.
func (s *TenantSessionService) ListSessions(ctx context.Context, userKey session.UserKey, options ...session.Option) ([]*session.Session, error) {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	userKey.AppName = tenant.BuildTenantAppName(tenantID, userKey.AppName)
	return svc.ListSessions(ctx, userKey, options...)
}

// UpdateUserState implements session.Service.
func (s *TenantSessionService) UpdateUserState(ctx context.Context, userKey session.UserKey, state session.StateMap) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	userKey.AppName = tenant.BuildTenantAppName(tenantID, userKey.AppName)
	return svc.UpdateUserState(ctx, userKey, state)
}

// ListUserStates implements session.Service.
func (s *TenantSessionService) ListUserStates(ctx context.Context, userKey session.UserKey) (session.StateMap, error) {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	userKey.AppName = tenant.BuildTenantAppName(tenantID, userKey.AppName)
	return svc.ListUserStates(ctx, userKey)
}

// DeleteUserState implements session.Service.
func (s *TenantSessionService) DeleteUserState(ctx context.Context, userKey session.UserKey, key string) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	userKey.AppName = tenant.BuildTenantAppName(tenantID, userKey.AppName)
	return svc.DeleteUserState(ctx, userKey, key)
}

// ─── App-level state methods (appName is a bare string) ────────────

// UpdateAppState implements session.Service.
func (s *TenantSessionService) UpdateAppState(ctx context.Context, appName string, state session.StateMap) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	appName = tenant.BuildTenantAppName(tenantID, appName)
	return svc.UpdateAppState(ctx, appName, state)
}

// DeleteAppState implements session.Service.
func (s *TenantSessionService) DeleteAppState(ctx context.Context, appName string, key string) error {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	appName = tenant.BuildTenantAppName(tenantID, appName)
	return svc.DeleteAppState(ctx, appName, key)
}

// ListAppStates implements session.Service.
func (s *TenantSessionService) ListAppStates(ctx context.Context, appName string) (session.StateMap, error) {
	svc, tenantID, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	appName = tenant.BuildTenantAppName(tenantID, appName)
	return svc.ListAppStates(ctx, appName)
}

// ─── Session-level methods (session carries its own AppName) ───────

// AppendEvent implements session.Service.
func (s *TenantSessionService) AppendEvent(ctx context.Context, sess *session.Session, evt *event.Event, options ...session.Option) error {
	svc, _, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	return svc.AppendEvent(ctx, sess, evt, options...)
}

// CreateSessionSummary implements session.Service.
func (s *TenantSessionService) CreateSessionSummary(ctx context.Context, sess *session.Session, filterKey string, force bool) error {
	svc, _, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	return svc.CreateSessionSummary(ctx, sess, filterKey, force)
}

// EnqueueSummaryJob implements session.Service.
func (s *TenantSessionService) EnqueueSummaryJob(ctx context.Context, sess *session.Session, filterKey string, force bool) error {
	svc, _, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	return svc.EnqueueSummaryJob(ctx, sess, filterKey, force)
}

// GetSessionSummaryText implements session.Service.
func (s *TenantSessionService) GetSessionSummaryText(ctx context.Context, sess *session.Session, opts ...session.SummaryOption) (string, bool) {
	svc, _, err := s.resolve(ctx)
	if err != nil {
		return "", false
	}
	return svc.GetSessionSummaryText(ctx, sess, opts...)
}

// ─── Lifecycle ─────────────────────────────────────────────────────

// Close closes all backend session services managed by this wrapper.
func (s *TenantSessionService) Close() error {
	var firstErr error
	// Close the default backend first, then all tenant backends.
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
