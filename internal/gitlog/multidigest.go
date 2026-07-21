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
// §2). The per-commit diff collection *inside* each repo's AggregateDigest
// call does use golang.org/x/sync/errgroup (a separate, inner pool) — that
// choice is scoped to that call site, not a blanket "no errgroup anywhere"
// rule.
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

	// Cap the inner errgroup (per-commit diffs) so total concurrent git
	// processes stay at GOMAXPROCS rather than growing to concurrency×GOMAXPROCS.
	innerConcurrency := runtime.GOMAXPROCS(0) / concurrency
	if innerConcurrency < 1 {
		innerConcurrency = 1
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = collectOne(ctx, repoPaths[i], since, innerConcurrency)
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
// failure into a RepoResult.Err rather than propagating it (§10.4).
// innerConcurrency limits the errgroup that parallelises DiffForCommit calls;
// the caller sets it so that outer×inner stays at most GOMAXPROCS.
func collectOne(ctx context.Context, path string, since time.Time, innerConcurrency int) RepoResult {
	until := time.Now().UTC()
	if err := ctx.Err(); err != nil {
		return RepoResult{Path: path, Since: since, Until: until, Err: err}
	}

	runner, err := NewRunner(path)
	if err != nil {
		return RepoResult{Path: path, Since: since, Until: until, Err: err}
	}

	commits, err := runner.LogSince(ctx, since)
	if err != nil {
		return RepoResult{Path: path, Since: since, Until: until, Err: err}
	}

	diffs := make(map[string]string, len(commits))
	if len(commits) > 0 {
		diffSlice := make([]string, len(commits)) // index-ordered, no mutex needed
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(min(innerConcurrency, len(commits)))
		for i, c := range commits {
			hash := c.Hash
			g.Go(func() error {
				d, err := runner.DiffForCommit(gctx, hash)
				if err != nil {
					return err
				}
				diffSlice[i] = d
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return RepoResult{Path: path, Since: since, Until: until, Err: err}
		}
		for i, c := range commits {
			diffs[c.Hash] = diffSlice[i]
		}
	}

	return RepoResult{
		Path:   path,
		Since:  since,
		Until:  until,
		Digest: AggregateDigest(commits, diffs, since, until),
	}
}
