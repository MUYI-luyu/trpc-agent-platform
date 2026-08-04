package infra

import (
	"fmt"
)

// Truncation limits (in tokens) for each tool. These are enforced at the
// framework level before tool results are written to Messages.
const (
	TruncLimitSearchKB  = 1500 // tokens per document
	TruncLimitWebSearch = 200  // tokens per search result item
	TruncLimitWebFetch  = 3000 // tokens per fetched page
)

// TruncLimits maps tool names to their token limits.
var TruncLimits = map[string]int{
	"search_kb":  TruncLimitSearchKB,
	"web_search": TruncLimitWebSearch,
	"web_fetch":  TruncLimitWebFetch,
}

// EnforceTruncation is the framework-level gate. It must be called before
// any tool result is written to Messages. If the content's estimated token
// count exceeds the limit for the given tool, the content is truncated and
// a truncation marker is appended.
func EnforceTruncation(toolName, content string) string {
	limit, ok := TruncLimits[toolName]
	if !ok {
		// Unknown tool — pass through without truncation.
		return content
	}
	tokenCount := EstimateTokens(content)
	if tokenCount <= limit {
		return content
	}
	truncated := TruncateContent(content, limit)
	return truncated + fmt.Sprintf(" [内容已截断，原始长度 %d tokens]", tokenCount)
}

// EstimateTokens provides a rough token count heuristic.
// English: ~4 characters per token. CJK: ~1.5 characters per token.
// This is a heuristic — for production use, a proper tokenizer (e.g.,
// tiktoken) should be used.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	englishChars := 0
	cjkChars := 0
	for _, r := range s {
		if isCJK(r) {
			cjkChars++
		} else {
			englishChars++
		}
	}
	return englishChars/4 + cjkChars*2/3
}

// TruncateContent truncates content to approximately limit tokens while
// preserving whole characters and preferring sentence boundaries.
func TruncateContent(content string, limitTokens int) string {
	if limitTokens <= 0 {
		return ""
	}

	// Convert token limit to approximate byte/character limit.
	// Use a conservative estimate: 2 chars per token for mixed content.
	charLimit := limitTokens * 2

	runes := []rune(content)
	if len(runes) <= charLimit {
		return content
	}

	// Try to truncate at a sentence boundary within the last 20% of the limit.
	truncPoint := charLimit
	searchStart := charLimit * 4 / 5
	if searchStart < 0 {
		searchStart = 0
	}

	for i := truncPoint - 1; i >= searchStart; i-- {
		r := runes[i]
		if r == '.' || r == '。' || r == '！' || r == '？' || r == '\n' {
			return string(runes[:i+1])
		}
	}

	// Fallback: truncate at char limit.
	return string(runes[:truncPoint])
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Unified Ideographs Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Unified Ideographs Extension B
		(r >= 0xF900 && r <= 0xFAFF) || // CJK Compatibility Ideographs
		(r >= 0x3040 && r <= 0x309F) || // Hiragana
		(r >= 0x30A0 && r <= 0x30FF) || // Katakana
		(r >= 0xAC00 && r <= 0xD7AF) // Hangul Syllables
}

// ─── Unicode helpers ─────────────────────────────────────────────────────
