package gitlog

import "strings"

// diffHeaderPrefix is the per-file section header of a unified git diff. A
// genuine header always STARTS a line; the same text APPEARING inside a
// content line (every content line is prefixed with '+', '-', ' ' or '\')
// must never be treated as a boundary — see SplitDiffSections.
const diffHeaderPrefix = "diff --git "

// SplitDiffSections splits unified diff text into a leading preamble (any
// text before the first genuine section header — empty for normal `git diff`
// output, but preserved defensively) and self-contained per-file sections,
// each starting with its own "diff --git " header line.
//
// A section boundary is a line that STARTS WITH "diff --git ": either at
// position 0 of the whole string or immediately after a '\n'. Anchoring to
// line start is both correct and sufficient — real diff content lines are
// always prefixed with '+', '-', ' ' or '\', so they can never start with
// the literal header text, while an added line whose CONTENT contains
// "diff --git " mid-line must not split a section (previously it did,
// letting excluded files leak past filterDiffByGlobs).
//
// The invariant preamble + concat(sections) == diff always holds, so callers
// can re-join kept sections without altering any bytes.
func SplitDiffSections(diff string) (preamble string, sections []string) {
	var starts []int
	for off := 0; ; {
		i := strings.Index(diff[off:], diffHeaderPrefix)
		if i < 0 {
			break
		}
		pos := off + i
		if pos == 0 || diff[pos-1] == '\n' {
			starts = append(starts, pos)
		}
		off = pos + len(diffHeaderPrefix)
	}
	if len(starts) == 0 {
		return diff, nil
	}
	preamble = diff[:starts[0]]
	sections = make([]string, 0, len(starts))
	for k, s := range starts {
		end := len(diff)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		sections = append(sections, diff[s:end])
	}
	return preamble, sections
}

// DiffSectionPath extracts the new-side (b/...) file path from one section as
// returned by SplitDiffSections (i.e. starting with its "diff --git " header
// line). It returns "" when the header cannot be parsed.
//
// Two header shapes exist:
//
//	diff --git a/old path b/new path         (unquoted; spaces are NOT quoted)
//	diff --git "a/\320\264..." "b/\320\264..." (quoted per core.quotePath)
//
// Git quotes a path (C-style, in double quotes) when it contains non-ASCII
// bytes, double quotes, backslashes or control characters — and
// core.quotepath=true is git's DEFAULT, so quoted headers occur for any
// non-ASCII filename unless the producing `git diff` disabled it. The quoted
// form is unescaped with git's own quoting dialect (see unquoteGitPath), not
// strconv.Unquote, whose rules differ.
func DiffSectionPath(section string) string {
	header := section
	if nl := strings.IndexByte(section, '\n'); nl != -1 {
		header = section[:nl]
	}
	if !strings.HasPrefix(header, diffHeaderPrefix) {
		return ""
	}
	header = strings.TrimSuffix(header[len(diffHeaderPrefix):], "\r")

	// Quoted b-side: the b path is the last token on the line, so a quoted
	// b path means the header ends with the closing '"'.
	if strings.HasSuffix(header, `"`) {
		if p, ok := unquoteTrailingGitPath(header); ok && strings.HasPrefix(p, "b/") {
			return p[len("b/"):]
		}
		return ""
	}

	// Unquoted: both sides may contain spaces (git does not quote plain
	// spaces), so the LAST " b/" is the only reliable split point.
	if i := strings.LastIndex(header, " b/"); i >= 0 {
		return header[i+len(" b/"):]
	}
	return ""
}

// unquoteTrailingGitPath extracts and unquotes the final double-quoted token
// of a header that ends with '"'. It scans backward for the matching opening
// quote, skipping quotes escaped inside the path (a '"' preceded by an odd
// number of backslashes is content, not a delimiter).
func unquoteTrailingGitPath(header string) (string, bool) {
	end := len(header) - 1 // closing quote
	for j := end - 1; j >= 0; j-- {
		if header[j] != '"' {
			continue
		}
		backslashes := 0
		for k := j - 1; k >= 0 && header[k] == '\\'; k-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			continue // escaped quote inside the quoted path
		}
		return unquoteGitPath(header[j+1 : end]), true
	}
	return "", false
}

// unquoteGitPath decodes the C-style escapes git emits inside quoted paths
// (git's quote.c dialect): \" \\ \a \b \f \n \r \t \v and \NNN three-digit
// OCTAL byte escapes (e.g. the UTF-8 byte 0xD0 appears as \320). This is
// deliberately NOT strconv.Unquote — Go's dialect differs (e.g. \NNN octal
// escapes above \377 handling, \xNN, \uNNNN), and git never emits those.
// Unknown escapes and a trailing lone backslash are passed through verbatim
// rather than failing: a best-effort path beats losing the path entirely.
func unquoteGitPath(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			b.WriteByte('\\')
			break
		}
		switch e := s[i]; e {
		case '"', '\\':
			b.WriteByte(e)
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '0', '1', '2', '3':
			if i+2 < len(s) && isOctalDigit(s[i+1]) && isOctalDigit(s[i+2]) {
				b.WriteByte((e-'0')<<6 | (s[i+1]-'0')<<3 | (s[i+2] - '0'))
				i += 2
			} else {
				b.WriteByte('\\')
				b.WriteByte(e)
			}
		default:
			b.WriteByte('\\')
			b.WriteByte(e)
		}
	}
	return b.String()
}

// isOctalDigit reports whether c is an ASCII octal digit ('0'–'7').
func isOctalDigit(c byte) bool {
	return c >= '0' && c <= '7'
}
