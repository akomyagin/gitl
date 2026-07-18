package cli

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/llmcache"
	"github.com/akomyagin/gitl/internal/prompt"
	"github.com/akomyagin/gitl/internal/render"
)

// newChangelogCmd builds the `gitl changelog [<range>]` command (§9.1).
func newChangelogCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "changelog [<range>] [--ai]",
		Short: "Keep a Changelog-style summary of a commit range, grouped by conventional-commit type",
		Long: "changelog runs `git log` over the given revision range and groups commits into\n" +
			"Keep a Changelog categories (Added/Changed/Deprecated/Removed/Fixed/Security/Other)\n" +
			"based on their conventional-commit prefix (feat:, fix:, ...).\n\n" +
			"<range> is optional: without it, changelog uses <latest-tag>..HEAD, or the full\n" +
			"history if the repository has no tags yet.\n\n" +
			"By default the categorization is fully deterministic from commit metadata — no\n" +
			"LLM call, online or offline, with or without an API key. --ai optionally routes\n" +
			"the grouped result through the model for more readable prose and reclassification\n" +
			"of significant non-conventional commits out of Other; it falls back to the\n" +
			"deterministic changelog (with a warning, exit 0) without an API key or on a\n" +
			"malformed model response. --dry-run/--max-cost-usd/--no-cache apply only with --ai.\n\n" +
			"policy.required_changelog_categories (if set) is checked after categorization\n" +
			"(the AI one when --ai is in effect): an empty required category prints a warning\n" +
			"to stderr but never fails the command.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChangelog(cmd.Context(), cmd, gf, args)
		},
	}

	cmd.Flags().String("format", "", "output format (md | text | json)")
	cmd.Flags().Bool("ai", false, "rewrite the grouped changelog with the LLM (falls back to deterministic without an API key)")
	// Flags bound into config (see config.bindChangedFlags), same set as review
	// minus review-only concerns; only override config when explicitly set.
	cmd.Flags().String("provider", "", "LLM provider (openai | ollama | azure_openai); only used with --ai")
	cmd.Flags().String("model", "", "model name; only used with --ai")
	cmd.Flags().String("base-url", "", "LLM API base URL; only used with --ai")
	cmd.Flags().Float64("max-cost-usd", 0, "block the request if the estimated cost exceeds this (<=0 disables the guard); only used with --ai")
	cmd.Flags().Bool("dry-run", false, "print a cost estimate and exit without calling the API; only used with --ai")
	cmd.Flags().Bool("no-cache", false, "skip LLM response cache (always call the API); only used with --ai")

	return cmd
}

// runChangelog executes the changelog pipeline for one revision range.
func runChangelog(ctx context.Context, cmd *cobra.Command, gf *globalFlags, args []string) error {
	cfg, err := loadConfig(cmd, gf)
	if err != nil {
		return err
	}

	runner, err := gitlog.NewRunner("")
	if err != nil {
		return err
	}

	revRange, err := resolveChangelogRange(ctx, runner, args)
	if err != nil {
		return err
	}

	slog.Debug("collecting git history for changelog", "range", revRange)
	commits, err := runner.Log(ctx, revRange)
	if err != nil {
		return err
	}

	// The deterministic categorization is always computed: it is the whole
	// output on the default path, and both the fallback and the model's
	// "starting point" on the --ai path.
	cl := gitlog.CategorizeCommits(commits)

	ai, err := cmd.Flags().GetBool("ai")
	if err != nil {
		return err
	}
	// An empty range never calls the model, even with --ai — there is nothing
	// to rewrite, and the deterministic "No changes" output is free.
	if ai && len(commits) > 0 {
		done, err := runChangelogAI(ctx, cmd, cfg, revRange, commits, cl)
		if done || err != nil {
			return err
		}
		// done == false: offline mode or a malformed model response — fall
		// through to the deterministic path below (a warning was printed).
	}

	missing := gitlog.MissingRequiredCategories(cl, cfg.Policy.RequiredChangelogCategories)
	for _, name := range missing {
		slog.Warn(fmt.Sprintf("required changelog category %q has no entries in range %q", name, revRange))
	}

	art := render.NewChangelogArtifact(time.Now().UTC(), revRange, cl, missing)
	return render.RenderChangelog(cmd.OutOrStdout(), art, render.Format(cfg.Output.Format))
}

