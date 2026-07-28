// Package backend defines the data access abstraction for routing tenant
// requests to their configured storage backends.
//
// The central concept is DataBackend, which bundles a session.Service and
// memory.Service pair for a single tenant. The BackendRouter maps tenant
// IDs to their DataBackend instances, and the TenantBackendFactory creates
// backend instances from tenant configuration.
package backend

import (
	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

// DataBackend bundles the session and memory services for a single tenant.
// Each tenant may use different backends — tenant A might use Redis while
// tenant B uses Postgres — so every tenant gets its own DataBackend pair.
type DataBackend interface {
	// SessionService returns the tenant-scoped session backend.
	SessionService() session.Service

	// MemoryService returns the tenant-scoped memory backend.
	MemoryService() memory.Service

	// HealthCheck verifies the backend is reachable and operational.
	HealthCheck() error

	// Close releases all resources held by the backend (connections,
	// background workers, etc.).
	Close() error
}
