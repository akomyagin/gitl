package render

import "strings"

// ANSI SGR sequences for colorizing the risk level token. Hand-written stdlib
// constants on purpose — a handful of SGR codes does not justify a color
// library (dependencies appear on demand, not up front).
//
// Color is ONLY ever applied AFTER sanitizeTerminal and ONLY around the
// static, enum-derived level token. sanitizeTerminal strips every ESC byte,
// so coloring before it would be silently undone; and wrapping any
// attacker-influenced text (summary, body) would let our own escapes mingle
// with injected content. See riskHeaderColored.
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiGreen  = "\x1b[32m"
)

// colorForLevel maps a validated risk level (any case) to its ANSI SGR code:
// high → red, medium → yellow, low → green. Unknown levels return "" (no
// color) — defense in depth; the enum is validated upstream. Levels are
// matched as literal strings rather than llm.Risk* constants to avoid a new
// render→llm package edge.
func colorForLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "high":
		return ansiRed
	case "medium":
		return ansiYellow
	case "low":
		return ansiGreen
	default:
		return ""
	}
}

// colorizeLevel wraps the first occurrence of the uppercased level token in
// sanitizedHeader with the ANSI color for riskLevel. sanitizedHeader MUST
// already have passed sanitizeTerminal. Replacing only the FIRST occurrence
// is safe because the level token always precedes the (attacker-influenced)
// summary in the header, so the first match is the trusted token itself.
func colorizeLevel(sanitizedHeader, riskLevel string) string {
	c := colorForLevel(riskLevel)
	if c == "" {
		return sanitizedHeader
	}
	level := strings.ToUpper(riskLevel)
	return strings.Replace(sanitizedHeader, level, c+level+ansiReset, 1)
}

// riskHeaderColored returns the sanitized risk header with the LEVEL token
// wrapped in ANSI color when color is true. Only the static level token is
// colored; summary text is never wrapped (it may be attacker-influenced).
// With color=false the result is byte-identical to
// sanitizeTerminal(riskHeader(art)) — the pre-color contract.
func riskHeaderColored(art Artifact, color bool) string {
	header := sanitizeTerminal(riskHeader(art))
	if !color {
		return header
	}
	return colorizeLevel(header, art.RiskLevel)
}

// RiskHeaderLineColored is the color-aware variant of RiskHeaderLine, used by
// the streaming path. With color=false it is byte-identical to RiskHeaderLine,
// which keeps the existing no-color contract intact.
func RiskHeaderLineColored(level, summary string, heuristic, color bool) string {
	return riskHeaderColored(Artifact{
		RiskLevel:     level,
		RiskSummary:   summary,
		RiskHeuristic: heuristic,
	}, color)
}
