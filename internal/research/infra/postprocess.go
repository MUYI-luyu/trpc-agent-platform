package infra

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// PostProcessResult holds the output of the post-processing pipeline.
type PostProcessResult struct {
	Report   string   // The report with appended warnings (if any)
	Warnings []string // All warnings generated during post-processing
}

// ─── Pipeline ────────────────────────────────────────────────────────────

// PostProcessReport runs the complete post-processing pipeline on a report.
// All checks are rule-based and execute in < 15ms total.
//
// Pipeline order:
//  1. Number validation — check that reported numbers appear in sources
//  2. Entity existence — check that technical terms appear in sources
//  3. Source timeliness — flag sources older than 2 years (Phase 3 only)
func PostProcessReport(report string, messagesContent []string) *PostProcessResult {
	result := &PostProcessResult{Report: report}

	// 1. Number validation.
	numWarnings := ValidateNumbers(report, messagesContent)
	result.Warnings = append(result.Warnings, numWarnings...)

	// 2. Entity existence.
	entityWarnings := ValidateEntities(report, messagesContent)
	result.Warnings = append(result.Warnings, entityWarnings...)

	// 3. Source timeliness (if we have dates in the messages).
	timeWarnings := CheckSourceTimeliness(report, messagesContent)
	result.Warnings = append(result.Warnings, timeWarnings...)

	// Append all warnings to the report.
	if len(result.Warnings) > 0 {
		result.Report += "\n\n---\n## 验证警告\n\n"
		for _, w := range result.Warnings {
			result.Report += fmt.Sprintf("- %s\n", w)
		}
	}

	return result
}

// ─── Number validation ───────────────────────────────────────────────────

// numberPattern matches numbers with optional units.
var numberPattern = regexp.MustCompile(`\d+\.?\d*\s*(?:ms|秒|min|分钟|MB|GB|KB|bps|%|个|次|年|元|[A-Za-z]*)`)

// ValidateNumbers checks that numeric values in the report have corresponding
// matches in the source messages.
func ValidateNumbers(report string, messages []string) []string {
	nums := numberPattern.FindAllString(report, -1)
	if len(nums) == 0 {
		return nil
	}

	// Deduplicate and build a combined source corpus.
	sourceText := strings.Join(messages, " ")
	var warnings []string

	for _, num := range unique(nums, 20) {
		// Check each number against the full source corpus.
		if !strings.Contains(sourceText, num) {
			warnings = append(warnings, fmt.Sprintf("[⚠️ 数值验证] 报告中的数值 \"%s\" 未在来源中找到对应证据", num))
		}
	}
	return warnings
}

// ─── Entity existence validation ─────────────────────────────────────────

// techTermDict is a domain-specific dictionary of technical terms.
// Terms are grouped by domain for maintainability.
var techTermDict = map[string][]string{
	"distributed": {"Raft", "Paxos", "etcd", "ZooKeeper", "Chubby", "gRPC", "Thrift", "Consul", "Nomad"},
	"storage":     {"LSM-Tree", "B+Tree", "WAL", "SSTable", "Bloom Filter", "Compaction", "MemTable", "BoltDB", "LevelDB", "RocksDB"},
	"framework":   {"tRPC", "tRPC-Agent-Go", "LangGraph", "MCP", "SSE", "WebSocket"},
	"protocols":   {"HTTP/2", "HTTP/3", "QUIC", "TCP", "UDP", "Protobuf", "FlatBuffers", "Cap'n Proto"},
}

// capitalizedPattern matches capitalized technical terms (e.g., "Raft", "Paxos").
var capitalizedPattern = regexp.MustCompile(`\b[A-Z][a-zA-Z0-9]*(?:[- ][A-Z][a-zA-Z0-9]*)*\b`)

// ValidateEntities checks that technical entities mentioned in the report
// actually appear in the source messages.
func ValidateEntities(report string, messages []string) []string {
	sourceText := strings.Join(messages, " ")

	// Build flat term list from dictionary.
	termSet := make(map[string]bool, 100)
	for _, terms := range techTermDict {
		for _, t := range terms {
			termSet[t] = true
		}
	}

	// Find all capitalized terms in the report.
	candidates := capitalizedPattern.FindAllString(report, -1)
	var warnings []string
	seen := make(map[string]bool)

	for _, term := range unique(candidates, 50) {
		if seen[term] {
			continue
		}
		// Only check terms present in our dictionary (avoid false positives
		// on generic capitalized words like "The", "Note", etc.).
		if !termSet[term] {
			continue
		}
		seen[term] = true

		if !strings.Contains(sourceText, term) {
			warnings = append(warnings, fmt.Sprintf("[⚠️ 实体验证] 报告中的技术术语 \"%s\" 未在来源中确认出现", term))
		}
	}
	return warnings
}

// ─── Source timeliness check ─────────────────────────────────────────────

// datePattern matches common date formats in source text.
var datePattern = regexp.MustCompile(`(20\d{2})[-/年](\d{1,2})[-/月]`)

// maxSourceAge is the threshold beyond which a source is flagged as stale.
const maxSourceAge = 2 * 365 * 24 * time.Hour // 2 years

// CheckSourceTimeliness flags sources that are older than 2 years.
func CheckSourceTimeliness(_ string, messages []string) []string {
	var warnings []string
	now := time.Now()
	seen := make(map[string]bool)

	for _, msg := range messages {
		matches := datePattern.FindAllStringSubmatch(msg, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			dateStr := m[0]
			if seen[dateStr] {
				continue
			}
			seen[dateStr] = true

			// Try to parse the date.
			t, err := parseApproxDate(m[1], m[2])
			if err != nil {
				continue
			}
			if now.Sub(t) > maxSourceAge {
				warnings = append(warnings, fmt.Sprintf("[📅 时效性] 来源中包含 %s 的数据，可能已过时（距今超过 2 年）", dateStr))
			}
		}
	}
	return warnings
}

// ─── Helpers ─────────────────────────────────────────────────────────────

// unique returns up to max unique non-empty strings.
func unique(items []string, max int) []string {
	seen := make(map[string]bool, max)
	result := make([]string, 0, max)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
		if len(result) >= max {
			break
		}
	}
	return result
}

func parseApproxDate(year, month string) (time.Time, error) {
	// Simplified parsing: "2024-03" or "2024年3月" → approximate date.
	var y, m int
	if _, err := fmt.Sscanf(year, "%d", &y); err != nil {
		return time.Time{}, fmt.Errorf("parse year %q: %w", year, err)
	}
	if _, err := fmt.Sscanf(month, "%d", &m); err != nil {
		return time.Time{}, fmt.Errorf("parse month %q: %w", month, err)
	}
	if y < 2000 || m < 1 || m > 12 {
		return time.Time{}, fmt.Errorf("invalid date: %s-%s", year, month)
	}
	return time.Date(y, time.Month(m), 15, 0, 0, 0, 0, time.UTC), nil
}
