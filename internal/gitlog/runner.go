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
