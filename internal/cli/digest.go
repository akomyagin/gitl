package cli

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/render"
	"github.com/akomyagin/gitl/internal/tui"
)

// defaultDigestDays is the default --days window (§10.1).
const defaultDigestDays = 7

// newDigestCmd builds the `gitl digest` command (§10.1/§10.4).
func newDigestCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "digest",
		Short: "Deterministic activity summary over the last N days, optionally across multiple repos",
		Long: "digest aggregates git history from the last --days days by author, by\n" +
			"conventional-commit topic, and by most-changed files. It never calls an LLM —\n" +
			"the aggregation is fully deterministic from commit metadata.\n\n" +
			"--repos=a,b,c runs the digest concurrently over multiple repositories (a bounded\n" +
			"worker pool) and adds a combined overall summary. A repository that is missing,\n" +
			"not a git repository, or otherwise fails is reported as a per-repo error and does\n" +
			"not abort the others. --repos replaces digest.repos from .gitl.yaml entirely (it\n" +
			"does not merge with it). Without --repos or digest.repos, digest runs against the\n" +
			"current directory as a single repo.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDigest(cmd.Context(), cmd, gf)
		},
	}

	cmd.Flags().Int("days", defaultDigestDays, "size of the activity window in days (must be > 0)")
	cmd.Flags().String("repos", "", "comma-separated list of repository paths (overrides digest.repos entirely)")
	cmd.Flags().String("format", "", "output format (md | text | json)")
	cmd.Flags().Bool("tui", false, "interactive TUI viewer (requires a terminal)")

	return cmd
}

// runDigest executes the (possibly multi-repo) digest pipeline.
func runDigest(ctx context.Context, cmd *cobra.Command, gf *globalFlags) error {
	cfg, err := loadConfig(cmd, gf)
	if err != nil {
		return err
	}

	days, err := cmd.Flags().GetInt("days")
	if err != nil {
		return err
	}
	if days <= 0 {
		return fmt.Errorf("--days must be a positive integer, got %d", days)
	}

	repoPaths, err := resolveDigestRepos(cmd, cfg)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	since := now.AddDate(0, 0, -days)

	concurrency := gitlog.DefaultConcurrency(len(repoPaths))
	slog.Debug("collecting digest", "repos", len(repoPaths), "days", days, "concurrency", concurrency)
	results := gitlog.CollectDigests(ctx, repoPaths, since, concurrency)

	art := buildDigestArtifact(now, days, since, results)

	tuiFlag, _ := cmd.Flags().GetBool("tui")
	if tuiFlag {
		if !isTerminal(cmd.OutOrStdout()) {
			fmt.Fprintln(cmd.ErrOrStderr(), "gitl: --tui requires a terminal — falling back to plain output")
		} else {
			if cmd.Flags().Changed("format") {
				fmt.Fprintln(cmd.ErrOrStderr(), "gitl: --tui ignores --format (interactive view)")
			}
			return tui.Run(ctx, art)
		}
	}
	return render.RenderDigest(cmd.OutOrStdout(), art, render.Format(cfg.Output.Format))
}

// resolveDigestRepos determines the repository path list (§10.4): --repos,
// if set, replaces digest.repos wholesale; otherwise digest.repos is used;
// otherwise digest runs single-repo against the current directory.
func resolveDigestRepos(cmd *cobra.Command, cfg *config.Config) ([]string, error) {
	reposFlag, err := cmd.Flags().GetString("repos")
	if err != nil {
		return nil, err
	}
	if cmd.Flags().Changed("repos") {
		var paths []string
		for _, raw := range strings.Split(reposFlag, ",") {
			p := strings.TrimSpace(raw)
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
		if len(paths) == 0 {
			return nil, fmt.Errorf("--repos was set but contained no repository paths")
		}
		return paths, nil
	}

	if len(cfg.Digest.Repos) > 0 {
		paths := make([]string, 0, len(cfg.Digest.Repos))
		for _, r := range cfg.Digest.Repos {
			p := r.Path
			if !filepath.IsAbs(p) {
				if abs, err := filepath.Abs(p); err == nil {
					p = abs
				}
			}
			paths = append(paths, p)
		}
		return paths, nil
	}

	return []string{"."}, nil
}

// buildDigestArtifact converts worker-pool results into the render artifact
// (§10.5/§10.6).
func buildDigestArtifact(generatedAt time.Time, days int, since time.Time, results []gitlog.RepoResult) render.DigestArtifact {
	repos := make([]render.RepoDigest, 0, len(results))
	until := generatedAt
	for _, r := range results {
		if !r.Until.IsZero() {
			until = r.Until
		}
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
		Until:       until,
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
