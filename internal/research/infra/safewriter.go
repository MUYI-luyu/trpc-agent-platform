package infra

import (
	"context"
	"fmt"
	"net/http"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/research/types"
	"sync"
	"sync/atomic"
	"time"
)

// SafeStreamWriter wraps an HTTP response writer for SSE delivery with
// connection-broken detection, context cancellation awareness, and a write
// timeout to prevent goroutine leaks.
type SafeStreamWriter struct {
	writer  http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context

	// broken is set atomically when the connection is detected as broken.
	// Once true, all subsequent Write calls return types.ErrConnectionBroken
	// without blocking — this is the fast-path skip.
	broken atomic.Bool

	// mu serializes writes to the underlying connection to prevent data
	// interleaving from concurrent goroutines.
	mu sync.Mutex
}

// compile-time interface conformance check
var _ types.StreamWriter = (*SafeStreamWriter)(nil)

// NewSafeStreamWriter creates a SafeStreamWriter wrapping an HTTP response
// writer. The caller must ensure w implements http.Flusher and set the
// appropriate SSE headers (Content-Type: text/event-stream, Cache-Control:
// no-cache, Connection: keep-alive) before passing it in.
func NewSafeStreamWriter(w http.ResponseWriter, ctx context.Context) (*SafeStreamWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not implement http.Flusher")
	}
	return &SafeStreamWriter{
		writer:  w,
		flusher: flusher,
		ctx:     ctx,
	}, nil
}

// Write marshals the event as an SSE data frame and writes it to the
// underlying connection with a 2-second timeout. After the first write
// failure (including timeout), the connection is marked broken and all
// subsequent calls return types.ErrConnectionBroken without blocking.
func (w *SafeStreamWriter) Write(event types.StreamEvent) error {
	// Fast path: connection is already known to be broken.
	if w.broken.Load() {
		return types.ErrConnectionBroken
	}

	// Check if the request context has been cancelled (client disconnect).
	select {
	case <-w.ctx.Done():
		w.broken.Store(true)
		return w.ctx.Err()
	default:
	}

	// Serialize access to the underlying writer.
	w.mu.Lock()
	defer w.mu.Unlock()

	// Re-check after acquiring the lock (another goroutine may have marked
	// it broken in the meantime).
	if w.broken.Load() {
		return types.ErrConnectionBroken
	}

	// Marshal the event to SSE format.
	data, err := event.MarshalSSE()
	if err != nil {
		return err
	}

	// Write with a 2-second timeout to prevent goroutine leaks from a
	// slow or dead client.
	done := make(chan error, 1)
	go func() {
		_, writeErr := w.writer.Write(data)
		if writeErr == nil {
			w.flusher.Flush()
		}
		done <- writeErr
	}()

	select {
	case writeErr := <-done:
		if writeErr != nil {
			// One write failure marks the connection as broken forever.
			w.broken.Store(true)
		}
		return writeErr
	case <-w.ctx.Done():
		w.broken.Store(true)
		return w.ctx.Err()
	case <-time.After(2 * time.Second):
		w.broken.Store(true)
		return types.ErrWriteTimeout
	}
}

// IsBroken reports whether the writer has detected a broken connection.
func (w *SafeStreamWriter) IsBroken() bool {
	return w.broken.Load()
}
