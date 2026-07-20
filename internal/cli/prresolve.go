// PR-number resolution for `gitl review pr/N` (§ post-MVP). The GitHub CLI
// (`gh`) resolves a PR number into base/head SHAs; the diff itself is then
// computed locally by gitlog.Runner (triple-dot base...head, like GitHub), so
// the whole shaping pipeline (exclude_globs, truncation, stats) stays
// identical across review modes.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// PRRef is the resolved base/head SHA pair for a pull request.
type PRRef struct {
	BaseSHA     string
	HeadSHA     string
	HeadRef     string // head branch name, for diagnostics
	BaseRefName string // base branch name (e.g. "main") — fetched by name, more robust than a bare SHA
	URL         string // full PR URL — identifies the PR's repository for remote-name resolution
	// CrossRepo marks a PR from a fork. Not an error: `git fetch <remote>
	// pull/N/head` works for forks too, so PR review handles them
	// transparently.
	CrossRepo bool
}

// PRResolver resolves a PR number into SHAs. The production ghResolver shells
// out to `gh`; tests substitute a fake, so no test ever talks to GitHub.
type PRResolver interface {
	ResolvePR(ctx context.Context, num int) (PRRef, error)
}

// newPRResolver builds the resolver used by `gitl review pr/N`. A package-level
// factory so tests can swap in a fake resolver without invoking gh.
var newPRResolver = func(dir string) (PRResolver, error) {
	return newGHResolver(dir)
}

// ghResolver implements PRResolver by shelling out to the GitHub CLI. gh holds
// the user's auth and works with private repos, forks, and GHE — no bespoke
// REST client needed. It is a hard runtime dependency ONLY on the pr/N path;
// range and --staged reviews never require it.
type ghResolver struct {
	// dir is the working directory for gh (empty = current directory), so gh
	// picks up the repository from the local checkout like git does.
	dir string
}

// newGHResolver fails fast with a friendly error when gh is not installed,
// instead of deferring the failure to the first ResolvePR call.
func newGHResolver(dir string) (*ghResolver, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("pr/N review requires the GitHub CLI (gh); install it (https://cli.github.com) or pass an explicit base..head range")
	}
	return &ghResolver{dir: dir}, nil
}

var _ PRResolver = (*ghResolver)(nil)

// ResolvePR runs `gh pr view <num> --json ...` and parses the SHA pair.
// gh inherits os.Environ() by default (auth token, config paths) — same as
// gitlog.Runner, which also never scopes cmd.Env.
func (g *ghResolver) ResolvePR(ctx context.Context, num int) (PRRef, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(num),
		"--json", "baseRefOid,headRefOid,headRefName,isCrossRepository,url,baseRefName")
	cmd.Dir = g.dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PRRef{}, fmt.Errorf("gh pr view: %w", ctxErr)
		}
		return PRRef{}, classifyGHError(num, stderr.String(), err)
	}
	return parseGHPRView(stdout.Bytes())
}

