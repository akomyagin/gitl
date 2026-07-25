package cli

// The cmd-free core of `gitl digest`: everything a non-CLI caller (e.g. a
// future MCP server) needs to compute a digest artifact, with zero dependency
// on cobra/pflag. The cobra wrapper (runDigest in digest.go) only parses flags,
// calls RunDigestCore, and renders the result (plain or --tui).

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/render"
	"github.com/akomyagin/gitl/internal/riskhistory"
)

// DigestOptions carries the per-run digest parameters. Deliberately plain Go
// types only — no *cobra.Command, no *pflag.FlagSet — so non-CLI callers can
// fill it directly (config.Load with Options{Flags: nil} still applies
// defaults→file→env).
type DigestOptions struct {
	// Days is the size of the activity window in days (the --days flag).
	// Must be > 0.
	Days int
	// Repos, when non-empty, is the explicit repository path list (the --repos
	// flag): it replaces cfg digest.repos wholesale (§10.4). Entries are
	// trimmed, empty ones skipped, relative paths made absolute. When empty,
	// cfg.Digest.Repos is used, falling back to the current directory.
	Repos []string
}

// RunDigestCore executes the (possibly multi-repo) digest pipeline without any
// cobra dependency and without writing the result anywhere: it resolves the
// repository list, collects per-repo digests concurrently, and returns the
// final artifact ready for the render package (md|text|json) or the TUI.
func RunDigestCore(ctx context.Context, cfg *config.Config, opts DigestOptions) (render.DigestArtifact, error) {
	if opts.Days <= 0 {
		return render.DigestArtifact{}, fmt.Errorf("--days must be a positive integer, got %d", opts.Days)
	}

	repoPaths, err := digestRepoPaths(cfg, opts.Repos)
	if err != nil {
		return render.DigestArtifact{}, err
	}

	now := time.Now().UTC()
	since := now.AddDate(0, 0, -opts.Days)

	concurrency := gitlog.DefaultConcurrency(len(repoPaths))
	slog.Debug("collecting digest", "repos", len(repoPaths), "days", opts.Days, "concurrency", concurrency)
	results := gitlog.CollectDigests(ctx, repoPaths, since, concurrency)

	trends := digestRiskTrends(ctx, cfg, results, opts.Days, now)
	return buildDigestArtifact(now, opts.Days, since, results, trends), nil
}

// digestRiskTrends loads the local risk-history log once and aggregates a
// per-repo trend for every successful repo result, keyed by the result's Path.
// Entirely best-effort: an unavailable/unreadable log, a failed repo-key
// resolution, or an empty window all just mean "no trend attached" — the
// digest must NEVER fail (or even warn) because of the risk log. Returns nil
// when policy.risk_log_enabled is false (digest behaves exactly as before).
func digestRiskTrends(ctx context.Context, cfg *config.Config, results []gitlog.RepoResult, days int, now time.Time) map[string]*render.DigestRiskTrend {
	if !cfg.Policy.RiskLogEnabled {
		return nil
	}
	log, err := riskhistory.New()
	if err != nil {
		slog.Debug("risk history unavailable", "err", err)
		return nil
	}
	records, err := log.Load()
	if err != nil {
		slog.Debug("risk history unreadable, treating as empty", "err", err)
		return nil
	}
	if len(records) == 0 {
		return nil
	}

	trends := make(map[string]*render.DigestRiskTrend)
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		// CollectDigests yields worktree paths, not runners — build a Runner
		// for the repo (one cheap `git remote get-url origin` per repo) so the
		// reader computes the SAME identity key the review writer used.
		runner, rerr := gitlog.NewRunner(r.Path)
		if rerr != nil {
			continue
		}
		key := riskhistory.RepoKey(ctx, runner)
		trend := riskhistory.Aggregate(records, key, days, now)
		if trend.Total > 0 {
			trends[r.Path] = toRenderRiskTrend(trend)
		}
	}
	return trends
}

