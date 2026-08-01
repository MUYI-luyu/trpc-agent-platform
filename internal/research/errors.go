package research

import "fmt"

// Sentinel errors for the Research Agent.
var (
	// ErrConnectionBroken indicates the SSE client connection is no longer active.
	ErrConnectionBroken = fmt.Errorf("stream connection broken")
	// ErrWriteTimeout indicates an SSE write exceeded the timeout.
	ErrWriteTimeout = fmt.Errorf("stream write timeout")
	// ErrToolsAllFailed indicates all tools failed in a round and the
	// consecutive failure threshold has been reached.
	ErrToolsAllFailed = fmt.Errorf("all tools failed, research interrupted")
	// ErrSessionLocked indicates the session is currently processing another
	// request.
	ErrSessionLocked = fmt.Errorf("session is currently processing another request")
	// ErrMaxRoundsReached indicates the loop was stopped by the hard limit.
	ErrMaxRoundsReached = fmt.Errorf("maximum research rounds reached")
)
