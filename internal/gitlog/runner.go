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

// logFormat is the --pretty format with ASCII control separators:
// %x1f (unit separator) between fields, %x1e (record separator) after each
// commit. Control characters never appear in commit messages, so they are
// safe delimiters even for multi-line bodies (never split on "\n").
const logFormat = "--pretty=format:%H%x1f%an%x1f%aI%x1f%s%x1f%b%x1e"

// Source provides git history for a revision range. It exists so that the
// os/exec-based Runner could later be swapped for a go-git implementation
// without touching command code.
type Source interface {
	// Log returns the commits in the given revision range (e.g. "HEAD~5..HEAD").
	Log(ctx context.Context, revRange string) ([]Commit, error)
	// Diff returns the unified diff text for the given revision range.
	Diff(ctx context.Context, revRange string) (string, error)
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
	out, err := r.run(ctx, "log", logFormat, "--name-status", revRange)
	if err != nil {
		return nil, err
	}
	return ParseLog(out)
}

// Diff runs `git diff` over revRange and returns the raw unified diff text.
func (r *Runner) Diff(ctx context.Context, revRange string) (string, error) {
	return r.run(ctx, "diff", revRange)
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
	return r.run(ctx, "show", "--format=", hash)
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
