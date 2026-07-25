package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// prSummaryFragment is a representative fragment as rendered by
// ci/comment.sh with GITL_EMIT_SUMMARY=1 — the exact inner content is
// irrelevant to the merge script (it treats the fragment as opaque lines),
// what matters is the marker pair on the first/last lines.
const prSummaryFragment = `<!-- gitl-review-summary -->
**gitl risk: HIGH** — new summary *(heuristic)*

<sub>Automated by [gitl](https://github.com/akomyagin/gitl) · see the full review comment on this PR.</sub>
<!-- /gitl-review-summary -->
`

// runPRSummaryMerge executes ci/pr-summary-merge.sh with the given body and
// fragment contents and returns the merged stdout.
func runPRSummaryMerge(t *testing.T, body, fragment string) string {
	t.Helper()
	root := repoRoot(t)
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "body.md")
	fragFile := filepath.Join(dir, "fragment.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0o600); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	if err := os.WriteFile(fragFile, []byte(fragment), 0o600); err != nil {
		t.Fatalf("write fragment file: %v", err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "ci", "pr-summary-merge.sh"), bodyFile, fragFile)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("pr-summary-merge.sh failed: %v\nstderr: %s", err, ee.Stderr)
		}
		t.Fatalf("pr-summary-merge.sh failed: %v", err)
	}
	return string(out)
}

// TestPRSummaryMerge is the behavioral (golden-style, byte-exact) test of
// ci/pr-summary-merge.sh: replace-or-append semantics, preservation of
// human text outside the markers, and the defensive append path for
// damaged marker blocks. It shells out to bash, so it is skipped where
// bash is unavailable (Windows runners without git-bash on PATH).
func TestPRSummaryMerge(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available, skipping pr-summary-merge.sh test")
	}

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			// (a) first run on a PR with an empty description: the fragment
			// becomes the whole body, no leading separator.
			name: "empty body appends fragment as whole description",
			body: "",
			want: prSummaryFragment,
		},
		{
			// (b) first run on a human-written description: append after
			// exactly one blank separator line, human text untouched.
			name: "human body without markers gets fragment appended",
			body: "## What\n\nThis PR adds the frobnicator.\n",
			want: "## What\n\nThis PR adds the frobnicator.\n\n" + prSummaryFragment,
		},
		{
			// (c) subsequent run: the existing well-formed block is replaced
			// in place, text before and after preserved byte-for-byte.
			name: "well-formed block is replaced in place",
			body: "Intro paragraph.\n" +
				"<!-- gitl-review-summary -->\n" +
				"**gitl risk: LOW** — stale summary\n" +
				"<!-- /gitl-review-summary -->\n" +
				"Outro paragraph.\n",
			want: "Intro paragraph.\n" + prSummaryFragment + "Outro paragraph.\n",
		},
		{
			// (e) open marker with no close after it: never edit in place
			// (that could swallow human text) — body preserved, fresh block
			// appended.
			name: "unclosed open marker preserved and fragment appended",
			body: "Human text.\n" +
				"<!-- gitl-review-summary -->\n" +
				"dangling block with no close marker\n",
			want: "Human text.\n" +
				"<!-- gitl-review-summary -->\n" +
				"dangling block with no close marker\n" +
				"\n" + prSummaryFragment,
		},
		{
			// (f) close marker before the open marker (and no close after
			// the open): damaged block — preserved, fresh block appended.
			name: "close before open preserved and fragment appended",
			body: "<!-- /gitl-review-summary -->\n" +
				"Human text between stray markers.\n" +
				"<!-- gitl-review-summary -->\n",
			want: "<!-- /gitl-review-summary -->\n" +
				"Human text between stray markers.\n" +
				"<!-- gitl-review-summary -->\n" +
				"\n" + prSummaryFragment,
		},
		{
			// (g) two well-formed pairs: only the FIRST pair is replaced,
			// the second is left exactly as-is.
			name: "only first of two well-formed pairs is replaced",
			body: "A\n" +
				"<!-- gitl-review-summary -->\n" +
				"first old block\n" +
				"<!-- /gitl-review-summary -->\n" +
				"B\n" +
				"<!-- gitl-review-summary -->\n" +
				"second old block\n" +
				"<!-- /gitl-review-summary -->\n" +
				"C\n",
			want: "A\n" + prSummaryFragment +
				"B\n" +
				"<!-- gitl-review-summary -->\n" +
				"second old block\n" +
				"<!-- /gitl-review-summary -->\n" +
				"C\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runPRSummaryMerge(t, tt.body, prSummaryFragment)
			if got != tt.want {
				t.Errorf("merged body mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestPRSummaryMergeIdempotent is case (d): once a well-formed block exists,
// re-running the merge with the same fragment must be a byte-identical
// no-op — this is what makes force-push re-runs safe for the description.
func TestPRSummaryMergeIdempotent(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available, skipping pr-summary-merge.sh test")
	}

	body := "Intro.\n\nSome human context.\n"
	first := runPRSummaryMerge(t, body, prSummaryFragment)
	second := runPRSummaryMerge(t, first, prSummaryFragment)
	if second != first {
		t.Errorf("merge is not idempotent\nfirst:  %q\nsecond: %q", first, second)
	}
	third := runPRSummaryMerge(t, second, prSummaryFragment)
	if third != second {
		t.Errorf("merge is not idempotent on third run\nsecond: %q\nthird:  %q", second, third)
	}
}
