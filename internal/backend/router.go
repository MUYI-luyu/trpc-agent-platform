package backend

import (
	"fmt"
	"sync"
)

// Router maps tenant IDs to their DataBackend instances. It is the central
// dispatch point: every tenant-scoped operation resolves the tenant ID to a
// backend and delegates to it.
//
// The zero value is ready to use.
type Router struct {
	mu       sync.RWMutex
	backends map[string]DataBackend // tenantID → backend
}

// NewRouter creates an empty Router.
func NewRouter() *Router {
	return &Router{
		backends: make(map[string]DataBackend),
	}
}

// Register associates a tenant with a backend. If the tenant already has a
// backend, it is replaced (the old backend is NOT closed — the caller must
// close it if necessary).
func (r *Router) Register(tenantID string, backend DataBackend) {
	if tenantID == "" || backend == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.backends[tenantID] = backend
}

// Resolve returns the backend for a given tenant. It returns an error if
// no backend has been registered for the tenant.
func (r *Router) Resolve(tenantID string) (DataBackend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	b, ok := r.backends[tenantID]
	if !ok {
		return nil, fmt.Errorf("no backend registered for tenant %q", tenantID)
	}
	return b, nil
}

// Remove dissociates a tenant from its backend. The backend is NOT closed —
// the caller must close it if necessary. Returns nil even if the tenant was
// not registered (idempotent).
func (r *Router) Remove(tenantID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.backends, tenantID)
}

// TenantIDs returns the list of tenant IDs currently registered in the
// router. The order is non-deterministic.
func (r *Router) TenantIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.backends))
	for id := range r.backends {
		ids = append(ids, id)
	}
	return ids
}

// CloseAll closes every registered backend and clears the router. Errors
// from individual backends are collected; the first error is returned.
func (r *Router) CloseAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var firstErr error
	for id, b := range r.backends {
		if err := b.Close(); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("close backend for tenant %q: %w", id, err)
			}
		}
	}
	r.backends = make(map[string]DataBackend)
	return firstErr
}
