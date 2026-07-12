package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// Risk levels, lowest → highest. never is not a level a review can carry — it
// is only a --fail-on threshold meaning "never fail" — but it participates in
// the ordering below.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// riskOrder ranks levels for --fail-on comparisons (§6). "never" sorts below
// everything so a "never" threshold is never met by any real risk level.
var riskOrder = map[string]int{
	"never":    0,
	RiskLow:    1,
	RiskMedium: 2,
	RiskHigh:   3,
}

// ValidRiskLevel reports whether level is one of low|medium|high.
func ValidRiskLevel(level string) bool {
	switch level {
	case RiskLow, RiskMedium, RiskHigh:
		return true
	default:
		return false
	}
}

// RiskAtLeast reports whether level meets or exceeds threshold in the ordering
// low < medium < high, with "never" below all of them. Both arguments are
// compared case-insensitively. An unrecognized threshold is treated as "never"
// (never fail) — the safe direction, since an unknown level's zero-value rank
// would otherwise sit below every real risk level and make this always true.
// config.Config.validate() rejects bad policy.fail_on values before they ever
// reach here; this is a defense-in-depth guard for other callers.
func RiskAtLeast(level, threshold string) bool {
	t, known := riskOrder[strings.ToLower(strings.TrimSpace(threshold))]
	if !known || strings.ToLower(strings.TrimSpace(threshold)) == "never" {
		return false
	}
	l := riskOrder[strings.ToLower(strings.TrimSpace(level))]
	return l >= t
}

// sensitiveKeywords are the security/risk-relevant substrings that push the
// heuristic toward "high" (§7.3, case-insensitive match against file paths,
// commit subjects and bodies).
var sensitiveKeywords = []string{
	"auth", "secret", "password", "token", "credential",
	"security", "payment", "migration", "permission",
}

// Heuristic thresholds (§7.3). Not dogma — calibrated conservatively so the
// default gate leans toward WARN over BLOCK (see §9 risks table).
const (
	highChangedLines = 300
	highFileCount    = 20
	lowChangedLines  = 20
	lowFileCount     = 3
)

// HeuristicRisk computes a deterministic risk score from commits and diff. It is
// the single shared implementation used both by the offline provider (always)
// and by the network Client as a fallback when the model's risk block is
// missing or invalid (§7.3). Same input → same output, no randomness, no I/O.
func HeuristicRisk(commits []gitlog.Commit, diff string) Risk {
	added, removed := gitlog.DiffLineStats(diff)
	changed := added + removed
	files := gitlog.DiffFileCount(diff)
	hits := sensitiveHits(commits, diff)

	switch {
	case len(hits) > 0:
		return Risk{
			Level:   RiskHigh,
			Summary: fmt.Sprintf("Touches security-sensitive keywords: %s.", quoteList(hits)),
		}
	case changed > highChangedLines:
		return Risk{
			Level:   RiskHigh,
			Summary: fmt.Sprintf("Large diff (%d changed lines) across %d file(s).", changed, files),
		}
	case files > highFileCount:
		return Risk{
			Level:   RiskHigh,
			Summary: fmt.Sprintf("Wide-reaching change across %d files (%d changed lines).", files, changed),
		}
	case changed < lowChangedLines && files <= lowFileCount:
		return Risk{
			Level:   RiskLow,
			Summary: fmt.Sprintf("Small, contained change (%d changed lines across %d file(s)).", changed, files),
		}
	default:
		return Risk{
			Level:   RiskMedium,
			Summary: fmt.Sprintf("Moderate change (%d changed lines across %d file(s)).", changed, files),
		}
	}
}

// sensitiveHits returns the distinct sensitive keywords found in file paths,
// commit subjects and bodies, in deterministic order.
func sensitiveHits(commits []gitlog.Commit, diff string) []string {
	var haystack strings.Builder
	for _, c := range commits {
		haystack.WriteString(c.Subject)
		haystack.WriteByte('\n')
		haystack.WriteString(c.Body)
		haystack.WriteByte('\n')
		for _, f := range c.Files {
			haystack.WriteString(f.Path)
			haystack.WriteByte('\n')
			haystack.WriteString(f.Old)
			haystack.WriteByte('\n')
		}
	}
	// File paths parsed from the diff's structured "diff --git" headers are
	// the same category of signal as commit file paths, so they're scanned
	// too — this is the only path signal available for a staged review,
	// which has no commit metadata yet.
	for _, p := range gitlog.DiffPaths(diff) {
		haystack.WriteString(p)
		haystack.WriteByte('\n')
	}
	// The diff body's actual content lines are deliberately not scanned for
	// keywords: they commonly contain benign occurrences (e.g. the word
	// "token" in a comment) that would over-trigger. Paths, subjects and
	// bodies are the strong signal.
	lower := strings.ToLower(haystack.String())

	seen := map[string]bool{}
	var hits []string
	for _, kw := range sensitiveKeywords {
		if strings.Contains(lower, kw) && !seen[kw] {
			seen[kw] = true
			hits = append(hits, kw)
		}
	}
	sort.Strings(hits)
	return hits
}

// quoteList renders keywords as a comma-separated, double-quoted list.
func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, it := range items {
		quoted[i] = fmt.Sprintf("%q", it)
	}
	return strings.Join(quoted, ", ")
}

// ValidFailOnLevel reports whether s is a recognised --fail-on value
// (never | low | medium | high). It consults the same riskOrder map as
// RiskAtLeast so config validation and comparison share one source of truth.
func ValidFailOnLevel(s string) bool {
	_, ok := riskOrder[s]
	return ok
}

// fencedBlockRe matches a fenced code block, capturing its language tag and
// body. It is tolerant of surrounding whitespace and multi-line bodies.
var fencedBlockRe = regexp.MustCompile("(?s)```([a-zA-Z0-9_-]*)\\r?\\n(.*?)```")

// riskPayload is the one-line JSON object the model emits inside the risk block.
type riskPayload struct {
	Level   string `json:"level"`
	Summary string `json:"summary"`
}

// ParseRisk extracts the model's risk score from a review body (§7.2). It looks
// for the LAST fenced block tagged `risk`, validates the level, and returns the
// review content with that block stripped. ok is false when no valid risk block
// is found, in which case the caller should fall back to the heuristic; the
// returned content is the original text with any partially-matched, invalid
// risk blocks left intact (only a valid block is stripped).
// Only ` ```risk ` blocks are accepted — ` ```json ` is not, to avoid
// accidentally treating a generic JSON block as a risk payload.
func ParseRisk(content string) (stripped string, risk Risk, ok bool) {
	matches := fencedBlockRe.FindAllStringSubmatchIndex(content, -1)
	// Iterate from the last block backward so the LAST valid risk block wins.
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		lang := strings.ToLower(strings.TrimSpace(content[m[2]:m[3]]))
		if lang != "risk" {
			continue
		}
		body := strings.TrimSpace(content[m[4]:m[5]])

		var p riskPayload
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			continue
		}
		level := strings.ToLower(strings.TrimSpace(p.Level))
		if !ValidRiskLevel(level) {
			continue
		}

		// Strip the whole fenced block (indices m[0]:m[1]) from the content.
		stripped = content[:m[0]] + content[m[1]:]
		stripped = strings.TrimRight(stripped, " \t\r\n") + "\n"
		return stripped, Risk{Level: level, Summary: strings.TrimSpace(p.Summary)}, true
	}
	return content, Risk{}, false
}
