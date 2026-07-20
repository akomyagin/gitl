// Package gitlog reads and parses git history via the system `git` binary
// (os/exec), hidden behind the Source interface so a go-git backend could
// replace it without touching commands (see docs/TECHNICAL_PLAN.md §4).
package gitlog

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// logFormat is the --pretty format with NUL separators: a single NUL (%x00)
// after EACH of the five fields — including the last one, %b.
//
// NUL is the ONLY byte git guarantees can never appear inside a commit
// message: fsck's nulInCommit check rejects such an object at creation time,
// verified empirically both at the porcelain level (`git commit-tree`) and at
// the plumbing level (`git hash-object -w -t commit --stdin`). Other control
// characters give no such guarantee — \x1e/\x1f pass verbatim through
// `git commit -F` from an attacker-controlled message and used to corrupt
// parsing here (record injection / one-commit DoS). This is the same
// principle behind git's own -z modes.
//
// The format inserts EXACTLY 5 literal %x00 bytes per commit (after
// hash/author/date/subject/body). Git guarantees this regardless of field
// content — an empty %s or %b still emits its trailing NUL — so a flat
// strings.Split on the single NUL byte always yields exactly 5*N+1 tokens
// for N commits, with no dependence on whether any field is empty. (A
// previous design used a 2-NUL record terminator and split records on a
// regex run of 2+ NULs; an empty subject AND body then produced 4 adjacent
// NULs that the run greedily swallowed, losing two fields and failing the
// whole range on one --allow-empty-message commit.)
//
// --name-status deliberately stays in its default TAB/LF mode (no -z): its
// block contains no NUL bytes, so it appears exclusively as part of the
// "head" token of the following record (glued to the next commit's hash
// with no separator), which splitHead takes apart.
const logFormat = "--pretty=format:%H%x00%an%x00%aI%x00%s%x00%b%x00"

// Source provides git history for a revision range. It exists so that the
// os/exec-based Runner could later be swapped for a go-git implementation
// without touching command code.
type Source interface {
	// Log returns the commits in the given revision range (e.g. "HEAD~5..HEAD").
	Log(ctx context.Context, revRange string) ([]Commit, error)
	// Diff returns the unified diff text for the given revision range.
	Diff(ctx context.Context, revRange string) (string, error)
	// DiffStaged returns the unified diff of staged (indexed, not yet
	// committed) changes, like `git diff --cached`. An empty string means
	// nothing is staged.
	DiffStaged(ctx context.Context) (string, error)
	// LatestTag returns the most recent reachable tag from HEAD (like
	// `git describe --tags --abbrev=0`). An empty string with a nil error
	// means no tags exist — a legitimate result, not a failure (see
	// docs/TECHNICAL_PLAN.md §9.5).
	LatestTag(ctx context.Context) (string, error)
	// LogSince returns the commits reachable from HEAD committed after since
	// (like `git log --since=<RFC3339>`).
	LogSince(ctx context.Context, since time.Time) ([]Commit, error)
	// DiffForCommit returns the unified diff text introduced by a single
	// commit (like `git show --format= <hash>`).
	DiffForCommit(ctx context.Context, hash string) (string, error)
	// ObjectExists reports whether a git object (commit SHA) is available
	// locally without a network call — used for a best-effort fetch (skip
	// fetching what's already there, which also keeps PR review testable
	// without real git fetches).
	ObjectExists(ctx context.Context, sha string) bool
	// FetchRef runs `git fetch --no-tags <remote> <ref>` for a remote ref/SHA
	// not yet available locally (e.g. a PR head or a base commit needed for
	// a merge-base diff).
	FetchRef(ctx context.Context, remote, ref string) error
	// RemoteNames returns the configured remote names (like `git remote`),
	// one per line of git output. An empty slice with a nil error means the
	// repository has no remotes.
	RemoteNames(ctx context.Context) ([]string, error)
	// RemoteURL returns the fetch URL of the given remote (like
	// `git remote get-url <remote>`). Errors when the remote does not exist.
	RemoteURL(ctx context.Context, remote string) (string, error)
}

// Runner implements Source by shelling out to the system `git` binary.
type Runner struct {
	// dir is the working directory for git commands; empty means the
	// current working directory.
	dir string
}

var _ Source = (*Runner)(nil)

// NewRunner returns a Runner operating in dir (empty = current directory).
// It fails with a friendly error if `git` is not installed in PATH.
func NewRunner(dir string) (*Runner, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git executable not found in PATH: gitl requires the system git; install it via your package manager (e.g. `apt install git`, `brew install git`)")
	}
	return &Runner{dir: dir}, nil
}

