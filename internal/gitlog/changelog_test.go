package gitlog

import (
	"testing"
)

func TestCategorizeOnePrefixMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		subject  string
		wantCat  string
		wantSubj string
	}{
		{"feat: add token refresh", CategoryAdded, "add token refresh"},
		{"fix: correct off-by-one", CategoryFixed, "correct off-by-one"},
		{"perf: speed up parser", CategoryChanged, "speed up parser"},
		{"refactor: extract helper", CategoryChanged, "extract helper"},
		{"revert: revert feat X", CategoryChanged, "revert feat X"},
		{"deprecate: old API", CategoryDeprecated, "old API"},
		{"remove: dead code", CategoryRemoved, "dead code"},
		{"security: patch CVE", CategorySecurity, "patch CVE"},
		{"docs: update README", CategoryOther, "update README"},
		{"style: gofmt", CategoryOther, "gofmt"},
		{"test: add cases", CategoryOther, "add cases"},
		{"build: bump go version", CategoryOther, "bump go version"},
		{"ci: fix workflow", CategoryOther, "fix workflow"},
		{"chore: housekeeping", CategoryOther, "housekeeping"},
		{"FEAT: uppercase type", CategoryAdded, "uppercase type"},
		{"feat(auth): scoped", CategoryAdded, "scoped"},
		{"random commit message", CategoryOther, "random commit message"},
		{"", CategoryOther, ""},
	}
	for _, tc := range tests {
		e := categorizeOne(Commit{Hash: "abcdefg1234", Subject: tc.subject})
		if e.Category != tc.wantCat {
			t.Errorf("categorizeOne(%q).Category = %q, want %q", tc.subject, e.Category, tc.wantCat)
		}
		if e.Subject != tc.wantSubj {
			t.Errorf("categorizeOne(%q).Subject = %q, want %q", tc.subject, e.Subject, tc.wantSubj)
		}
	}
}

func TestCategorizeOneShortensHash(t *testing.T) {
	t.Parallel()
	e := categorizeOne(Commit{Hash: "abcdefabcdef1234", Subject: "feat: x"})
	if e.Hash != "abcdefa" {
		t.Errorf("Hash = %q, want 7-char prefix", e.Hash)
	}
	// A hash already <=7 chars is left as-is.
	e2 := categorizeOne(Commit{Hash: "abc12", Subject: "feat: x"})
	if e2.Hash != "abc12" {
		t.Errorf("Hash = %q, want unchanged short hash", e2.Hash)
	}
}

func TestCategorizeOneBreakingBang(t *testing.T) {
	t.Parallel()
	e := categorizeOne(Commit{Hash: "abcdefg", Subject: "feat!: rework session store"})
	if !e.Breaking {
		t.Fatal("expected Breaking = true for feat!:")
	}
	if e.Category != CategoryAdded {
		t.Errorf("category = %q, want Added (feat! still maps to feat's category)", e.Category)
	}
	if e.BreakingText != "rework session store" {
		t.Errorf("BreakingText = %q, want stripped subject", e.BreakingText)
	}
}

func TestCategorizeOneBreakingFooter(t *testing.T) {
	t.Parallel()
	e := categorizeOne(Commit{
		Hash:    "abcdefg",
		Subject: "feat: rework session store API",
		Body:    "Some description.\n\nBREAKING CHANGE: drop support for config schema v0\n",
	})
	if !e.Breaking {
		t.Fatal("expected Breaking = true for BREAKING CHANGE: footer")
	}
	if e.BreakingText != "drop support for config schema v0" {
		t.Errorf("BreakingText = %q", e.BreakingText)
	}
}

func TestCategorizeOneScopedBreakingBoth(t *testing.T) {
	t.Parallel()
	// Both "!" and footer present: footer text wins (more specific).
	e := categorizeOne(Commit{
		Hash:    "abcdefg",
		Subject: "fix(api)!: change response shape",
		Body:    "BREAKING CHANGE: responses are now wrapped in {data: ...}",
	})
	if !e.Breaking {
		t.Fatal("expected Breaking = true")
	}
	if e.Category != CategoryFixed {
		t.Errorf("category = %q, want Fixed", e.Category)
	}
	if e.BreakingText != "responses are now wrapped in {data: ...}" {
		t.Errorf("BreakingText = %q", e.BreakingText)
	}
}

func TestCategorizeCommitsExcludesMerges(t *testing.T) {
	t.Parallel()
	commits := []Commit{
		{Hash: "h1", Subject: "Merge pull request #1 from foo/bar"},
		{Hash: "h2", Subject: "feat: real change"},
	}
	cl := CategorizeCommits(commits)
	if len(cl.Categories[CategoryAdded]) != 1 {
		t.Fatalf("Added = %+v, want exactly the feat commit", cl.Categories[CategoryAdded])
	}
	for cat, entries := range cl.Categories {
		for _, e := range entries {
			if e.Hash == "h1" {
				t.Errorf("merge commit leaked into category %q", cat)
			}
		}
	}
}

func TestCategorizeCommitsDedupByHash(t *testing.T) {
	t.Parallel()
	commits := []Commit{
		{Hash: "dupe123", Subject: "feat: first"},
		{Hash: "dupe123", Subject: "feat: first (duplicate entry)"},
	}
	cl := CategorizeCommits(commits)
	if len(cl.Categories[CategoryAdded]) != 1 {
		t.Fatalf("Added = %+v, want deduped to 1 entry", cl.Categories[CategoryAdded])
	}
	if cl.Categories[CategoryAdded][0].Subject != "first" {
		t.Errorf("kept subject = %q, want first occurrence", cl.Categories[CategoryAdded][0].Subject)
	}
}

func TestCategorizeCommitsAllCategoryKeysPresent(t *testing.T) {
	t.Parallel()
	cl := CategorizeCommits(nil)
	for _, name := range CategoryOrder {
		if _, ok := cl.Categories[name]; !ok {
			t.Errorf("category key %q missing from Categories map", name)
		}
	}
}

func TestCategorizeCommitsBreakingListPopulated(t *testing.T) {
	t.Parallel()
	commits := []Commit{
		{Hash: "h1", Subject: "feat!: breaking one"},
		{Hash: "h2", Subject: "fix: normal fix"},
		{Hash: "h3", Subject: "feat: breaking two", Body: "BREAKING CHANGE: second breaking change"},
	}
	cl := CategorizeCommits(commits)
	if len(cl.Breaking) != 2 {
		t.Fatalf("Breaking = %+v, want 2 entries", cl.Breaking)
	}
	if cl.Breaking[0].Hash != "h1" || cl.Breaking[1].Hash != "h3" {
		t.Errorf("Breaking order/content unexpected: %+v", cl.Breaking)
	}
}

func TestMissingRequiredCategories(t *testing.T) {
	t.Parallel()
	commits := []Commit{
		{Hash: "h1", Subject: "feat: add x"},
	}
	cl := CategorizeCommits(commits)

	tests := []struct {
		name     string
		required []string
		want     []string
	}{
		{"nil required", nil, nil},
		{"empty required", []string{}, nil},
		{"all satisfied", []string{"Added"}, nil},
		{"one missing", []string{"Added", "Security"}, []string{"Security"}},
		{"multiple missing sorted", []string{"Security", "Fixed"}, []string{"Fixed", "Security"}},
	}
	for _, tc := range tests {
		got := MissingRequiredCategories(cl, tc.required)
		if !equalStringSlices(got, tc.want) {
			t.Errorf("%s: MissingRequiredCategories = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
