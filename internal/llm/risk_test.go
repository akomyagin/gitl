package llm

import (
	"strings"
	"testing"

	"github.com/akomyagin/gitl/internal/gitlog"
)

func commitsWith(files ...gitlog.FileChange) []gitlog.Commit {
	return []gitlog.Commit{{Subject: "chore: work", Files: files}}
}

func manyFiles(n int) []gitlog.FileChange {
	fs := make([]gitlog.FileChange, n)
	for i := range fs {
		fs[i] = gitlog.FileChange{Status: "M", Path: "pkg/file" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".go"}
	}
	return fs
}

func bigDiff(lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString("+added line\n")
	}
	return b.String()
}

// manyFileDiff builds a minimal unified diff with n distinct "diff --git" sections.
func manyFileDiff(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		name := "pkg/file" + string(rune('a'+i%26)) + ".go"
		b.WriteString("diff --git a/" + name + " b/" + name + "\n+line\n")
	}
	return b.String()
}

func TestHeuristicRisk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		commits []gitlog.Commit
		diff    string
		want    string
	}{
		{
			name:    "low: tiny contained change",
			commits: commitsWith(gitlog.FileChange{Status: "M", Path: "README.md"}),
			diff:    "--- a/README.md\n+++ b/README.md\n+one line\n",
			want:    RiskLow,
		},
		{
			name:    "high: sensitive keyword in path",
			commits: commitsWith(gitlog.FileChange{Status: "M", Path: "internal/auth/token.go"}),
			diff:    "+x\n",
			want:    RiskHigh,
		},
		{
			name:    "high: many changed lines",
			commits: commitsWith(gitlog.FileChange{Status: "M", Path: "big.go"}),
			diff:    bigDiff(301),
			want:    RiskHigh,
		},
		{
			name:    "high: many files",
			commits: commitsWith(manyFiles(21)...),
			diff:    manyFileDiff(21),
			want:    RiskHigh,
		},
		{
			name:    "medium: default middle ground",
			commits: commitsWith(manyFiles(5)...),
			diff:    bigDiff(50),
			want:    RiskMedium,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := HeuristicRisk(tc.commits, tc.diff)
			if got.Level != tc.want {
				t.Errorf("level = %q, want %q (summary: %q)", got.Level, tc.want, got.Summary)
			}
			if got.Summary == "" {
				t.Error("summary must not be empty")
			}
		})
	}
}

func TestHeuristicRiskDeterministic(t *testing.T) {
	t.Parallel()
	c := commitsWith(gitlog.FileChange{Status: "M", Path: "internal/security/perm.go"})
	a := HeuristicRisk(c, "+x\n")
	b := HeuristicRisk(c, "+x\n")
	if a != b {
		t.Errorf("HeuristicRisk not deterministic: %+v vs %+v", a, b)
	}
}

func TestParseRisk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		content   string
		wantOK    bool
		wantLevel string
	}{
		{
			name:      "valid risk block",
			content:   "## Summary\n\nbody\n\n```risk\n{\"level\": \"medium\", \"summary\": \"touches auth\"}\n```\n",
			wantOK:    true,
			wantLevel: "medium",
		},
		{
			name:      "level case-insensitive",
			content:   "prose\n\n```risk\n{\"level\": \"HIGH\", \"summary\": \"UPPERCASE_MARKER\"}\n```",
			wantOK:    true,
			wantLevel: "high",
		},
		{
			name:    "json-tagged block rejected",
			content: "prose\n\n```json\n{\"level\": \"low\", \"summary\": \"JSONTAG_MARKER\"}\n```",
			wantOK:  false,
		},
		{
			name:    "missing block",
			content: "## Summary\n\njust prose, no fenced risk\n",
			wantOK:  false,
		},
		{
			name:    "malformed json",
			content: "body\n\n```risk\n{not json}\n```",
			wantOK:  false,
		},
		{
			name:    "invalid level",
			content: "body\n\n```risk\n{\"level\": \"critical\", \"summary\": \"x\"}\n```",
			wantOK:  false,
		},
		{
			name:      "last valid block wins",
			content:   "```risk\n{\"level\":\"low\",\"summary\":\"a\"}\n```\nmore\n```risk\n{\"level\":\"high\",\"summary\":\"b\"}\n```",
			wantOK:    true,
			wantLevel: "high",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stripped, risk, ok := ParseRisk(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if risk.Level != tc.wantLevel {
				t.Errorf("level = %q, want %q", risk.Level, tc.wantLevel)
			}
			// The summary of the chosen block must not remain in the stripped
			// output (only the winning block is removed; malformed sibling
			// blocks, if any, may legitimately remain).
			if strings.Contains(stripped, risk.Summary) && risk.Summary != "" {
				t.Errorf("chosen risk block not stripped (summary %q still present):\n%s", risk.Summary, stripped)
			}
		})
	}
}

func TestRiskAtLeast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level, threshold string
		want             bool
	}{
		{"high", "high", true},
		{"medium", "high", false},
		{"high", "medium", true},
		{"low", "low", true},
		{"HIGH", "high", true}, // case-insensitive
		{"high", "never", false},
		{"low", "never", false},
	}
	for _, tc := range tests {
		if got := RiskAtLeast(tc.level, tc.threshold); got != tc.want {
			t.Errorf("RiskAtLeast(%q, %q) = %v, want %v", tc.level, tc.threshold, got, tc.want)
		}
	}
}
