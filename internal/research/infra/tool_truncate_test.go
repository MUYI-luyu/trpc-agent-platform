package infra

import (
	"fmt"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		wantMin int
		wantMax int
	}{
		{"empty", "", 0, 0},
		{"english short", "hello world", 2, 4},
		{"english sentence", "The quick brown fox jumps over the lazy dog", 7, 12},
		{"chinese short", "你好世界", 2, 4},
		{"mixed", "Raft使用Leader Election机制", 5, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.text)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("EstimateTokens(%q) = %d, want between %d and %d", tt.text, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestEnforceTruncation(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		content := "short content"
		got := EnforceTruncation("search_kb", content)
		if got != content {
			t.Errorf("content should not be truncated: got %q", got)
		}
	})

	t.Run("exceeds limit", func(t *testing.T) {
		// Generate content that exceeds 1500 tokens.
		longContent := ""
		for i := 0; i < 10000; i++ {
			longContent += "test "
		}
		got := EnforceTruncation("search_kb", longContent)
		if len(got) >= len(longContent) {
			t.Error("content should be truncated")
		}
		if !contains(got, "[内容已截断") {
			t.Error("truncated content should have truncation marker")
		}
	})

	t.Run("unknown tool", func(t *testing.T) {
		content := "some content"
		got := EnforceTruncation("unknown_tool", content)
		if got != content {
			t.Errorf("unknown tool content should not be truncated: got %q", got)
		}
	})

	t.Run("web fetch head tail", func(t *testing.T) {
		// Create content longer than web_fetch limit.
		longContent := ""
		for i := 0; i < 20000; i++ {
			longContent += "test content. "
		}
		got := enforceWebFetchTruncation(longContent)
		if len(got) >= len(longContent) {
			t.Error("web fetch content should be truncated")
		}
		if !contains(got, "中间部分已截断") {
			t.Error("web fetch truncation should have middle truncation marker")
		}
	})
}

func TestTruncateContent(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		content := "Hello. World."
		got := TruncateContent(content, 100)
		if got != content {
			t.Errorf("content should not be truncated: got %q", got)
		}
	})

	t.Run("at sentence boundary", func(t *testing.T) {
		// Create content with sentence boundaries.
		content := "First sentence. Second sentence. Third sentence. Fourth. Fifth. Sixth."
		limitTokens := 5 // ~10 chars
		got := TruncateContent(content, limitTokens)
		if len(got) >= len(content) {
			t.Error("content should be truncated")
		}
	})

	t.Run("no sentence boundary", func(t *testing.T) {
		content := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
		limitTokens := 3
		got := TruncateContent(content, limitTokens)
		if len([]rune(got)) > limitTokens*2 {
			t.Errorf("truncated content too long: %d chars for limit %d tokens", len([]rune(got)), limitTokens)
		}
	})
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ─── parseDuckDuckGoLite tests ──────────────────────────────────────────

func TestParseDuckDuckGoLite_RealHTML(t *testing.T) {
	// Sample HTML matching the actual DuckDuckGo Lite format (August 2026).
	// Key characteristics:
	//   1. href= comes BEFORE class='result-link' in the <a> tag
	//   2. All external URLs are wrapped in //duckduckgo.com/l/?uddg=<encoded>
	//   3. Snippets are in separate <tr> rows with class='result-snippet'
	html := `<!DOCTYPE html><html><body>
<table border="0">
<tr>
  <td valign="top">1.&nbsp;</td>
  <td>
    <a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Farticle&amp;rut=abc123" class='result-link'>Example Article Title</a>
  </td>
</tr>
<tr>
  <td>&nbsp;&nbsp;&nbsp;</td>
  <td class='result-snippet'>
    This is the <b>snippet</b> text for the first result.
  </td>
</tr>
<tr><td>&nbsp;</td><td>&nbsp;</td></tr>
<tr>
  <td valign="top">2.&nbsp;</td>
  <td>
    <a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Ftest.org%2Fpage&amp;rut=def456" class='result-link'>Second &amp; Result</a>
  </td>
</tr>
<tr>
  <td>&nbsp;&nbsp;&nbsp;</td>
  <td class='result-snippet'>
    Another snippet with <b>bold</b> formatting.
  </td>
</tr>
</table>
</body></html>`

	results := parseDuckDuckGoLite(html)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Result 1.
	if results[0].Title != "Example Article Title" {
		t.Errorf("result[0].Title = %q, want %q", results[0].Title, "Example Article Title")
	}
	if results[0].URL != "https://example.com/article" {
		t.Errorf("result[0].URL = %q, want %q", results[0].URL, "https://example.com/article")
	}
	if results[0].Snippet != "This is the snippet text for the first result." {
		t.Errorf("result[0].Snippet = %q", results[0].Snippet)
	}

	// Result 2 — title should have &amp; unescaped.
	if results[1].Title != "Second & Result" {
		t.Errorf("result[1].Title = %q, want %q", results[1].Title, "Second & Result")
	}
	if results[1].URL != "https://test.org/page" {
		t.Errorf("result[1].URL = %q, want %q", results[1].URL, "https://test.org/page")
	}
}

func TestParseDuckDuckGoLite_EmptyHTML(t *testing.T) {
	results := parseDuckDuckGoLite("")
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty HTML, got %d", len(results))
	}
}

func TestParseDuckDuckGoLite_NoResults(t *testing.T) {
	html := `<html><body><p>No results found.</p></body></html>`
	results := parseDuckDuckGoLite(html)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestParseDuckDuckGoLite_MaxFive(t *testing.T) {
	// Build HTML with 8 results — should return at most 5.
	var b strings.Builder
	b.WriteString("<html><body><table>")
	for i := 0; i < 8; i++ {
		b.WriteString(fmt.Sprintf(`
<tr><td valign="top">%d.&nbsp;</td><td>
<a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%%3A%%2F%%2Fsite%d.com&amp;rut=x" class='result-link'>Title %d</a>
</td></tr>
<tr><td>&nbsp;&nbsp;&nbsp;</td><td class='result-snippet'>Snippet %d</td></tr>
<tr><td>&nbsp;</td><td>&nbsp;</td></tr>`, i+1, i, i, i))
	}
	b.WriteString("</table></body></html>")

	results := parseDuckDuckGoLite(b.String())
	if len(results) != 5 {
		t.Errorf("expected max 5 results, got %d", len(results))
	}
}

func TestExtractDDGURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "standard DDG redirect",
			in:   "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpath&amp;rut=abc123",
			want: "https://example.com/path",
		},
		{
			name: "non-DDG URL passed through",
			in:   "https://direct-link.com/page",
			want: "https://direct-link.com/page",
		},
		{
			name: "uddg with semicolon separator",
			in:   "//duckduckgo.com/l/?uddg=https%3A%2F%2Ftest.org;rut=xyz",
			want: "https://test.org",
		},
		{
			name: "no uddg parameter",
			in:   "//duckduckgo.com/l/?q=test",
			want: "//duckduckgo.com/l/?q=test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDDGURL(tt.in)
			if got != tt.want {
				t.Errorf("extractDDGURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
