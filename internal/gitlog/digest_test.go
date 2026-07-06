package gitlog

import (
	"testing"
	"time"
)

func TestAggregateDigestByAuthor(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	commits := []Commit{
		{Hash: "h1", Author: "Jane Doe", Subject: "feat: a", Files: []FileChange{{Path: "a.go"}}},
		{Hash: "h2", Author: "John Roe", Subject: "fix: b", Files: []FileChange{{Path: "b.go"}}},
		{Hash: "h3", Author: "Jane Doe", Subject: "fix: c", Files: []FileChange{{Path: "a.go"}}},
	}
	diffs := map[string]string{
		"h1": "+added1\n+added2\n",
		"h2": "+added\n-removed\n",
		"h3": "-removed1\n-removed2\n-removed3\n",
	}

	d := AggregateDigest(commits, diffs, since, until)

	if d.Commits != 3 {
		t.Errorf("Commits = %d, want 3", d.Commits)
	}
	if d.FilesChanged != 2 {
		t.Errorf("FilesChanged = %d, want 2", d.FilesChanged)
	}
	if d.LinesAdded != 3 || d.LinesRemoved != 4 {
		t.Errorf("Lines +%d/-%d, want +3/-4", d.LinesAdded, d.LinesRemoved)
	}
	if len(d.ByAuthor) != 2 {
		t.Fatalf("ByAuthor = %+v, want 2 authors", d.ByAuthor)
	}
	// Jane Doe has 2 commits, sorts first (descending commit count).
	if d.ByAuthor[0].Author != "Jane Doe" || d.ByAuthor[0].Commits != 2 {
		t.Errorf("ByAuthor[0] = %+v, want Jane Doe with 2 commits", d.ByAuthor[0])
	}
	if d.ByAuthor[0].LinesAdded != 2 || d.ByAuthor[0].LinesRemoved != 3 {
		t.Errorf("Jane Doe lines = +%d/-%d, want +2/-3", d.ByAuthor[0].LinesAdded, d.ByAuthor[0].LinesRemoved)
	}
	if d.ByAuthor[1].Author != "John Roe" || d.ByAuthor[1].Commits != 1 {
		t.Errorf("ByAuthor[1] = %+v, want John Roe with 1 commit", d.ByAuthor[1])
	}
}

func TestAggregateDigestAuthorTieBreak(t *testing.T) {
	t.Parallel()
	commits := []Commit{
		{Hash: "h1", Author: "Zed", Subject: "feat: a"},
		{Hash: "h2", Author: "Amy", Subject: "feat: b"},
	}
	d := AggregateDigest(commits, nil, time.Time{}, time.Time{})
	if len(d.ByAuthor) != 2 {
		t.Fatalf("ByAuthor = %+v", d.ByAuthor)
	}
	// Equal commit counts (1 each) → alphabetical tie-break.
	if d.ByAuthor[0].Author != "Amy" || d.ByAuthor[1].Author != "Zed" {
		t.Errorf("tie-break order = %v, want [Amy, Zed]", []string{d.ByAuthor[0].Author, d.ByAuthor[1].Author})
	}
}

func TestAggregateDigestByTopic(t *testing.T) {
	t.Parallel()
	commits := []Commit{
		{Hash: "h1", Subject: "feat: a"},
		{Hash: "h2", Subject: "feat: b"},
		{Hash: "h3", Subject: "fix: c"},
		{Hash: "h4", Subject: "no convention here"},
	}
	d := AggregateDigest(commits, nil, time.Time{}, time.Time{})
	if len(d.ByTopic) != 3 {
		t.Fatalf("ByTopic = %+v, want 3 topics (feat, fix, other)", d.ByTopic)
	}
	if d.ByTopic[0].Topic != "feat" || d.ByTopic[0].Commits != 2 {
		t.Errorf("ByTopic[0] = %+v, want feat:2", d.ByTopic[0])
	}
	// fix and other are tied at 1 commit each → alphabetical: fix < other.
	if d.ByTopic[1].Topic != "fix" || d.ByTopic[2].Topic != "other" {
		t.Errorf("tie-break order = %+v, want [fix, other]", d.ByTopic[1:])
	}
}

func TestAggregateDigestTopFilesLimitAndOrder(t *testing.T) {
	t.Parallel()
	var commits []Commit
	// File "z.go" touched by 3 commits, "a.go" by 3 commits (tie → alpha),
	// plus 15 distinct single-touch files to exceed the top-10 cap.
	for i := 0; i < 3; i++ {
		commits = append(commits,
			Commit{Hash: "z" + string(rune('0'+i)), Subject: "chore: x", Files: []FileChange{{Path: "z.go"}}},
			Commit{Hash: "a" + string(rune('0'+i)), Subject: "chore: y", Files: []FileChange{{Path: "a.go"}}},
		)
	}
	for i := 0; i < 15; i++ {
		commits = append(commits, Commit{
			Hash:    "extra" + string(rune('A'+i)),
			Subject: "chore: extra",
			Files:   []FileChange{{Path: "extra" + string(rune('A'+i)) + ".go"}},
		})
	}

	d := AggregateDigest(commits, nil, time.Time{}, time.Time{})
	if d.FilesChanged != 17 { // z.go + a.go + 15 extras
		t.Errorf("FilesChanged = %d, want 17", d.FilesChanged)
	}
	if len(d.TopFiles) != topFilesLimit {
		t.Fatalf("TopFiles length = %d, want %d", len(d.TopFiles), topFilesLimit)
	}
	if d.TopFiles[0].Path != "a.go" || d.TopFiles[0].Commits != 3 {
		t.Errorf("TopFiles[0] = %+v, want a.go:3 (tie-break alpha before z.go)", d.TopFiles[0])
	}
	if d.TopFiles[1].Path != "z.go" || d.TopFiles[1].Commits != 3 {
		t.Errorf("TopFiles[1] = %+v, want z.go:3", d.TopFiles[1])
	}
}

func TestAggregateDigestEmptyWindow(t *testing.T) {
	t.Parallel()
	d := AggregateDigest(nil, nil, time.Time{}, time.Time{})
	if d.Commits != 0 || d.FilesChanged != 0 || len(d.ByAuthor) != 0 || len(d.ByTopic) != 0 || len(d.TopFiles) != 0 {
		t.Errorf("empty digest not all-zero: %+v", d)
	}
}

func TestTopicOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		subject string
		want    string
	}{
		{"feat: x", "feat"},
		{"FIX: x", "fix"},
		{"refactor(core): x", "refactor"},
		{"no prefix here", "other"},
		{"", "other"},
	}
	for _, tc := range tests {
		if got := topicOf(Commit{Subject: tc.subject}); got != tc.want {
			t.Errorf("topicOf(%q) = %q, want %q", tc.subject, got, tc.want)
		}
	}
}