// runChangelogAI attempts the --ai changelog path. done == true means the
// command was fully handled here (output rendered, or a hard error to
// propagate); done == false means the caller must fall back to the
// deterministic changelog (offline mode or a malformed model response — the
// warning has already been printed, never a failure). Operational API errors
// (429/5xx after retries, network) ARE hard errors: the user explicitly asked
// for --ai and has a key, so a silent deterministic swap would be misleading —
// symmetric with review.
func runChangelogAI(ctx context.Context, cmd *cobra.Command, cfg *config.Config, revRange string, commits []gitlog.Commit, cl gitlog.Changelog) (done bool, err error) {
	if cfg.OfflineMode() {
		fmt.Fprintln(cmd.ErrOrStderr(), "gitl: --ai requested but no LLM API key configured — falling back to the deterministic changelog (set GITL_API_KEY for AI prose).")
		return false, nil
	}

	system, user, err := prompt.BuildChangelogWithTemplate(prompt.Changelog{
		Range:   revRange,
		Commits: commits,
		Grouped: cl,
	}, cfg.Prompt.SystemTemplateFile)
	if err != nil {
		return true, fmt.Errorf("prompt template: %w", err)
	}

	// LLM response cache: same conditions as review (offline is already
	// excluded above). The RAW model response is cached, not the parsed
	// artifact, so a single cache hit serves every --format.
	noCache, _ := cmd.Flags().GetBool("no-cache")
	useCache := cfg.Cache.Enabled && !noCache && cfg.Cache.TTLHours > 0
	var (
		cache    *llmcache.Cache
		cacheKey string
	)
	if useCache {
		if c, cerr := llmcache.New(time.Duration(cfg.Cache.TTLHours) * time.Hour); cerr == nil {
			cache = c
			cacheKey = llmcache.Key(cfg.LLM.Provider, cfg.LLM.Model, system, user)
			if resp, ok, _ := cache.Get(cacheKey); ok {
				if payload, pok := llm.ParseChangelogResponse(resp.Content); pok {
					slog.Debug("llm cache hit", "key", cacheKey[:12])
					return true, renderAIChangelog(cmd, cfg, revRange, commits, payload)
				}
				// A cached entry that no longer parses is treated as a miss.
				slog.Debug("llm cache entry unparsable; ignoring", "key", cacheKey[:12])
			}
		} else {
			slog.Debug("llm cache unavailable", "err", cerr)
		}
	}

	// --dry-run: print the estimate, no network call, exit 0.
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		return true, printDryRun(cmd, cfg, system+"\n"+user)
	}

	// Cost guard runs automatically before calling the provider (§8.4).
	if err := costGuard(cmd, cfg, system+"\n"+user); err != nil {
		return true, err
	}

	client, err := newNetworkClient(cfg)
	if err != nil {
		return true, err
	}

	slog.Debug("requesting AI changelog", "commits", len(commits))
	content, err := client.CompleteRaw(ctx, llm.Request{
		System:      system,
		User:        user,
		Model:       cfg.LLM.Model,
		MaxTokens:   cfg.LLM.MaxTokens,
		Temperature: cfg.LLM.Temperature,
	})
	if err != nil {
		return true, fmt.Errorf("changelog --ai failed: %w", err)
	}

	payload, ok := llm.ParseChangelogResponse(content)
	if !ok {
		fmt.Fprintln(cmd.ErrOrStderr(), "gitl: model response carried no valid ```changelog block — falling back to the deterministic changelog.")
		return false, nil
	}

	if cache != nil && cacheKey != "" {
		if err := cache.Put(cacheKey, llm.Response{Content: content}); err != nil {
			slog.Debug("llm cache put failed", "err", err)
		}
	}

	return true, renderAIChangelog(cmd, cfg, revRange, commits, payload)
}

// renderAIChangelog converts the parsed model payload into the standard
// changelog artifact and renders it with the existing renderer — all three
// --format values work off the same artifact.
func renderAIChangelog(cmd *cobra.Command, cfg *config.Config, revRange string, commits []gitlog.Commit, payload llm.ChangelogPayload) error {
	art := aiChangelogArtifact(time.Now().UTC(), revRange, commits, payload, cfg.Policy.RequiredChangelogCategories)
	for _, name := range art.MissingRequiredCategories {
		slog.Warn(fmt.Sprintf("required changelog category %q has no entries in range %q", name, revRange))
	}
	return render.RenderChangelog(cmd.OutOrStdout(), art, render.Format(cfg.Output.Format))
}

