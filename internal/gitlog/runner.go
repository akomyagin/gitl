// Package gitlog reads and parses git history via the system `git` binary
// (os/exec). All git invocations are concentrated in the Runner type so a
// go-git backend could replace it without touching commands (see
// docs/TECHNICAL_PLAN.md §4).
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

// Runner provides git history by shelling out to the system `git` binary. It
// concentrates every git invocation of the project, so a go-git implementation
// could later replace it without touching command code.
type Runner struct {
	// dir is the working directory for git commands; empty means the
	// current working directory.
	dir string
}

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

// noQuotePath disables git's core.quotePath quoting of non-ASCII filenames in
// diff headers (`diff --git "a/\320..." "b/\320..."` → plain UTF-8 paths).
// Quoting is git's DEFAULT, and quoted headers used to defeat exclude-glob
// matching and DiffPaths extraction. Passed as `-c core.quotepath=false`
// BEFORE the subcommand on every diff-producing invocation. Defense in depth:
// DiffSectionPath still decodes quoted headers for diff text that originated
// elsewhere — this flag just keeps our own output unquoted at the source.
var noQuotePath = []string{"-c", "core.quotepath=false"}

// Diff runs `git diff` over revRange and returns the raw unified diff text.
func (r *Runner) Diff(ctx context.Context, revRange string) (string, error) {
	return r.run(ctx, append(noQuotePath, "diff", "--end-of-options", revRange)...)
}

// DiffStaged runs `git diff --cached` and returns the unified diff of staged
// changes. Nothing staged is not an error — git exits 0 with empty output, so
// callers branch on the empty string.
func (r *Runner) DiffStaged(ctx context.Context) (string, error) {
	return r.run(ctx, append(noQuotePath, "diff", "--cached")...)
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

// numstatFormat is the --pretty format for NumstatSince: a single NUL (%x00)
// immediately BEFORE each commit's hash, so a flat split on NUL yields one
// clean per-commit record (hash line + that commit's --numstat block) — the
// same NUL-separator principle as logFormat (NUL is the one byte git can never
// emit from commit content; see the logFormat doc comment).
const numstatFormat = "--pretty=format:%x00%H"

// NumstatSince runs `git log --since=<RFC3339> --numstat` with a NUL-prefixed
// hash format and returns per-commit added/removed line totals, keyed by
// commit hash (same open-ended window semantics as LogSince). One subprocess
// replaces the previous per-commit `git show` fan-out for digest line stats.
// No noQuotePath here: only the two numeric columns are read, paths are
// ignored entirely (quoted or not), and file attribution comes from LogSince.
func (r *Runner) NumstatSince(ctx context.Context, since time.Time) (map[string]LineStats, error) {
	arg := "--since=" + since.UTC().Format(time.RFC3339)
	out, err := r.run(ctx, "log", numstatFormat, arg, "--numstat")
	if err != nil {
		return nil, err
	}
	return ParseNumstat(out)
}

// ObjectExists runs `git cat-file -e <sha>^{commit}` and reports whether the
// commit object is available locally. Any failure (missing object, malformed
// SHA, ...) is false, never an error — this is a boolean probe, not an
// operation that can "fail".
func (r *Runner) ObjectExists(ctx context.Context, sha string) bool {
	_, err := r.run(ctx, "cat-file", "-e", "--end-of-options", sha+"^{commit}")
	return err == nil
}

// FetchRef runs `git fetch --no-tags <remote> <ref>...` to make one or more
// remote refs/SHAs (e.g. `pull/N/head` and a base branch) available locally in
// a SINGLE fetch — git accepts multiple refspecs on one command line, so two
// missing PR refs cost one subprocess, not two. The remote is a parameter, not
// a hardcoded "origin": in fork workflows the PR's repository is often
// configured as "upstream" while "origin" points at the fork.
//
// Zero refs is a no-op returning nil (like appending nothing): running bare
// `git fetch <remote>` would fetch the remote's DEFAULT refspec — never what a
// caller that computed an empty missing-ref list wants.
func (r *Runner) FetchRef(ctx context.Context, remote string, refs ...string) error {
	if len(refs) == 0 {
		return nil
	}
	args := append([]string{"fetch", "--no-tags", "--end-of-options", remote}, refs...)
	_, err := r.run(ctx, args...)
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

// TopLevel runs `git rev-parse --show-toplevel` and returns the absolute path
// of the working-tree root. Outside a git work tree (or in a bare repo) git
// exits non-zero and the error is returned as-is — callers that only need a
// best-effort root (config discovery) treat any error as "no root found".
func (r *Runner) TopLevel(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--show-toplevel")
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
		name := gitSubcommand(args)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("git %s: %w", name, ctxErr)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s failed: %s", name, msg)
	}
	return stdout.String(), nil
}

// gitSubcommand returns the subcommand name for error messages, skipping any
// leading "-c <key>=<val>" option pairs (e.g. the noQuotePath prefix) so
// errors say `git diff failed`, not `git -c failed`.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++ // skip the "-c" value too
			continue
		}
		return args[i]
	}
	return "git"
}
