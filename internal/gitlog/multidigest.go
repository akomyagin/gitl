package gitlog

import (
	"context"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// RepoResult is the per-repository outcome of a multi-repo digest collection
// (§10.4). Exactly one of Digest/Err is meaningful: when Err is non-nil, the
// repo failed (bad path, not a git repo, git failure) and the other fields
// besides Path/Since/Until are zero values — callers must check Err before
// reading Digest.
type RepoResult struct {
	Path   string
	Since  time.Time
	Until  time.Time
	Digest Digest
	Err    error
}

// CollectDigests runs AggregateDigest over each repository path concurrently,
// using a bounded worker pool of `concurrency` goroutines communicating over
// a job-index channel (§10.4) — not an unbounded fan-out, and this outer pool
// is hand-rolled on raw channels/sync.WaitGroup rather than errgroup, per the
// project's "teach raw stdlib concurrency" principle (docs/TECHNICAL_PLAN.md
// §2). Inside each repo, collectOne runs exactly two independent git calls
// (LogSince + NumstatSince) in parallel via golang.org/x/sync/errgroup — that
// choice is scoped to that call site, not a blanket "no errgroup anywhere"
// rule. Total concurrent git processes are therefore bounded by 2×concurrency
// (no per-commit fan-out anymore, so no inner worker-sizing logic is needed).
//
// Results are written to results[i] by exactly one goroutine (the one that
// claims job i), so no mutex is needed, and the returned slice preserves the
// input order of repoPaths regardless of completion order — required for
// deterministic output and golden tests.
//
// A single bad repository (missing directory, not a git repository, git
// failure, or "no commits in window") never aborts the others: errors are
// isolated into that repo's RepoResult.Err. Cancelling ctx (e.g. Ctrl-C) stops
// workers from starting new repos; already-running `git` invocations are
// killed by exec.CommandContext as usual.
func CollectDigests(ctx context.Context, repoPaths []string, since time.Time, concurrency int) []RepoResult {
	results := make([]RepoResult, len(repoPaths))
	if len(repoPaths) == 0 {
		return results
	}

	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(repoPaths) {
		concurrency = len(repoPaths)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = collectOne(ctx, repoPaths[i], since)
			}
		}()
	}

	for i := range repoPaths {
		select {
		case jobs <- i:
		case <-ctx.Done():
			// Stop dispatching new work; already-dispatched jobs still run
			// to completion (their git calls are themselves ctx-aware and
			// will fail fast). Remaining, never-dispatched repos are marked
			// with the cancellation error below.
			close(jobs)
			wg.Wait()
			fillCancelled(results, repoPaths, since, ctx.Err())
			return results
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

// DefaultConcurrency returns a sensible worker-pool size for CollectDigests:
// GOMAXPROCS, capped at the number of repositories (§10.4). Not
// user-configurable in Этап 3 (no --concurrency flag).
func DefaultConcurrency(repoCount int) int {
	c := runtime.GOMAXPROCS(0)
	if repoCount > 0 && c > repoCount {
		c = repoCount
	}
	if c < 1 {
		c = 1
	}
	return c
}

// fillCancelled marks every not-yet-populated result (Path still empty, the
// zero value) with err, so a Ctrl-C mid-collection surfaces a clear
// per-repo error instead of a silently empty result.
func fillCancelled(results []RepoResult, repoPaths []string, since time.Time, err error) {
	for i := range results {
		if results[i].Path == "" {
			results[i] = RepoResult{Path: repoPaths[i], Since: since, Err: err}
		}
	}
}

// collectOne collects the digest for a single repository path, converting any
// failure into a RepoResult.Err rather than propagating it (§10.4). It issues
// exactly TWO git subprocesses, run in parallel via errgroup since neither
// depends on the other: LogSince (commit metadata + --name-status file
// attribution) and NumstatSince (per-commit added/removed line totals). This
// replaced the previous per-commit `git show` fan-out — one subprocess per
// commit in the window — that existed solely to feed DiffLineStats.
func collectOne(ctx context.Context, path string, since time.Time) RepoResult {
	until := time.Now().UTC()
	if err := ctx.Err(); err != nil {
		return RepoResult{Path: path, Since: since, Until: until, Err: err}
	}

	runner, err := NewRunner(path)
	if err != nil {
		return RepoResult{Path: path, Since: since, Until: until, Err: err}
	}

	var (
		commits []Commit
		stats   map[string]LineStats
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		commits, err = runner.LogSince(gctx, since)
		return err
	})
	g.Go(func() error {
		var err error
		stats, err = runner.NumstatSince(gctx, since)
		return err
	})
	if err := g.Wait(); err != nil {
		return RepoResult{Path: path, Since: since, Until: until, Err: err}
	}

	return RepoResult{
		Path:   path,
		Since:  since,
		Until:  until,
		Digest: AggregateDigest(commits, stats, since, until),
	}
}
