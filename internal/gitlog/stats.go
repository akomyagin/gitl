package gitlog

import "strings"

// LineStats is a per-commit added/removed line-count pair, produced by
// ParseNumstat from `git log --numstat` output. Its zero value means "no
// changes", so a missing hash in a map[string]LineStats naturally reads as
// zero stats.
type LineStats struct {
	Added   int
	Removed int
}

// DiffLineStats counts added/removed content lines in a unified diff. Shared
// by the risk heuristic, the offline provider, and the review command's stats
// block — extracted here (rather than duplicated per caller) since it operates
// purely on diff text owned by this package.
//
// It is a small state machine driven by the diff's structural markers instead
// of per-line prefix guessing: only lines inside a hunk (after an "@@" hunk
// header, until the next "diff --git " section boundary) are counted. That
// makes the "+++ b/path"/"--- a/path" FILE headers naturally uncounted (they
// precede the first "@@" of each section), while genuine content lines that
// happen to START with literal "+++" or "---" (e.g. an added `+++i;` C line,
// or a removed YAML `---` separator) are correctly counted as content — the
// previous prefix-only check misclassified those as headers and dropped them.
//
// The scan walks the string with strings.Cut instead of strings.Split: the
// logic is a single sequential pass, so materialising a []string of every
// line was pure allocation overhead on large diffs. A final line without a
// trailing newline is still visited (Cut's found=false leaves it as the last
// rest), matching Split's behavior exactly.
func DiffLineStats(diff string) (added, removed int) {
	inHunk := false
	for rest := diff; rest != ""; {
		line, tail, found := strings.Cut(rest, "\n")
		if found {
			rest = tail
		} else {
			rest = "" // unterminated final line: process it, then stop
		}
		switch {
		case strings.HasPrefix(line, diffHeaderPrefix):
			// New per-file section: back to header territory. Each element of
			// the split IS a whole line, so this prefix check is line-anchored
			// by construction (a content line merely CONTAINING the header
			// text mid-line starts with '+'/'-'/' ' and never matches).
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk:
			// File headers (+++/---), index/mode/rename metadata before the
			// first hunk — never content.
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
			// Lines starting with ' ' (context) or '\' ("\ No newline at end
			// of file") fall through uncounted.
		}
	}
	return added, removed
}

// ChangedFileCount counts distinct changed file paths across all commits.
func ChangedFileCount(commits []Commit) int {
	seen := map[string]bool{}
	for _, c := range commits {
		for _, f := range c.Files {
			seen[f.Path] = true
		}
	}
	return len(seen)
}

// DiffFileCount counts the number of distinct files in a unified diff by
// counting its genuine (line-anchored) "diff --git " section headers. Used by
// HeuristicRisk so it scores the already glob-filtered diff, not the
// unfiltered commit file list.
func DiffFileCount(diff string) int {
	_, sections := SplitDiffSections(diff)
	return len(sections)
}

// DiffPaths extracts the changed (b-side) file paths from a unified diff by
// parsing "diff --git a/x b/y" headers (quoted core.quotePath headers
// included — see DiffSectionPath). Used as the sensitive-path signal when no
// commit metadata exists (e.g. a staged review, where there is no commit
// history to scan file paths from).
func DiffPaths(diff string) []string {
	_, sections := SplitDiffSections(diff)
	var paths []string
	for _, sec := range sections {
		if p := DiffSectionPath(sec); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
