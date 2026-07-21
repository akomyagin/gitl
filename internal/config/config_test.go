package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/pflag"
)

// writeFile is a helper that writes content to path, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func TestLoadDefaults(t *testing.T) {
	// No files, no env, no flags: pure built-in defaults.
	dir := t.TempDir()
	cfg, err := Load(Options{
		RepoDir:      dir,
		PersonalPath: filepath.Join(dir, "does-not-exist.yaml"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LLM.Provider != "openai" {
		t.Errorf("default provider = %q, want openai", cfg.LLM.Provider)
	}
	if cfg.LLM.Model != "gpt-4o-mini" {
		t.Errorf("default model = %q, want gpt-4o-mini", cfg.LLM.Model)
	}
	if cfg.LLM.MaxTokens != 1500 {
		t.Errorf("default max_tokens = %d, want 1500", cfg.LLM.MaxTokens)
	}
	if !cfg.OfflineMode() {
		t.Error("expected offline mode with empty api_key")
	}
}

// TestRepoOverridesPersonal is the documented-priority test: a repo-level
// .gitl.yaml must override the personal config file.
func TestRepoOverridesPersonal(t *testing.T) {
	personalDir := t.TempDir()
	repoDir := t.TempDir()

	personalPath := filepath.Join(personalDir, "config.yaml")
	writeFile(t, personalPath, `
llm:
  model: "personal-model"
  base_url: "https://personal.example/v1"
  max_tokens: 100
`)

	writeFile(t, filepath.Join(repoDir, ".gitl.yaml"), `
llm:
  model: "repo-model"
`)

	cfg, err := Load(Options{RepoDir: repoDir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Repo file wins for the key it sets.
	if cfg.LLM.Model != "repo-model" {
		t.Errorf("model = %q, want repo-model (repo .gitl.yaml must override personal)", cfg.LLM.Model)
	}
	// Personal value survives where the repo file is silent.
	if cfg.LLM.BaseURL != "https://personal.example/v1" {
		t.Errorf("base_url = %q, want personal value to survive", cfg.LLM.BaseURL)
	}
	if cfg.LLM.MaxTokens != 100 {
		t.Errorf("max_tokens = %d, want 100 from personal", cfg.LLM.MaxTokens)
	}
	// Untouched key falls back to the built-in default.
	if cfg.LLM.Provider != "openai" {
		t.Errorf("provider = %q, want default openai", cfg.LLM.Provider)
	}
}

// TestEnvOverridesFiles proves env beats both config files, and that the
// documented GITL_API_KEY special case binds to llm.api_key.
func TestEnvOverridesFiles(t *testing.T) {
	personalDir := t.TempDir()
	repoDir := t.TempDir()

	personalPath := filepath.Join(personalDir, "config.yaml")
	writeFile(t, personalPath, "llm:\n  model: personal-model\n")
	writeFile(t, filepath.Join(repoDir, ".gitl.yaml"), "llm:\n  model: repo-model\n")

	t.Setenv("GITL_LLM_MODEL", "env-model")
	t.Setenv("GITL_API_KEY", "sk-secret")

	cfg, err := Load(Options{RepoDir: repoDir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LLM.Model != "env-model" {
		t.Errorf("model = %q, want env-model (env must beat repo file)", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "sk-secret" {
		t.Errorf("api_key = %q, want sk-secret from GITL_API_KEY", cfg.LLM.APIKey)
	}
	if cfg.OfflineMode() {
		t.Error("expected online mode with GITL_API_KEY set")
	}
}

// TestEnvPolicyListKeys proves the two policy list keys are reachable via env.
// Viper's AutomaticEnv only consults env vars for keys it already knows about,
// so without defaults() entries for policy.exclude_globs /
// policy.required_changelog_categories these env vars were silently ignored.
// Comma-separated values decode via viper's default StringToSlice hook.
func TestEnvPolicyListKeys(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("GITL_POLICY_EXCLUDE_GLOBS", "*.gen.go,docs/**")
	t.Setenv("GITL_POLICY_REQUIRED_CHANGELOG_CATEGORIES", "Added,Fixed")

	cfg, err := Load(Options{
		RepoDir:      dir,
		PersonalPath: filepath.Join(dir, "does-not-exist.yaml"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.Policy.ExcludeGlobs, []string{"*.gen.go", "docs/**"}; !slices.Equal(got, want) {
		t.Errorf("policy.exclude_globs = %v, want %v from env", got, want)
	}
	if got, want := cfg.Policy.RequiredChangelogCategories, []string{"Added", "Fixed"}; !slices.Equal(got, want) {
		t.Errorf("policy.required_changelog_categories = %v, want %v from env", got, want)
	}
}

// TestFlagOverridesEverything proves an explicitly-set flag wins over env and
// files, while an unset flag does not clobber lower layers.
func TestFlagOverridesEverything(t *testing.T) {
	personalDir := t.TempDir()
	repoDir := t.TempDir()
	personalPath := filepath.Join(personalDir, "config.yaml")
	writeFile(t, personalPath, "llm:\n  model: personal-model\n  provider: openai\n")
	writeFile(t, filepath.Join(repoDir, ".gitl.yaml"), "llm:\n  model: repo-model\n")

	t.Setenv("GITL_LLM_MODEL", "env-model")

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("model", "flag-default", "")
	flags.String("provider", "flag-default", "")
	// Only --model is explicitly set; --provider is left at its default.
	if err := flags.Parse([]string{"--model", "flag-model"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	cfg, err := Load(Options{RepoDir: repoDir, PersonalPath: personalPath, Flags: flags})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LLM.Model != "flag-model" {
		t.Errorf("model = %q, want flag-model (explicit flag must win)", cfg.LLM.Model)
	}
	// --provider was not set, so the file value must survive (not "flag-default").
	if cfg.LLM.Provider != "openai" {
		t.Errorf("provider = %q, want openai (unset flag must not clobber files)", cfg.LLM.Provider)
	}
}

func TestValidateRejectsBadTimeout(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "llm:\n  timeout_seconds: 0\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err == nil {
		t.Error("expected error for timeout_seconds = 0")
	}
}

// TestValidateRejectsBadFailOn guards against a silent misfire: an unrecognized
// policy.fail_on value must be a loud config error, not fall through to
// llm.RiskAtLeast's rank lookup (where an unknown threshold ranks below every
// real risk level and would fail-gate on every review — the opposite of the
// project's "default WARN, hard gate is explicit opt-in" principle).
func TestValidateRejectsBadFailOn(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "policy:\n  fail_on: hgih\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err == nil {
		t.Error("expected error for policy.fail_on = \"hgih\"")
	}
}

// TestValidateNormalizesFailOnCase proves a mixed-case fail_on value (e.g. from
// a flag or YAML) is accepted and normalized, not rejected.
func TestValidateNormalizesFailOnCase(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "policy:\n  fail_on: High\n")
	cfg, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Policy.FailOn != "high" {
		t.Errorf("fail_on = %q, want normalized \"high\"", cfg.Policy.FailOn)
	}
}

// TestValidateRejectsMissingPromptTemplate: a missing prompt template file must
// cause a config-load error in online mode (api_key set).
func TestValidateRejectsMissingPromptTemplate(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	missing := filepath.Join(dir, "no-such.tmpl")
	// api_key must be set so validate() is in online mode and actually checks
	// the system template file.
	writeFile(t, personalPath, "llm:\n  api_key: test-key\nprompt:\n  system_template_file: "+missing+"\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err == nil {
		t.Error("expected error for missing prompt.system_template_file in online mode")
	}
}

// TestValidateSkipsPromptTemplateInOfflineMode: when no api_key is configured
// (offline mode), an inaccessible system template file must not block Load so
// that deterministic offline reviews remain available.
func TestValidateSkipsPromptTemplateInOfflineMode(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	missing := filepath.Join(dir, "no-such.tmpl")
	writeFile(t, personalPath, "prompt:\n  system_template_file: "+missing+"\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err != nil {
		t.Errorf("offline mode must not validate prompt.system_template_file: %v", err)
	}
}

// TestValidateAcceptsOutputTemplateWithFuncMapFunctions: an output template
// that calls upper or trimTrailingNewlines must not be rejected at config load.
func TestValidateAcceptsOutputTemplateWithFuncMapFunctions(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.md.tmpl")
	writeFile(t, outPath, "Risk={{upper .RiskLevel}}")
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "output:\n  template_file: "+outPath+"\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err != nil {
		t.Errorf("output template using 'upper' must be accepted at config load: %v", err)
	}
}

// TestValidateRejectsInvalidOutputTemplate: a template file with a syntax error
// must fail at config load, not mid-render (Item 3).
func TestValidateRejectsInvalidOutputTemplate(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "bad.md.tmpl")
	writeFile(t, tmplPath, "Risk={{.RiskLevel") // unterminated action
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "output:\n  template_file: "+tmplPath+"\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err == nil {
		t.Error("expected error for invalid output.template_file")
	}
}

// TestValidateAcceptsValidTemplates: valid template files load without error.
func TestValidateAcceptsValidTemplates(t *testing.T) {
	dir := t.TempDir()
	sysPath := filepath.Join(dir, "sys.tmpl")
	writeFile(t, sysPath, "Reviewer for {{.Range}}")
	outPath := filepath.Join(dir, "out.md.tmpl")
	writeFile(t, outPath, "Risk={{.RiskLevel}}")
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "prompt:\n  system_template_file: "+sysPath+"\noutput:\n  template_file: "+outPath+"\n")
	cfg, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Prompt.SystemTemplateFile != sysPath || cfg.Output.TemplateFile != outPath {
		t.Errorf("template paths not loaded: prompt=%q output=%q", cfg.Prompt.SystemTemplateFile, cfg.Output.TemplateFile)
	}
}

// TestDigestRepoRelativePathResolvedAgainstRepoDir is the regression test for
// resolveDigestRepoPaths' actual join branch (docs/TECHNICAL_PLAN.md §10.4):
// a relative digest.repos[].path must be resolved against repoDir (the
// directory containing the repo-level .gitl.yaml that declared it), not left
// as-is or resolved against the process cwd. Uses a genuinely relative path
// ("../other-repo") so the early-return branches for "" and already-absolute
// paths are not hit — those are already exercised indirectly by the
// cli-level digest tests, which only ever pass absolute t.TempDir() paths.
func TestDigestRepoRelativePathResolvedAgainstRepoDir(t *testing.T) {
	repoDir := t.TempDir()
	personalPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	writeFile(t, filepath.Join(repoDir, ".gitl.yaml"), "digest:\n  repos:\n    - path: \"../other-repo\"\n")

	cfg, err := Load(Options{RepoDir: repoDir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Digest.Repos) != 1 {
		t.Fatalf("Digest.Repos = %+v, want exactly 1 entry", cfg.Digest.Repos)
	}
	want := filepath.Join(repoDir, "../other-repo")
	if got := cfg.Digest.Repos[0].Path; got != want {
		t.Errorf("Digest.Repos[0].Path = %q, want %q (relative path joined against repoDir)", got, want)
	}
	if !filepath.IsAbs(cfg.Digest.Repos[0].Path) {
		t.Errorf("Digest.Repos[0].Path = %q, want an absolute path after resolution", cfg.Digest.Repos[0].Path)
	}
}

// TestDigestRepoAbsolutePathLeftUntouched documents the sibling branch: an
// already-absolute digest.repos[].path must survive Load() unchanged, not be
// re-joined against repoDir.
func TestDigestRepoAbsolutePathLeftUntouched(t *testing.T) {
	repoDir := t.TempDir()
	personalPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	absPath := filepath.Join(t.TempDir(), "some-service")
	writeFile(t, filepath.Join(repoDir, ".gitl.yaml"), "digest:\n  repos:\n    - path: \""+filepath.ToSlash(absPath)+"\"\n")

	cfg, err := Load(Options{RepoDir: repoDir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Digest.Repos) != 1 {
		t.Fatalf("Digest.Repos = %+v, want exactly 1 entry", cfg.Digest.Repos)
	}
	if got := cfg.Digest.Repos[0].Path; got != absPath {
		t.Errorf("Digest.Repos[0].Path = %q, want unchanged %q", got, absPath)
	}
}
