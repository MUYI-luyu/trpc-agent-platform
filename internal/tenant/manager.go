package tenant

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Manager defines the lifecycle operations for tenants. Implementations
// may be backed by in-memory storage, a file, an etcd cluster, or a
// database.
type Manager interface {
	// Create registers a new tenant. Returns an error if the tenant ID
	// already exists.
	Create(ctx context.Context, tenant *Tenant) error

	// Get retrieves a tenant by ID. Returns nil if not found.
	Get(ctx context.Context, tenantID string) (*Tenant, error)

	// Update replaces the configuration for an existing tenant. The tenant
	// must already exist.
	Update(ctx context.Context, tenant *Tenant) error

	// Delete removes a tenant. Implementations may perform a soft delete
	// (mark as StatusDeleted) or a hard delete.
	Delete(ctx context.Context, tenantID string) error

	// List returns all tenants, including suspended and deleted ones.
	List(ctx context.Context) ([]*Tenant, error)

	// Reload triggers a configuration refresh from the backing store.
	// For InMemoryManager this is a no-op; for file/db-backed managers
	// this re-reads the source.
	Reload(ctx context.Context) error

	// Watch returns a channel that emits configuration changes. Callers
	// should range over the channel to react to changes in real time.
	// The channel is closed when the context is cancelled.
	Watch(ctx context.Context) (<-chan ConfigChange, error)
}

// InMemoryManager is a Manager implementation that stores tenants in
// process memory. It is suitable for development, testing, and single-node
// deployments where durability is not required.
type InMemoryManager struct {
	mu      sync.RWMutex
	tenants map[string]*Tenant

	subscribers map[int]chan ConfigChange
	nextSubID   int
	subMu       sync.Mutex
}

// Ensure InMemoryManager implements Manager.
var _ Manager = (*InMemoryManager)(nil)

// NewInMemoryManager creates an empty InMemoryManager.
func NewInMemoryManager() *InMemoryManager {
	return &InMemoryManager{
		tenants:     make(map[string]*Tenant),
		subscribers: make(map[int]chan ConfigChange),
	}
}

// Create registers a new tenant.
func (m *InMemoryManager) Create(_ context.Context, tenant *Tenant) error {
	if tenant == nil {
		return fmt.Errorf("tenant must not be nil")
	}
	if tenant.ID == "" {
		return fmt.Errorf("tenant ID must not be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.tenants[tenant.ID]; exists {
		return fmt.Errorf("tenant %q already exists", tenant.ID)
	}

	now := time.Now()
	tenant.CreatedAt = now
	tenant.UpdatedAt = now
	if tenant.Status == "" {
		tenant.Status = StatusActive
	}

	cloned := *tenant
	m.tenants[tenant.ID] = &cloned

	m.emit(ConfigChange{
		Type:     ChangeCreate,
		TenantID: tenant.ID,
		After:    tenant,
	})
	return nil
}

// Get retrieves a tenant by ID.
func (m *InMemoryManager) Get(_ context.Context, tenantID string) (*Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, ok := m.tenants[tenantID]
	if !ok {
		return nil, fmt.Errorf("tenant %q not found", tenantID)
	}

	cloned := *t
	return &cloned, nil
}

// Update replaces the configuration for an existing tenant.
func (m *InMemoryManager) Update(_ context.Context, tenant *Tenant) error {
	if tenant == nil || tenant.ID == "" {
		return fmt.Errorf("tenant and tenant ID must not be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	old, exists := m.tenants[tenant.ID]
	if !exists {
		return fmt.Errorf("tenant %q not found", tenant.ID)
	}

	tenant.CreatedAt = old.CreatedAt
	tenant.UpdatedAt = time.Now()

	cloned := *tenant
	m.tenants[tenant.ID] = &cloned

	m.emit(ConfigChange{
		Type:     ChangeUpdate,
		TenantID: tenant.ID,
		Before:   old,
		After:    tenant,
	})
	return nil
}

// Delete removes a tenant (hard delete).
func (m *InMemoryManager) Delete(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	old, exists := m.tenants[tenantID]
	if !exists {
		return fmt.Errorf("tenant %q not found", tenantID)
	}

	delete(m.tenants, tenantID)

	m.emit(ConfigChange{
		Type:     ChangeDelete,
		TenantID: tenantID,
		Before:   old,
	})
	return nil
}

// List returns all tenants.
func (m *InMemoryManager) List(_ context.Context) ([]*Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Tenant, 0, len(m.tenants))
	for _, t := range m.tenants {
		cloned := *t
		result = append(result, &cloned)
	}
	return result, nil
}

// Reload is a no-op for the in-memory implementation.
func (m *InMemoryManager) Reload(_ context.Context) error { return nil }

// Watch returns a channel that receives tenant configuration changes.
func (m *InMemoryManager) Watch(ctx context.Context) (<-chan ConfigChange, error) {
	ch := make(chan ConfigChange, 64)

	m.subMu.Lock()
	id := m.nextSubID
	m.nextSubID++
	m.subscribers[id] = ch
	m.subMu.Unlock()

	go func() {
		<-ctx.Done()
		m.subMu.Lock()
		delete(m.subscribers, id)
		m.subMu.Unlock()
		close(ch)
	}()

	return ch, nil
}

// emit broadcasts a ConfigChange to all active subscribers. Must be called
// while m.mu is held (Lock or RLock from the caller). This is a non-blocking
// send — slow subscribers will miss events rather than stall the writer.
func (m *InMemoryManager) emit(change ConfigChange) {
	m.subMu.Lock()
	defer m.subMu.Unlock()

	for _, ch := range m.subscribers {
		select {
		case ch <- change:
		default:
			// drop if subscriber is too slow
		}
	}
}
