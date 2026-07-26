package llm

import (
	"fmt"
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

// bigDiff builds a structurally real single-file unified diff (header +
// hunk) with n added lines. DiffLineStats only counts lines inside a hunk
// (after "@@"), so bare "+..." lines without a header would count as zero.
func bigDiff(lines int) string {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", lines)
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
			diff:    "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n line\n+one line\n",
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

// TestHeuristicRiskStagedSensitivePath: with no commit metadata (the staged
// review case — nothing is committed yet), a sensitive path must still be
// detected from the diff's own "diff --git" headers, not silently missed.
func TestHeuristicRiskStagedSensitivePath(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/internal/auth/token.go b/internal/auth/token.go\n" +
		"new file mode 100644\n--- /dev/null\n+++ b/internal/auth/token.go\n+package auth\n"
	got := HeuristicRisk(nil, diff)
	if got.Level != RiskHigh {
		t.Errorf("level = %q, want %q (summary: %q)", got.Level, RiskHigh, got.Summary)
	}
	if !strings.Contains(got.Summary, "auth") && !strings.Contains(got.Summary, "token") {
		t.Errorf("summary should name the sensitive keyword, got: %q", got.Summary)
	}
}

// TestHeuristicRiskStagedNoSensitivePath: staged mode with no commits and no
// sensitive path in the diff must not spuriously trigger the keyword gate.
func TestHeuristicRiskStagedNoSensitivePath(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n+hello\n"
	got := HeuristicRisk(nil, diff)
	if got.Level != RiskLow {
		t.Errorf("level = %q, want %q (summary: %q)", got.Level, RiskLow, got.Summary)
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
		{
			name:      "trailing space after language tag",
			content:   "body\n\n```risk \n{\"level\": \"medium\", \"summary\": \"trailing space\"}\n```\n",
			wantOK:    true,
			wantLevel: "medium",
		},
		{
			name:      "trailing tab after language tag",
			content:   "body\n\n```risk\t\n{\"level\": \"low\", \"summary\": \"trailing tab\"}\n```\n",
			wantOK:    true,
			wantLevel: "low",
		},
		{
			name:      "CRLF with trailing space after language tag",
			content:   "body\r\n\r\n```risk \r\n{\"level\": \"high\", \"summary\": \"crlf\"}\r\n```\r\n",
			wantOK:    true,
			wantLevel: "high",
		},
		{
			name:    "json-tagged block with trailing space still rejected",
			content: "prose\n\n```json \n{\"level\": \"low\", \"summary\": \"JSONTAG_SPACE_MARKER\"}\n```",
			wantOK:  false,
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

// TestSensitiveKeywordWordBoundary: keyword detection must match whole tokens,
// not substrings — "auth" hits "auth.go"/"auth_token" but NOT "author"/
// "AUTHORS.md"/"authored". Guards the false-HIGH bug where a changelog edit
// naming an author, or an AUTHORS.md file, was scored HIGH.
func TestSensitiveKeywordWordBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		hit  bool
	}{
		{"auth as path segment", "internal/auth/x.go", true},
		{"auth as filename token", "auth.go", true},
		{"auth snake_case token", "auth_token.go", true},
		{"auth kebab token", "auth-service.go", true},
		{"author is not auth", "CHANGELOG-author.md", false},
		{"authored is not auth", "docs/authored.md", false},
		{"AUTHORS file is not auth", "AUTHORS.md", false},
		{"oauth is not auth", "oauth2client.go", false},
		{"security token", "internal/security/perm.go", true},
		{"payment plural not exact", "payments.go", false},
		{"payment exact", "payment.go", true},
		{"benign readme", "README.md", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := HeuristicRisk(commitsWith(gitlog.FileChange{Status: "M", Path: tc.path}), "+x\n")
			isHigh := got.Level == RiskHigh
			if isHigh != tc.hit {
				t.Errorf("path %q: HIGH=%v, want %v (level=%q, summary=%q)", tc.path, isHigh, tc.hit, got.Level, got.Summary)
			}
		})
	}
}
