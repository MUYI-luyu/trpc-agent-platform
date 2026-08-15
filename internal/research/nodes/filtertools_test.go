package nodes

import (
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// fakeTool is a minimal tool.Tool implementation for testing filterTools.
type fakeTool struct {
	name string
}

func (f fakeTool) Declaration() *tool.Declaration {
	return &tool.Declaration{Name: f.name}
}

func TestFilterTools(t *testing.T) {
	all := map[string]tool.Tool{
		"web_search": fakeTool{name: "web_search"},
		"web_fetch":  fakeTool{name: "web_fetch"},
		"search_kb":  fakeTool{name: "search_kb"},
	}

	tests := []struct {
		name    string
		allowed []string
		want    []string
	}{
		{
			name:    "subset",
			allowed: []string{"web_search", "web_fetch"},
			want:    []string{"web_search", "web_fetch"},
		},
		{
			name:    "empty allowlist yields no tools",
			allowed: []string{},
			want:    []string{},
		},
		{
			name:    "full allowlist",
			allowed: []string{"web_search", "web_fetch", "search_kb"},
			want:    []string{"web_search", "web_fetch", "search_kb"},
		},
		{
			name:    "unknown names ignored",
			allowed: []string{"web_search", "host_exec"},
			want:    []string{"web_search"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterTools(all, tt.allowed)
			if len(got) != len(tt.want) {
				t.Fatalf("filterTools() len = %d, want %d (got keys %v)", len(got), len(tt.want), keys(got))
			}
			for _, name := range tt.want {
				if _, ok := got[name]; !ok {
					t.Errorf("filterTools() missing tool %q (got keys %v)", name, keys(got))
				}
			}
		})
	}
}

func keys(m map[string]tool.Tool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
