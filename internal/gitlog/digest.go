package gitlog

import (
	"sort"
	"strings"
	"time"
)

// AuthorStat is the per-author aggregate for a digest (§10.2).
type AuthorStat struct {
	Author       string
	Commits      int
	LinesAdded   int
	LinesRemoved int
}

// TopicStat is the per-conventional-commit-type aggregate for a digest
// (§10.2). Topic is the lowercased conventional-commit type, or "other" for
// non-conventional / unmapped commits.
type TopicStat struct {
	Topic   string
	Commits int
}

// FileStat is the per-file aggregate for a digest (§10.2).
type FileStat struct {
	Path    string
	Commits int
}

// Digest is the deterministic aggregation result for one repository over one
// time window (§10.2, §10.3).
type Digest struct {
	Since        time.Time
	Until        time.Time
	Commits      int
	FilesChanged int
	LinesAdded   int
	LinesRemoved int
	ByAuthor     []AuthorStat
	ByTopic      []TopicStat
	TopFiles     []FileStat
}

// topFilesLimit caps the "top changed files" table (§10.2) — deliberately
// hardcoded, not configurable in Этап 3.
const topFilesLimit = 10

// AggregateDigest computes the deterministic digest for commits observed in
// [since, until]. diffs maps commit hash to that commit's unified diff text
// (collected by the caller via Runner.DiffForCommit) — used only for the
// per-author added/removed line counts.
func AggregateDigest(commits []Commit, diffs map[string]string, since, until time.Time) Digest {
	d := Digest{Since: since, Until: until, Commits: len(commits)}

	authorIdx := make(map[string]int)
	topicCounts := make(map[string]int)
	fileCounts := make(map[string]int)

	for _, c := range commits {
		// By author.
		idx, ok := authorIdx[c.Author]
		if !ok {
			idx = len(d.ByAuthor)
			authorIdx[c.Author] = idx
			d.ByAuthor = append(d.ByAuthor, AuthorStat{Author: c.Author})
		}
		added, removed := DiffLineStats(diffs[c.Hash])
		d.ByAuthor[idx].Commits++
		d.ByAuthor[idx].LinesAdded += added
		d.ByAuthor[idx].LinesRemoved += removed
		d.LinesAdded += added
		d.LinesRemoved += removed

		// By topic: reuse the changelog conventional-commit type parser.
		topicCounts[topicOf(c)]++

		// By file (distinct paths touched across the whole window).
		for _, f := range c.Files {
			fileCounts[f.Path]++
		}
	}

	d.FilesChanged = len(fileCounts)

	sort.Slice(d.ByAuthor, func(i, j int) bool {
		if d.ByAuthor[i].Commits != d.ByAuthor[j].Commits {
			return d.ByAuthor[i].Commits > d.ByAuthor[j].Commits
		}
		return d.ByAuthor[i].Author < d.ByAuthor[j].Author
	})

	for topic, n := range topicCounts {
		d.ByTopic = append(d.ByTopic, TopicStat{Topic: topic, Commits: n})
	}
	sort.Slice(d.ByTopic, func(i, j int) bool {
		if d.ByTopic[i].Commits != d.ByTopic[j].Commits {
			return d.ByTopic[i].Commits > d.ByTopic[j].Commits
		}
		return d.ByTopic[i].Topic < d.ByTopic[j].Topic
	})

	var allFiles []FileStat
	for path, n := range fileCounts {
		allFiles = append(allFiles, FileStat{Path: path, Commits: n})
	}
	sort.Slice(allFiles, func(i, j int) bool {
		if allFiles[i].Commits != allFiles[j].Commits {
			return allFiles[i].Commits > allFiles[j].Commits
		}
		return allFiles[i].Path < allFiles[j].Path
	})
	if len(allFiles) > topFilesLimit {
		allFiles = allFiles[:topFilesLimit]
	}
	d.TopFiles = allFiles

	return d
}

// topicOf returns the digest "topic" for a commit: its conventional-commit
// type (lowercased), or "other" (§10.2). Reuses the same prefix grammar as
// changelog categorization rather than a second parallel classifier.
func topicOf(c Commit) string {
	m := conventionalPrefixRe.FindStringSubmatch(c.Subject)
	if m == nil {
		return "other"
	}
	return strings.ToLower(m[1])
}
