package infra

import (
	"strings"
	"testing"

)

func TestValidateNumbers_Found(t *testing.T) {
	report := "Raft 选举超时通常为 150ms 到 300ms。"
	messages := []string{
		"Raft uses a randomized election timeout of 150ms to 300ms",
	}

	warnings := ValidateNumbers(report, messages)
	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

func TestValidateNumbers_NotFound(t *testing.T) {
	report := "Raft 选举超时通常为 150ms。"
	messages := []string{
		"Raft uses a randomized election timeout of 300ms", // different number
	}

	warnings := ValidateNumbers(report, messages)
	if len(warnings) == 0 {
		t.Error("expected warning for 150ms not found in sources")
	}
}

func TestValidateNumbers_NoNumbers(t *testing.T) {
	report := "Raft is a consensus algorithm."
	messages := []string{"Some source text"}

	warnings := ValidateNumbers(report, messages)
	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

func TestValidateEntities_Found(t *testing.T) {
	report := "Raft is used by etcd for consensus."
	messages := []string{
		"Raft is a consensus algorithm. etcd uses Raft for distributed coordination.",
	}

	warnings := ValidateEntities(report, messages)
	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

func TestValidateEntities_NotFound(t *testing.T) {
	report := "etcd uses BoltDB as its storage engine."
	messages := []string{
		"etcd uses Raft for consensus.",
		// BoltDB not mentioned.
	}

	warnings := ValidateEntities(report, messages)
	hasBoltDB := false
	for _, w := range warnings {
		if strings.Contains(w, "BoltDB") {
			hasBoltDB = true
		}
	}
	if !hasBoltDB {
		t.Error("expected warning about BoltDB not found in sources")
	}
}

func TestValidateEntities_IgnoresNonDict(t *testing.T) {
	report := "The Quick Brown Fox jumps over the Lazy Dog."
	messages := []string{"source"}

	warnings := ValidateEntities(report, messages)
	// "Quick", "Brown", etc. are not in techTermDict — should not generate warnings.
	if len(warnings) > 0 {
		t.Errorf("expected no warnings for non-tech terms, got %v", warnings)
	}
}

func TestValidateEntities_Deduplication(t *testing.T) {
	// "Raft" appears multiple times in the report but not at all in sources.
	report := "Raft is used. Raft works well. Raft is fast."
	messages := []string{"no matching term here"}

	warnings := ValidateEntities(report, messages)
	// Should warn about Raft exactly once (deduplicated).
	raftCount := 0
	for _, w := range warnings {
		if strings.Contains(w, "Raft") {
			raftCount++
		}
	}
	if raftCount > 1 {
		t.Errorf("expected at most 1 warning for Raft, got %d: %v", raftCount, warnings)
	}
	if raftCount == 0 {
		t.Error("expected warning about Raft not found in sources")
	}
}

func TestCheckSourceTimeliness_Old(t *testing.T) {
	messages := []string{
		"Published 2019-03-15. This paper discusses Raft.",
	}

	warnings := CheckSourceTimeliness("", messages)
	if len(warnings) == 0 {
		t.Error("expected warning for 2019 source (more than 2 years old)")
	}
}

func TestCheckSourceTimeliness_Recent(t *testing.T) {
	messages := []string{
		"Published 2026-01-10. Latest Raft implementation.",
	}

	warnings := CheckSourceTimeliness("", messages)
	if len(warnings) > 0 {
		t.Errorf("expected no warnings for recent source, got %v", warnings)
	}
}

func TestPostProcessReport_Pipeline(t *testing.T) {
	report := "Raft election timeout is 150ms. etcd uses BoltDB for storage."
	messages := []string{
		"Source: Raft election timeout is 300ms. Etcd uses Raft.",
	}

	result := PostProcessReport(report, messages)
	if len(result.Warnings) == 0 {
		t.Error("expected warnings")
	}
	// Report should have warnings appended.
	if !strings.Contains(result.Report, "验证警告") {
		t.Error("report should contain warning section")
	}
}

func TestUnique(t *testing.T) {
	items := []string{"a", "b", "a", "c", "", "b"}
	got := unique(items, 10)
	if len(got) != 3 {
		t.Errorf("expected 3 unique items, got %d: %v", len(got), got)
	}
}

