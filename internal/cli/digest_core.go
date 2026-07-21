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

	return buildDigestArtifact(now, opts.Days, since, results), nil
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
// (§10.5/§10.6).
func buildDigestArtifact(generatedAt time.Time, days int, since time.Time, results []gitlog.RepoResult) render.DigestArtifact {
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