// Log runs `git log` over revRange with the control-separator format plus
// --name-status and parses the output into Commits.
func (r *Runner) Log(ctx context.Context, revRange string) ([]Commit, error) {
	out, err := r.run(ctx, "log", logFormat, "--name-status", "--end-of-options", revRange)
	if err != nil {
		return nil, err
	}
	return ParseLog(out)
}

// Diff runs `git diff` over revRange and returns the raw unified diff text.
func (r *Runner) Diff(ctx context.Context, revRange string) (string, error) {
	return r.run(ctx, "diff", "--end-of-options", revRange)
}

// DiffStaged runs `git diff --cached` and returns the unified diff of staged
// changes. Nothing staged is not an error — git exits 0 with empty output, so
// callers branch on the empty string.
func (r *Runner) DiffStaged(ctx context.Context) (string, error) {
	return r.run(ctx, "diff", "--cached")
}

// LatestTag runs `git describe --tags --abbrev=0` and returns the most recent
// reachable tag. When the repository has no tags, git exits non-zero; that
// specific case is not an error here (§9.5) — LatestTag returns ("", nil) so
// callers can branch on an empty string instead of inspecting error text. Any
// describe failure (no tags, unborn HEAD, ...) degrades to "no tag" — the
// exact stderr wording varies by git version/locale, so it is not worth
// pattern-matching; every failure means the same thing to the caller.
func (r *Runner) LatestTag(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "describe", "--tags", "--abbrev=0")
	if err != nil {
		return "", nil //nolint:nilerr // any describe failure degrades to "no tag" per §9.5
	}
	return strings.TrimSpace(out), nil
}

// LogSince runs `git log --since=<RFC3339>` with the control-separator format
// plus --name-status and parses the output into Commits (§10.1). The window
// is open-ended on the upper bound (implicitly "since..HEAD" of the current
// checkout), matching every other command's branch-agnostic behavior.
func (r *Runner) LogSince(ctx context.Context, since time.Time) ([]Commit, error) {
	arg := "--since=" + since.UTC().Format(time.RFC3339)
	out, err := r.run(ctx, "log", logFormat, "--name-status", arg)
	if err != nil {
		return nil, err
	}
	return ParseLog(out)
}

// DiffForCommit runs `git show --format= <hash>` and returns the unified diff
// introduced by that single commit (no commit message, just the diff body).
func (r *Runner) DiffForCommit(ctx context.Context, hash string) (string, error) {
	return r.run(ctx, "show", "--format=", "--end-of-options", hash)
}

// ObjectExists runs `git cat-file -e <sha>^{commit}` and reports whether the
// commit object is available locally. Any failure (missing object, malformed
// SHA, ...) is false, never an error — this is a boolean probe, not an
// operation that can "fail".
func (r *Runner) ObjectExists(ctx context.Context, sha string) bool {
	_, err := r.run(ctx, "cat-file", "-e", "--end-of-options", sha+"^{commit}")
	return err == nil
}

// FetchRef runs `git fetch --no-tags <remote> <ref>` to make a remote ref/SHA
// (e.g. `pull/N/head` or a base branch) available locally. The remote is a
// parameter, not a hardcoded "origin": in fork workflows the PR's repository
// is often configured as "upstream" while "origin" points at the fork.
func (r *Runner) FetchRef(ctx context.Context, remote, ref string) error {
	_, err := r.run(ctx, "fetch", "--no-tags", "--end-of-options", remote, ref)
	return err
}

// RemoteNames runs `git remote` and returns the configured remote names. A
// repository without remotes yields an empty slice, not an error.
func (r *Runner) RemoteNames(ctx context.Context) ([]string, error) {
	out, err := r.run(ctx, "remote")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// RemoteURL runs `git remote get-url <remote>` and returns the fetch URL.
// A nonexistent remote is an error (git exits non-zero).
func (r *Runner) RemoteURL(ctx context.Context, remote string) (string, error) {
	out, err := r.run(ctx, "remote", "get-url", "--end-of-options", remote)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// run executes a git subcommand, reading stdout and stderr into separate
// buffers. Non-zero exits are wrapped with the stderr content.
func (r *Runner) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("git %s: %w", args[0], ctxErr)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s failed: %s", args[0], msg)
	}
	return stdout.String(), nil
}
