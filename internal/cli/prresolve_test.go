package cli

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// TestParseGHPRView: pure JSON parsing of `gh pr view --json` output — no gh
// invocation.
func TestParseGHPRView(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    PRRef
		wantErr string
	}{
		{
			name: "valid same-repo PR",
			in: `{"baseRefOid":"aaa111","headRefOid":"bbb222",` +
				`"headRefName":"feature/x","isCrossRepository":false,` +
				`"baseRefName":"main","url":"https://github.com/acme/widget/pull/42"}`,
			want: PRRef{
				BaseSHA: "aaa111", HeadSHA: "bbb222", HeadRef: "feature/x",
				BaseRefName: "main", URL: "https://github.com/acme/widget/pull/42",
			},
		},
		{
			name: "valid fork PR",
			in: `{"baseRefOid":"aaa111","headRefOid":"bbb222",` +
				`"headRefName":"fork-branch","isCrossRepository":true,` +
				`"baseRefName":"develop","url":"https://github.com/acme/widget/pull/7"}`,
			want: PRRef{
				BaseSHA: "aaa111", HeadSHA: "bbb222", HeadRef: "fork-branch",
				BaseRefName: "develop", URL: "https://github.com/acme/widget/pull/7",
				CrossRepo: true,
			},
		},
		{
			name: "url and baseRefName absent (older gh)",
			in: `{"baseRefOid":"aaa111","headRefOid":"bbb222",` +
				`"headRefName":"feature/x","isCrossRepository":false}`,
			want: PRRef{BaseSHA: "aaa111", HeadSHA: "bbb222", HeadRef: "feature/x"},
		},
		{
			name:    "invalid JSON",
			in:      "not json at all",
			wantErr: "not valid JSON",
		},
		{
			name:    "valid JSON missing SHAs",
			in:      `{"headRefName":"x"}`,
			wantErr: "missing baseRefOid/headRefOid",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseGHPRView([]byte(tc.in))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("PRRef = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestClassifyGHError: pure stderr-pattern classification — no gh invocation.
func TestClassifyGHError(t *testing.T) {
	t.Parallel()
	execErr := errors.New("exit status 1")
	tests := []struct {
		name   string
		stderr string
		want   []string // substrings the classified error must contain
	}{
		{
			name:   "PR not found",
			stderr: "GraphQL: Could not resolve to a PullRequest with the number of 999. no pull requests found",
			want:   []string{"pull request #42 not found"},
		},
		{
			name:   "not found variant",
			stderr: "no pull requests found for branch",
			want:   []string{"not found in this repository"},
		},
		{
			name:   "not authenticated",
			stderr: "To get started with GitHub CLI, please run:  gh auth login",
			want:   []string{"gh is not authenticated", "gh auth login", "#42"},
		},
		{
			name:   "authentication variant",
			stderr: "HTTP 401: authentication required",
			want:   []string{"gh is not authenticated"},
		},
		{
			name:   "unknown stderr passed through",
			stderr: "some totally unexpected gh failure",
			want:   []string{"gh pr view failed", "some totally unexpected gh failure"},
		},
		{
			name:   "empty stderr falls back to exec error",
			stderr: "",
			want:   []string{"gh pr view failed", "exit status 1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := classifyGHError(42, tc.stderr, execErr)
			if err == nil {
				t.Fatal("classifyGHError returned nil")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err.Error(), w)
				}
			}
		})
	}
}

// TestParseGitHubOwnerRepo: pure PR-URL parsing — github.com URLs yield
// owner/repo, anything else (GHE hosts, malformed) yields ok=false.
func TestParseGitHubOwnerRepo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		url   string
		owner string
		repo  string
		ok    bool
	}{
		{"valid github.com PR URL", "https://github.com/acme/widget/pull/42", "acme", "widget", true},
		{"owner and repo with dots and dashes", "https://github.com/some-org/my.repo-x/pull/1", "some-org", "my.repo-x", true},
		{"GitHub Enterprise host", "https://github.example.com/acme/widget/pull/42", "", "", false},
		{"http not https", "http://github.com/acme/widget/pull/42", "", "", false},
		{"missing pull segment", "https://github.com/acme/widget/issues/42", "", "", false},
		{"trailing garbage", "https://github.com/acme/widget/pull/42/files", "", "", false},
		{"empty string", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			owner, repo, ok := parseGitHubOwnerRepo(tc.url)
			if owner != tc.owner || repo != tc.repo || ok != tc.ok {
				t.Errorf("parseGitHubOwnerRepo(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.url, owner, repo, ok, tc.owner, tc.repo, tc.ok)
			}
		})
	}
}

// TestRemoteURLMatchesRepo: pure remote-URL matching — https and ssh forms,
// optional .git suffix, case-insensitive owner/repo.
func TestRemoteURLMatchesRepo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		url   string
		owner string
		repo  string
		want  bool
	}{
		{"https with .git", "https://github.com/acme/widget.git", "acme", "widget", true},
		{"https without .git", "https://github.com/acme/widget", "acme", "widget", true},
		{"ssh scp-like with .git", "git@github.com:acme/widget.git", "acme", "widget", true},
		{"ssh scp-like without .git", "git@github.com:acme/widget", "acme", "widget", true},
		{"ssh:// scheme", "ssh://git@github.com/acme/widget.git", "acme", "widget", true},
		{"case-insensitive owner/repo", "https://github.com/ACME/Widget.git", "acme", "widget", true},
		{"different repo", "https://github.com/acme/other.git", "acme", "widget", false},
		{"different owner (fork)", "https://github.com/someone/widget.git", "acme", "widget", false},
		{"non-github host", "https://gitlab.com/acme/widget.git", "acme", "widget", false},
		{"empty URL", "", "acme", "widget", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := remoteURLMatchesRepo(tc.url, tc.owner, tc.repo); got != tc.want {
				t.Errorf("remoteURLMatchesRepo(%q, %q, %q) = %v, want %v",
					tc.url, tc.owner, tc.repo, got, tc.want)
			}
		})
	}
}

// TestResolveRemoteName: a real temp repo with two remotes (origin = fork,
// upstream = the PR's repository) — the remote matching the PR's owner/repo
// wins; no match falls back to "origin".
func TestResolveRemoteName(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:someone/widget.git")
	runGit(t, dir, "remote", "add", "upstream", "https://github.com/acme/widget.git")

	runner, err := gitlog.NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	if got := resolveRemoteName(ctx, runner, "acme", "widget"); got != "upstream" {
		t.Errorf("resolveRemoteName(acme/widget) = %q, want %q", got, "upstream")
	}
	if got := resolveRemoteName(ctx, runner, "someone", "widget"); got != "origin" {
		t.Errorf("resolveRemoteName(someone/widget) = %q, want %q (fork remote)", got, "origin")
	}
	if got := resolveRemoteName(ctx, runner, "nobody", "nothing"); got != "origin" {
		t.Errorf("resolveRemoteName(no match) = %q, want fallback %q", got, "origin")
	}
	if got := resolveRemoteName(ctx, runner, "", ""); got != "origin" {
		t.Errorf("resolveRemoteName(empty owner/repo) = %q, want fallback %q", got, "origin")
	}
}

// TestResolveRemoteNameNoRemotes: a repo without any remotes falls back to
// "origin" (the setupPRRepo scenario — objects are local, no fetch happens).
func TestResolveRemoteNameNoRemotes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")

	runner, err := gitlog.NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if got := resolveRemoteName(context.Background(), runner, "acme", "widget"); got != "origin" {
		t.Errorf("resolveRemoteName(no remotes) = %q, want fallback %q", got, "origin")
	}
}