// digestRepoPaths determines the repository path list (§10.4): explicit paths
// (the --repos flag, or DigestOptions.Repos from a non-CLI caller), if any,
// replace digest.repos wholesale; otherwise digest.repos is used; otherwise
// the digest runs single-repo against the current directory.
func digestRepoPaths(cfg *config.Config, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		paths := normalizeRepoPaths(explicit)
		if len(paths) == 0 {
			return nil, fmt.Errorf("--repos was set but contained no repository paths")
		}
		return paths, nil
	}

	if len(cfg.Digest.Repos) > 0 {
		raw := make([]string, 0, len(cfg.Digest.Repos))
		for _, r := range cfg.Digest.Repos {
			raw = append(raw, r.Path)
		}
		paths := normalizeRepoPaths(raw)
		if len(paths) == 0 {
			return nil, fmt.Errorf("digest.repos configured but contained no non-empty repository paths")
		}
		return paths, nil
	}

	return []string{"."}, nil
}

// normalizeRepoPaths trims entries, drops empty ones (an empty path must never
// be silently absolutized to the CWD), and makes relative paths absolute.
func normalizeRepoPaths(raw []string) []string {
	paths := make([]string, 0, len(raw))
	for _, r := range raw {
		p := strings.TrimSpace(r)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
		}
		paths = append(paths, p)
	}
	return paths
}

// buildDigestArtifact converts worker-pool results into the render artifact
// (§10.5/§10.6). trends (nil when risk logging is disabled or there is no
// history) attaches the optional per-repo risk trend, keyed by repo path.
func buildDigestArtifact(generatedAt time.Time, days int, since time.Time, results []gitlog.RepoResult, trends map[string]*render.DigestRiskTrend) render.DigestArtifact {
	repos := make([]render.RepoDigest, 0, len(results))
	for _, r := range results {
		if r.Err != nil {
			repos = append(repos, render.RepoDigest{Path: r.Path, Ok: false, Err: r.Err.Error()})
			continue
		}
		repos = append(repos, render.RepoDigest{
			Path:         r.Path,
			Ok:           true,
			Commits:      r.Digest.Commits,
			FilesChanged: r.Digest.FilesChanged,
			LinesAdded:   r.Digest.LinesAdded,
			LinesRemoved: r.Digest.LinesRemoved,
			ByAuthor:     toRenderAuthors(r.Digest.ByAuthor),
			ByTopic:      toRenderTopics(r.Digest.ByTopic),
			TopFiles:     toRenderFiles(r.Digest.TopFiles),
			RiskTrend:    trends[r.Path],
		})
	}

	return render.DigestArtifact{
		GeneratedAt: generatedAt,
		Days:        days,
		Since:       since,
		Until:       generatedAt,
		Repos:       repos,
	}
}

// toRenderRiskTrend maps riskhistory.Trend into the render-owned
// DigestRiskTrend (render never imports riskhistory — same pattern as
// toRenderAuthors below).
func toRenderRiskTrend(t riskhistory.Trend) *render.DigestRiskTrend {
	out := &render.DigestRiskTrend{
		Total:        t.Total,
		Low:          t.Counts["low"],
		Medium:       t.Counts["medium"],
		High:         t.Counts["high"],
		RecentHigh:   t.RecentHigh,
		PreviousHigh: t.PreviousHigh,
		Recent:       make([]render.DigestRiskEntry, 0, len(t.Recent)),
	}
	for _, rec := range t.Recent {
		out.Recent = append(out.Recent, render.DigestRiskEntry{
			When:  rec.Timestamp,
			Range: rec.Range,
			Level: rec.RiskLevel,
		})
	}
	return out
}

func toRenderAuthors(stats []gitlog.AuthorStat) []render.DigestAuthorStat {
	out := make([]render.DigestAuthorStat, 0, len(stats))
	for _, a := range stats {
		out = append(out, render.DigestAuthorStat{
			Author: a.Author, Commits: a.Commits,
			LinesAdded: a.LinesAdded, LinesRemoved: a.LinesRemoved,
		})
	}
	return out
}

func toRenderTopics(stats []gitlog.TopicStat) []render.DigestTopicStat {
	out := make([]render.DigestTopicStat, 0, len(stats))
	for _, t := range stats {
		out = append(out, render.DigestTopicStat{Topic: t.Topic, Commits: t.Commits})
	}
	return out
}

func toRenderFiles(stats []gitlog.FileStat) []render.DigestFileStat {
	out := make([]render.DigestFileStat, 0, len(stats))
	for _, f := range stats {
		out = append(out, render.DigestFileStat{Path: f.Path, Commits: f.Commits})
	}
	return out
}
