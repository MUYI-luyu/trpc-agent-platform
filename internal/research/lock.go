package research

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// LockManager abstracts session-level mutual exclusion, shielding Node code
// from the underlying lock implementation (in-memory, Redis, etcd).
type LockManager interface {
	// TryLock attempts to acquire a lock for the given session, returning
	// immediately if it is already held.
	TryLock(ctx context.Context, sessionID string) (Lock, error)

	// Lock attempts to acquire a lock, blocking up to the given timeout.
	Lock(ctx context.Context, sessionID string, timeout time.Duration) (Lock, error)

	// Close releases any resources held by the LockManager (connections, etc.).
	Close() error
}

// Lock represents an acquired mutual exclusion lock on a session.
type Lock interface {
	Unlock(ctx context.Context) error
}

// ErrLockHeld is returned when TryLock finds the session already locked.
var ErrLockHeld = fmt.Errorf("session is locked by another request")

// ─── In-memory implementation ────────────────────────────────────────────

// InMemoryLockManager is a single-instance LockManager backed by an in-memory
// map. It is the default implementation for single-instance deployments.
// Multi-instance deployments should use a distributed lock (e.g., RedisLockManager).
type InMemoryLockManager struct {
	mu    sync.Mutex
	locks map[string]*inMemoryLock
}

// inMemoryLock is a session-level mutex tracked by InMemoryLockManager.
type inMemoryLock struct {
	mu        sync.Mutex
	sessionID string
	manager   *InMemoryLockManager
	held      bool
}

// NewInMemoryLockManager creates a ready-to-use InMemoryLockManager.
func NewInMemoryLockManager() *InMemoryLockManager {
	return &InMemoryLockManager{
		locks: make(map[string]*inMemoryLock),
	}
}

func (m *InMemoryLockManager) getOrCreate(sessionID string) *inMemoryLock {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[sessionID]
	if !ok {
		l = &inMemoryLock{
			sessionID: sessionID,
			manager:   m,
		}
		m.locks[sessionID] = l
	}
	return l
}

func (m *InMemoryLockManager) removeLock(l *inMemoryLock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.locks[l.sessionID]; ok && existing == l {
		delete(m.locks, l.sessionID)
	}
}

// TryLock attempts to acquire the lock without blocking.
func (m *InMemoryLockManager) TryLock(ctx context.Context, sessionID string) (Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionLock := m.getOrCreate(sessionID)
	if !sessionLock.mu.TryLock() {
		return nil, ErrLockHeld
	}
	sessionLock.held = true
	return sessionLock, nil
}

// Lock attempts to acquire the lock, blocking up to the given timeout.
func (m *InMemoryLockManager) Lock(ctx context.Context, sessionID string, timeout time.Duration) (Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionLock := m.getOrCreate(sessionID)

	// Create a channel that closes when the lock is acquired.
	acquired := make(chan struct{})
	go func() {
		sessionLock.mu.Lock()
		close(acquired)
	}()

	select {
	case <-acquired:
		sessionLock.held = true
		return sessionLock, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out waiting for session lock on %q", sessionID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close releases the LockManager. In the in-memory implementation this is a
// no-op since there are no external connections.
func (m *InMemoryLockManager) Close() error {
	return nil
}

// Unlock releases the session lock and removes it from the manager.
func (l *inMemoryLock) Unlock(_ context.Context) error {
	if l.held {
		l.held = false
		l.mu.Unlock()
	}
	l.manager.removeLock(l)
	return nil
}

// compile-time interface conformance check
var _ LockManager = (*InMemoryLockManager)(nil)
var _ Lock = (*inMemoryLock)(nil)
