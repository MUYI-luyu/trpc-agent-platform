package backend

import (
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go/memory"
	meminmemory "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessinmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/tenant"
)

// defaultBackend is a simple DataBackend implementation that holds a
// session.Service and memory.Service pair.
type defaultBackend struct {
	session session.Service
	memory  memory.Service
}

func (b *defaultBackend) SessionService() session.Service { return b.session }
func (b *defaultBackend) MemoryService() memory.Service   { return b.memory }
func (b *defaultBackend) HealthCheck() error              { return nil }
func (b *defaultBackend) Close() error {
	var firstErr error
	if err := b.session.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := b.memory.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Factory creates DataBackend instances from tenant configuration.
// Callers can register custom backend constructors via RegisterBuilder.
type Factory struct {
	builders map[tenant.BackendType]BackendBuilder
}

// BackendBuilder constructs a DataBackend from a DataBackendConfig.
type BackendBuilder func(cfg tenant.DataBackendConfig) (DataBackend, error)

// NewFactory creates a Factory with the built-in backend builders
// registered (inmemory).
func NewFactory() *Factory {
	f := &Factory{
		builders: make(map[tenant.BackendType]BackendBuilder),
	}
	f.RegisterBuilder(tenant.BackendInMemory, buildInMemory)
	return f
}

// RegisterBuilder adds or replaces a backend constructor for the given
// backend type. This is used to register additional backends (Redis,
// Postgres, etc.) that are not built into the factory.
func (f *Factory) RegisterBuilder(bt tenant.BackendType, builder BackendBuilder) {
	f.builders[bt] = builder
}

// Create constructs a DataBackend from the given configuration. It
// delegates to the registered BackendBuilder for the config's Type.
func (f *Factory) Create(cfg tenant.DataBackendConfig) (DataBackend, error) {
	builder, ok := f.builders[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("no backend builder registered for type %q", cfg.Type)
	}
	return builder(cfg)
}

// buildInMemory creates an in-memory backend pair. The DSN and connection
// parameters in the config are ignored — in-memory backends have no external
// dependencies.
func buildInMemory(_ tenant.DataBackendConfig) (DataBackend, error) {
	return &defaultBackend{
		session: sessinmemory.NewSessionService(),
		memory:  meminmemory.NewMemoryService(),
	}, nil
}

// NewSessionService is a convenience helper that creates a session.Service
// directly from a DataBackendConfig using the registered builders. This is
// useful when only a session service is needed.
func (f *Factory) NewSessionService(cfg tenant.DataBackendConfig) (session.Service, error) {
	b, err := f.Create(cfg)
	if err != nil {
		return nil, err
	}
	return b.SessionService(), nil
}

// NewMemoryService is a convenience helper that creates a memory.Service
// directly from a DataBackendConfig using the registered builders. This is
// useful when only a memory service is needed.
func (f *Factory) NewMemoryService(cfg tenant.DataBackendConfig) (memory.Service, error) {
	b, err := f.Create(cfg)
	if err != nil {
		return nil, err
	}
	return b.MemoryService(), nil
}
