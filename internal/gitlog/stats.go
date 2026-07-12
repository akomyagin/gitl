package gitlog

import "strings"

// DiffLineStats counts added/removed content lines in a unified diff, ignoring
// the +++/--- file headers. Shared by the risk heuristic, the offline
// provider, and the review command's stats block — extracted here (rather
// than duplicated per caller) since it operates purely on diff text owned by
// this package.
func DiffLineStats(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			// file headers, not content changes
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
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
// counting "diff --git " headers. Used by HeuristicRisk so it scores the
// already glob-filtered diff, not the unfiltered commit file list.
func DiffFileCount(diff string) int {
	n := strings.Count(diff, "\ndiff --git ")
	if strings.HasPrefix(diff, "diff --git ") {
		n++
	}
	return n
}

// DiffPaths extracts the changed (b-side) file paths from a unified diff by
// parsing "diff --git a/x b/y" headers. Used as the sensitive-path signal
// when no commit metadata exists (e.g. a staged review, where there is no
// commit history to scan file paths from).
func DiffPaths(diff string) []string {
	const sep = "diff --git "
	idx := strings.Index(diff, sep)
	if idx == -1 {
		return nil
	}
	var paths []string
	for _, sec := range strings.Split(diff[idx:], sep) {
		if sec == "" {
			continue
		}
		header := sec
		if nl := strings.IndexByte(sec, '\n'); nl != -1 {
			header = sec[:nl]
		}
		// header is "a/OLDPATH b/NEWPATH"; find the last " b/" to correctly
		// split the b-side even for paths containing spaces.
		if i := strings.LastIndex(header, " b/"); i >= 0 {
			paths = append(paths, header[i+3:])
		}
	}
	return paths
}