// aiChangelogArtifact converts the model's structured payload into a render
// artifact, defensively:
//   - hashes not present in the range are dropped (debug-logged), and an entry
//     whose hashes are ALL unknown is discarded entirely — an invented change,
//     per the prompt's no-invention rule;
//   - unknown category names are remapped to Other (debug-logged), appended in
//     sorted name order for deterministic output;
//   - MissingRequiredCategories is recomputed from the AI categories: the
//     policy check applies to what is actually printed, not to the
//     deterministic starting point.
func aiChangelogArtifact(generatedAt time.Time, revRange string, commits []gitlog.Commit, payload llm.ChangelogPayload, required []string) render.ChangelogArtifact {
	known := make(map[string]bool, len(commits))
	for _, c := range commits {
		h := c.Hash
		if len(h) > 7 {
			h = h[:7]
		}
		known[h] = true
	}

	// validatedEntry carries the surviving hashes as a []string all the way
	// through validation and breaking-intersection checks; the display string
	// ("h1, h2") is formatted exactly once, at the very end, when the final
	// render.ChangelogEntry is built — no code ever re-splits a formatted
	// string back into hashes.
	type validatedEntry struct {
		hashes  []string
		subject string
	}

	// toEntry validates one model item: keep only hashes that exist in the
	// range (normalized to the short form), require a non-empty subject and at
	// least one surviving hash.
	toEntry := func(it llm.ChangelogItem) (validatedEntry, bool) {
		var hashes []string
		for _, h := range it.Hashes {
			h = strings.TrimSpace(h)
			if len(h) > 7 {
				h = h[:7]
			}
			if known[h] {
				hashes = append(hashes, h)
			} else if h != "" {
				slog.Debug("dropping unknown hash from AI changelog entry", "hash", h)
			}
		}
		subject := strings.TrimSpace(it.Subject)
		if subject == "" || len(hashes) == 0 {
			slog.Debug("dropping AI changelog entry with no valid hash or subject", "subject", it.Subject)
			return validatedEntry{}, false
		}
		return validatedEntry{hashes: hashes, subject: subject}, true
	}

	validCategory := make(map[string]bool, len(gitlog.CategoryOrder))
	for _, name := range gitlog.CategoryOrder {
		validCategory[name] = true
	}

	categoryEntries := make(map[string][]validatedEntry, len(gitlog.CategoryOrder))
	for _, name := range gitlog.CategoryOrder {
		for _, it := range payload.Categories[name] {
			if e, ok := toEntry(it); ok {
				categoryEntries[name] = append(categoryEntries[name], e)
			}
		}
	}
	// Unknown category names → Other, in sorted order for determinism (map
	// iteration order must never leak into the output).
	unknown := make([]string, 0, len(payload.Categories))
	for name := range payload.Categories {
		if !validCategory[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		slog.Debug("remapping unknown AI changelog category to Other", "category", name)
		for _, it := range payload.Categories[name] {
			if e, ok := toEntry(it); ok {
				categoryEntries[gitlog.CategoryOther] = append(categoryEntries[gitlog.CategoryOther], e)
			}
		}
	}

	breakingEntries := make([]validatedEntry, 0, len(payload.Breaking))
	breakingHashes := make(map[string]bool)
	for _, it := range payload.Breaking {
		e, ok := toEntry(it)
		if !ok {
			continue
		}
		breakingEntries = append(breakingEntries, e)
		for _, h := range e.hashes {
			breakingHashes[h] = true
		}
	}

	// The single point where the hash list is formatted for display.
	toRenderEntry := func(e validatedEntry, isBreaking bool) render.ChangelogEntry {
		return render.ChangelogEntry{
			Hash:     strings.Join(e.hashes, ", "),
			Subject:  e.subject,
			Breaking: isBreaking,
		}
	}

	// Category entries that share ANY hash with a breaking entry get the
	// **BREAKING** prefix in the md/text renderers, as usual.
	categories := make(map[string][]render.ChangelogEntry, len(categoryEntries))
	for name, entries := range categoryEntries {
		list := make([]render.ChangelogEntry, 0, len(entries))
		for _, e := range entries {
			isBreaking := false
			for _, h := range e.hashes {
				if breakingHashes[h] {
					isBreaking = true
					break
				}
			}
			list = append(list, toRenderEntry(e, isBreaking))
		}
		categories[name] = list
	}

	breaking := make([]render.ChangelogEntry, 0, len(breakingEntries))
	for _, e := range breakingEntries {
		breaking = append(breaking, toRenderEntry(e, true))
	}

	// The required-categories policy is checked against the AI result — the
	// categories the user actually sees.
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if len(categories[name]) == 0 {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)

	return render.ChangelogArtifact{
		GeneratedAt:               generatedAt,
		Range:                     revRange,
		Categories:                categories,
		Breaking:                  breaking,
		MissingRequiredCategories: missing,
	}
}

// resolveChangelogRange returns the explicit range argument if given, or
// falls back to <latest-tag>..HEAD, or plain "HEAD" if the repo has no tags
// at all (§9.1). A failure to describe a tag is not fatal.
func resolveChangelogRange(ctx context.Context, runner *gitlog.Runner, args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}

	tag, err := runner.LatestTag(ctx)
	if err != nil {
		return "", err
	}
	if tag == "" {
		slog.Debug("no tags found; defaulting changelog range to full history")
		return "HEAD", nil
	}
	return tag + "..HEAD", nil
}
