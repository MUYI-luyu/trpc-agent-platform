package types

import "time"

// Config holds tunable parameters for the Research Agent nodes.
// All fields have sensible defaults. A nil Config is equivalent to DefaultConfig().
type Config struct {
	// ClarifyTemperature controls randomness in the Clarify LLM call.
	// Default: 0.1
	ClarifyTemperature float64

	// ClarifyMaxTokens limits the Clarify LLM response length.
	// Default: 1024
	ClarifyMaxTokens int

	// ClarifyConfidenceMin is the logprobs confidence threshold below which
	// an "answer" action is downgraded to "research".
	// Default: 0.85
	ClarifyConfidenceMin float64

	// SynthesizeTemperature controls randomness in the Synthesize LLM call.
	// Default: 0.3
	SynthesizeTemperature float64

	// SynthesizeMaxTokens limits the Synthesize LLM response length.
	// Default: 4096
	SynthesizeMaxTokens int

	// SSETimeout is the per-write deadline for the SafeStreamWriter.
	// Default: 2s
	SSETimeout time.Duration

	// LockTimeout is the maximum time to wait for a session lock.
	// Default: 30s
	LockTimeout time.Duration

	// MaxSourceAge is the threshold for flagging stale sources during
	// post-processing. Sources older than this are marked as potentially
	// out-of-date.
	// Default: 2 years (17520h)
	MaxSourceAge time.Duration

	// MaxRounds is the default maximum number of ReAct search rounds.
	// 0 means use DefaultMaxRounds (from state.go).
	MaxRounds int

	// TruncateKB is the token limit for search_kb tool results.
	// Default: 1500
	TruncateKB int

	// TruncateWebSearch is the token limit for web_search tool results.
	// Default: 200
	TruncateWebSearch int

	// TruncateWebFetch is the token limit for web_fetch tool results.
	// Default: 3000
	TruncateWebFetch int
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		ClarifyTemperature:    0.1,
		ClarifyMaxTokens:      1024,
		ClarifyConfidenceMin:  0.85,
		SynthesizeTemperature: 0.3,
		SynthesizeMaxTokens:   4096,
		SSETimeout:            2 * time.Second,
		LockTimeout:           30 * time.Second,
		MaxSourceAge:          17520 * time.Hour, // 2 years
		MaxRounds:             0,                 // use DefaultMaxRounds
		TruncateKB:            1500,
		TruncateWebSearch:     200,
		TruncateWebFetch:      3000,
	}
}

// EffectiveMaxRounds returns the max rounds, falling back to DefaultMaxRounds
// when the config value is zero.
func (c *Config) EffectiveMaxRounds() int {
	if c == nil || c.MaxRounds <= 0 {
		return DefaultMaxRounds
	}
	return c.MaxRounds
}

// EffectiveClarifyTemperature returns the Clarify temperature, falling back
// when the config is nil or the value is unset (0.0).
func (c *Config) EffectiveClarifyTemperature() float64 {
	if c == nil || c.ClarifyTemperature == 0.0 {
		return 0.1
	}
	return c.ClarifyTemperature
}

// EffectiveClarifyConfidenceMin returns the Clarify confidence threshold,
// falling back when the config is nil or the value is unset (0.0).
func (c *Config) EffectiveClarifyConfidenceMin() float64 {
	if c == nil || c.ClarifyConfidenceMin == 0.0 {
		return 0.85
	}
	return c.ClarifyConfidenceMin
}

// EffectiveClarifyMaxTokens returns the Clarify max tokens, falling back
// when the config is nil or the value is unset (0).
func (c *Config) EffectiveClarifyMaxTokens() int {
	if c == nil || c.ClarifyMaxTokens <= 0 {
		return 1024
	}
	return c.ClarifyMaxTokens
}

// EffectiveSynthesizeTemperature returns the Synthesize temperature.
func (c *Config) EffectiveSynthesizeTemperature() float64 {
	if c == nil || c.SynthesizeTemperature == 0.0 {
		return 0.3
	}
	return c.SynthesizeTemperature
}

// EffectiveSynthesizeMaxTokens returns the Synthesize max tokens.
func (c *Config) EffectiveSynthesizeMaxTokens() int {
	if c == nil || c.SynthesizeMaxTokens <= 0 {
		return 4096
	}
	return c.SynthesizeMaxTokens
}