// parseGHPRView decodes the `gh pr view --json` payload into a PRRef. Pure
// function — tested on fixed JSON strings without invoking gh.
func parseGHPRView(out []byte) (PRRef, error) {
	var v struct {
		BaseRefOid        string `json:"baseRefOid"`
		HeadRefOid        string `json:"headRefOid"`
		HeadRefName       string `json:"headRefName"`
		BaseRefName       string `json:"baseRefName"`
		URL               string `json:"url"`
		IsCrossRepository bool   `json:"isCrossRepository"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return PRRef{}, fmt.Errorf("unexpected gh pr view output (not valid JSON): %w", err)
	}
	if v.BaseRefOid == "" || v.HeadRefOid == "" {
		return PRRef{}, fmt.Errorf("gh pr view output missing baseRefOid/headRefOid: %s", strings.TrimSpace(string(out)))
	}
	return PRRef{
		BaseSHA:     v.BaseRefOid,
		HeadSHA:     v.HeadRefOid,
		HeadRef:     v.HeadRefName,
		BaseRefName: v.BaseRefName,
		URL:         v.URL,
		CrossRepo:   v.IsCrossRepository,
	}, nil
}

// classifyGHError maps a failed `gh pr view` to a user-facing error by
// matching a few frequent stderr patterns (no full parser — 2-3 patterns plus
// a pass-through fallback). Pure function — tested on fixed stderr strings.
func classifyGHError(num int, stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no pull requests found") || strings.Contains(lower, "not found"):
		return fmt.Errorf("pull request #%d not found in this repository", num)
	case strings.Contains(lower, "gh auth login") || strings.Contains(lower, "authentication") || strings.Contains(lower, "auth status"):
		return fmt.Errorf("gh is not authenticated — run `gh auth login` (needed to resolve PR #%d): %s", num, msg)
	case msg == "":
		return fmt.Errorf("gh pr view failed: %v", err)
	default:
		return fmt.Errorf("gh pr view failed: %s", msg)
	}
}

// ghPRURLPattern matches a pull request URL as reported by `gh pr view
// --json url`, e.g. https://github.com/OWNER/REPO/pull/42 or, for GitHub
// Enterprise, https://ghe.example.com/OWNER/REPO/pull/42 — the host is
// captured, not assumed, so GHE works the same way github.com does: gh
// itself never fabricates a host, so this comparison stays strict.
var ghPRURLPattern = regexp.MustCompile(`^https://([^/]+)/([^/]+)/([^/]+)/pull/\d+$`)

// parseGitHubOwnerRepo extracts host/owner/repo from a PR URL reported by gh.
// ok=false for anything unrecognized (empty, malformed, non-https) — not an
// error, the caller just keeps the historical "origin" default. Pure function.
func parseGitHubOwnerRepo(url string) (host, owner, repo string, ok bool) {
	m := ghPRURLPattern.FindStringSubmatch(url)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// Remote URL forms that identify a hosted git repository: https (with or
// without a trailing .git) and ssh scp-like syntax. The host is captured
// (not assumed to be github.com), so GitHub Enterprise remotes match the
// same way — remoteURLMatchesRepo requires the host to equal the one gh
// reported for the PR, so this stays a strict, host-scoped comparison, not
// an open-ended "any git host" match. Owner/repo comparison is
// case-insensitive, matching GitHub's own semantics.
var (
	httpsRemotePattern = regexp.MustCompile(`(?i)^https://([^/]+)/([^/]+)/([^/]+?)(?:\.git)?/?$`)
	sshRemotePattern   = regexp.MustCompile(`(?i)^(?:ssh://)?git@([^:/]+)[:/]([^/]+)/([^/]+?)(?:\.git)?$`)
)

// remoteURLMatchesRepo reports whether a `git remote get-url` result points
// at the given host/owner/repo, accepting both https and ssh URL forms.
// Pure function.
func remoteURLMatchesRepo(remoteURL, host, owner, repo string) bool {
	u := strings.TrimSpace(remoteURL)
	for _, re := range []*regexp.Regexp{httpsRemotePattern, sshRemotePattern} {
		if m := re.FindStringSubmatch(u); m != nil {
			return strings.EqualFold(m[1], host) && strings.EqualFold(m[2], owner) && strings.EqualFold(m[3], repo)
		}
	}
	return false
}

// resolveRemoteName finds the local remote whose URL points at the PR's
// repository (host/owner/repo from the gh-reported PR URL). gh resolves PRs
// by its own repo detection, which is NOT tied to a remote named "origin" —
// in fork workflows "origin" is the fork and the PR lives on "upstream",
// where pull/N/head is actually published. When nothing matches (no remotes,
// unparsed URL), it falls back to "origin", preserving the old behavior for
// the common single-remote case. Arbitrary SSH host aliases from
// ~/.ssh/config (e.g. `Host gh-work` → `HostName github.com`) are
// deliberately NOT resolved: the remote URL is compared literally, without
// consulting the SSH config — resolving aliases (parsing ~/.ssh/config,
// shelling out to `ssh -G`) is a much larger task, intentionally out of
// scope here.
func resolveRemoteName(ctx context.Context, runner *gitlog.Runner, host, owner, repo string) string {
	const fallback = "origin"
	if host == "" || owner == "" || repo == "" {
		return fallback
	}
	names, err := runner.RemoteNames(ctx)
	if err != nil {
		return fallback
	}
	for _, name := range names {
		url, err := runner.RemoteURL(ctx, name)
		if err != nil {
			continue
		}
		if remoteURLMatchesRepo(url, host, owner, repo) {
			return name
		}
	}
	return fallback
}
