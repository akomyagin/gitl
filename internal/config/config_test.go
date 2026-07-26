package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	// Remote cache tier is strictly opt-in: off by default.
	if cfg.Cache.Remote.URL != "" {
		t.Errorf("default cache.remote.url = %q, want empty (remote tier off)", cfg.Cache.Remote.URL)
	}
	if cfg.Cache.Remote.TokenEnv != "" {
		t.Errorf("default cache.remote.token_env = %q, want empty", cfg.Cache.Remote.TokenEnv)
	}
	if cfg.Cache.Remote.TimeoutMS != 3000 {
		t.Errorf("default cache.remote.timeout_ms = %d, want 3000", cfg.Cache.Remote.TimeoutMS)
	}
}

// TestEnvCacheRemoteKeys proves the cache.remote.* keys are reachable via env.
// Same viper quirk as TestEnvAzureOpenAIKeys: AutomaticEnv only consults env
// vars for keys registered in defaults(), so without the empty cache.remote.*
// defaults these env vars would be silently ignored.
func TestEnvCacheRemoteKeys(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("GITL_CACHE_REMOTE_URL", "https://cache.example.com/gitl")
	t.Setenv("GITL_CACHE_REMOTE_TOKEN_ENV", "GITL_REMOTE_CACHE_TOKEN")
	t.Setenv("GITL_CACHE_REMOTE_TIMEOUT_MS", "1500")

	cfg, err := Load(Options{
		RepoDir:      dir,
		PersonalPath: filepath.Join(dir, "does-not-exist.yaml"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Cache.Remote.URL != "https://cache.example.com/gitl" {
		t.Errorf("cache.remote.url = %q, want the env value", cfg.Cache.Remote.URL)
	}
	if cfg.Cache.Remote.TokenEnv != "GITL_REMOTE_CACHE_TOKEN" {
		t.Errorf("cache.remote.token_env = %q, want the env value", cfg.Cache.Remote.TokenEnv)
	}
	if cfg.Cache.Remote.TimeoutMS != 1500 {
		t.Errorf("cache.remote.timeout_ms = %d, want 1500 from env", cfg.Cache.Remote.TimeoutMS)
	}
}

// TestApplyProviderBaseURL: an empty llm.base_url must resolve to the
// provider's OWN native default endpoint after Load — never to the OpenAI URL
// for anthropic/gemini (the old unconditional default sent their auth headers,
// key included, to api.openai.com) — while an explicit config value always
// wins over any default.
func TestApplyProviderBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		baseURL  string // configured value; "" = omitted
		want     string
	}{
		{"openai empty", "openai", "", "https://api.openai.com/v1"},
		{"openai explicit", "openai", "https://proxy.example/v1", "https://proxy.example/v1"},
		{"ollama empty", "ollama", "", "http://localhost:11434/v1"},
		{"ollama explicit", "ollama", "http://gpu-box:11434/v1", "http://gpu-box:11434/v1"},
		{"anthropic empty", "anthropic", "", ""},
		{"anthropic explicit", "anthropic", "https://claude-proxy.example", "https://claude-proxy.example"},
		{"gemini empty", "gemini", "", ""},
		{"gemini explicit", "gemini", "https://gemini-proxy.example", "https://gemini-proxy.example"},
		{"azure empty", "azure_openai", "", ""},
		{"azure explicit", "azure_openai", "https://ignored.example", "https://ignored.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			yaml := "llm:\n  provider: " + tt.provider + "\n"
			if tt.baseURL != "" {
				yaml += "  base_url: \"" + tt.baseURL + "\"\n"
			}
			personalPath := filepath.Join(dir, "config.yaml")
			writeFile(t, personalPath, yaml)

			cfg, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.LLM.BaseURL != tt.want {
				t.Errorf("base_url = %q, want %q", cfg.LLM.BaseURL, tt.want)
			}
		})
	}
}

// TestBaseURLFlagWinsOverProviderDefault: an explicitly-set --base-url flag
// must survive the provider-default resolution, exactly like a file value.
func TestBaseURLFlagWinsOverProviderDefault(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "llm:\n  provider: ollama\n")

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("base-url", "", "")
	if err := flags.Parse([]string{"--base-url", "http://flag-host:11434/v1"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	cfg, err := Load(Options{RepoDir: dir, PersonalPath: personalPath, Flags: flags})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LLM.BaseURL != "http://flag-host:11434/v1" {
		t.Errorf("base_url = %q, want the explicit flag value to win over the ollama default", cfg.LLM.BaseURL)
	}
}

// TestOfflineModeProviderAware: an empty api_key means offline mode only for
// providers that actually need a key — the documented keyless ollama setup
// must reach the real network path, not silently degrade to the offline
// heuristic. Unknown providers default to "requires a key" (safe).
func TestOfflineModeProviderAware(t *testing.T) {
	tests := []struct {
		provider string
		apiKey   string
		want     bool
	}{
		{"openai", "", true},
		{"openai", "sk-x", false},
		{"ollama", "", false},
		{"ollama", "dummy", false},
		{"anthropic", "", true},
		{"gemini", "", true},
		{"azure_openai", "", true},
		{"unknown-provider", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/key="+tt.apiKey, func(t *testing.T) {
			cfg := &Config{LLM: LLMConfig{Provider: tt.provider, APIKey: tt.apiKey}}
			if got := cfg.OfflineMode(); got != tt.want {
				t.Errorf("OfflineMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEnvAzureOpenAIKeys proves the three Azure sub-keys are reachable via
// env. Same viper quirk as TestEnvPolicyListKeys: AutomaticEnv only consults
// env vars for keys registered in defaults(), so without the empty
// llm.azure_openai.* defaults these env vars were silently ignored.
func TestEnvAzureOpenAIKeys(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("GITL_LLM_AZURE_OPENAI_ENDPOINT", "https://acct.openai.azure.com")
	t.Setenv("GITL_LLM_AZURE_OPENAI_DEPLOYMENT", "prod-gpt4o")
	t.Setenv("GITL_LLM_AZURE_OPENAI_API_VERSION", "2024-06-01")

	cfg, err := Load(Options{
		RepoDir:      dir,
		PersonalPath: filepath.Join(dir, "does-not-exist.yaml"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LLM.AzureOpenAI.Endpoint != "https://acct.openai.azure.com" {
		t.Errorf("azure endpoint = %q, want the env value", cfg.LLM.AzureOpenAI.Endpoint)
	}
	if cfg.LLM.AzureOpenAI.Deployment != "prod-gpt4o" {
		t.Errorf("azure deployment = %q, want the env value", cfg.LLM.AzureOpenAI.Deployment)
	}
	if cfg.LLM.AzureOpenAI.APIVersion != "2024-06-01" {
		t.Errorf("azure api_version = %q, want the env value", cfg.LLM.AzureOpenAI.APIVersion)
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

// TestValidateRejectsBadOutputFormat proves a misspelled output.format is a
// loud config-load error (naming the valid options) instead of being caught
// only inside internal/render, after a paid LLM call already happened.
func TestValidateRejectsBadOutputFormat(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "output:\n  format: yaml\n")
	_, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
	if err == nil {
		t.Fatal("expected error for output.format = \"yaml\"")
	}
	if !strings.Contains(err.Error(), "md|text|json") {
		t.Errorf("error %q does not mention the valid options md|text|json", err)
	}
}

// TestValidateNormalizesOutputFormatCase proves a mixed-case output.format is
// accepted and normalized to lowercase, mirroring the fail_on pattern.
func TestValidateNormalizesOutputFormatCase(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "output:\n  format: MD\n")
	cfg, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Output.Format != "md" {
		t.Errorf("output.format = %q, want normalized \"md\"", cfg.Output.Format)
	}
}

// TestValidateAcceptsEmptyOutputFormat proves an explicitly empty output.format
// is a legal "unset" value (render defaults it to md), and an absent key keeps
// the built-in "md" default.
func TestValidateAcceptsEmptyOutputFormat(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "output:\n  format: \"\"\n")
	cfg, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v (empty output.format must be legal)", err)
	}
	if cfg.Output.Format != "" {
		t.Errorf("output.format = %q, want \"\" (explicitly empty)", cfg.Output.Format)
	}

	// Key entirely absent → built-in default survives validation.
	absentDir := t.TempDir()
	cfg, err = Load(Options{
		RepoDir:      absentDir,
		PersonalPath: filepath.Join(absentDir, "does-not-exist.yaml"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v (absent output.format must be legal)", err)
	}
	if cfg.Output.Format != "md" {
		t.Errorf("output.format = %q, want the \"md\" default", cfg.Output.Format)
	}
}

// TestValidateRejectsMalformedExcludeGlob proves a syntactically invalid glob
// pattern (an unterminated "[") fails config-load with a clear error, instead
// of silently matching nothing on every file forever (matchesAnyGlob in
// internal/cli swallows path.Match errors on every call — this is the single
// point that catches it).
func TestValidateRejectsMalformedExcludeGlob(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "diff:\n  exclude_globs: [\"[unterminated\"]\n")
	_, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
	if err == nil {
		t.Fatal("expected error for malformed diff.exclude_globs pattern")
	}
	if !strings.Contains(err.Error(), "diff.exclude_globs") {
		t.Errorf("error = %q, want it to name diff.exclude_globs", err.Error())
	}
}

// TestValidateAcceptsValidExcludeGlobs proves ordinary glob patterns
// (including "**"-suffixed ones, valid per path.Match even though gitl treats
// the ** specially — see matchesAnyGlob) don't trip the new validation.
func TestValidateAcceptsValidExcludeGlobs(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "diff:\n  exclude_globs: [\"*.lock\", \"vendor/**\"]\npolicy:\n  exclude_globs: [\"testdata/*\"]\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err != nil {
		t.Errorf("Load() error = %v, want valid patterns accepted", err)
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
// (offline mode), inaccessible system template files (review AND changelog)
// must not block Load so that deterministic offline runs remain available.
func TestValidateSkipsPromptTemplateInOfflineMode(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	missing := filepath.Join(dir, "no-such.tmpl")
	writeFile(t, personalPath, "prompt:\n  system_template_file: "+missing+"\n  changelog_system_template_file: "+missing+"\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err != nil {
		t.Errorf("offline mode must not validate prompt.*_template_file: %v", err)
	}
}

// TestValidatePromptTemplatesWithFuncs: system-prompt templates (review and
// changelog) using functions genuinely registered in render.TemplateFuncs()
// must pass validation at config load — before the fix, validation parsed
// prompt.system_template_file WITHOUT the FuncMap that BuildReviewWithTemplate/
// BuildChangelogWithTemplate register at execution time, so a template using
// {{ upper ... }} broke EVERY command at Load. A genuinely undefined function
// must still be rejected.
func TestValidatePromptTemplatesWithFuncs(t *testing.T) {
	tests := []struct {
		name    string
		key     string // yaml key under prompt:
		content string
		wantErr bool
	}{
		{"review template with upper is accepted", "system_template_file", "Review {{ upper .Range }}", false},
		{"review template with trimTrailingNewlines is accepted", "system_template_file", "{{ trimTrailingNewlines .Diff }}", false},
		{"review template with undefined func is rejected", "system_template_file", "{{ bogusFunc .Range }}", true},
		{"changelog template with upper is accepted", "changelog_system_template_file", "Changelog {{ upper .Range }}", false},
		{"changelog template with undefined func is rejected", "changelog_system_template_file", "{{ bogusFunc .Range }}", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tmplPath := filepath.Join(dir, "sys.tmpl")
			writeFile(t, tmplPath, tt.content)
			personalPath := filepath.Join(dir, "config.yaml")
			// api_key set → online mode, so validate() actually checks the file.
			writeFile(t, personalPath, "llm:\n  api_key: test-key\nprompt:\n  "+tt.key+": "+tmplPath+"\n")
			_, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
			if tt.wantErr && err == nil {
				t.Errorf("expected error for prompt.%s = %q", tt.key, tt.content)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Load() error = %v, want template %q accepted", err, tt.content)
			}
		})
	}
}

// TestValidateChangelogTemplateIndependent: prompt.changelog_system_template_file
// is validated on its own — a broken path for one key fails Load regardless of
// whether the OTHER key is unset or perfectly valid, and the error names the
// key that is actually broken.
func TestValidateChangelogTemplateIndependent(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "ok.tmpl")
	writeFile(t, valid, "System prompt for {{.Range}}")
	missing := filepath.Join(dir, "no-such.tmpl")

	tests := []struct {
		name       string
		promptYAML string // lines under prompt:
		wantErrKey string // "" = Load must succeed
	}{
		{"bad changelog key alone fails", "  changelog_system_template_file: " + missing + "\n", "prompt.changelog_system_template_file"},
		{"bad changelog key fails despite valid review key", "  system_template_file: " + valid + "\n  changelog_system_template_file: " + missing + "\n", "prompt.changelog_system_template_file"},
		{"bad review key fails despite valid changelog key", "  system_template_file: " + missing + "\n  changelog_system_template_file: " + valid + "\n", "prompt.system_template_file"},
		{"both keys valid loads", "  system_template_file: " + valid + "\n  changelog_system_template_file: " + valid + "\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			personalPath := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, personalPath, "llm:\n  api_key: test-key\nprompt:\n"+tt.promptYAML)
			cfg, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
			if tt.wantErrKey == "" {
				if err != nil {
					t.Fatalf("Load() error = %v, want success", err)
				}
				if cfg.Prompt.ChangelogSystemTemplateFile != valid {
					t.Errorf("ChangelogSystemTemplateFile = %q, want %q", cfg.Prompt.ChangelogSystemTemplateFile, valid)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error naming %s", tt.wantErrKey)
			}
			if !strings.Contains(err.Error(), tt.wantErrKey) {
				t.Errorf("error = %q, want it to name %s", err.Error(), tt.wantErrKey)
			}
		})
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

// TestRepoRootConfigMergedBeneathCwdConfig is the core Batch-4 regression
// test: with a .gitl.yaml at BOTH the git repo root (RepoRootDir) and the
// cwd-based dir (RepoDir), both must merge — the cwd file winning key-by-key,
// while keys only the root file sets (the committed team policy, including
// the secret-exclusion globs) still apply.
func TestRepoRootConfigMergedBeneathCwdConfig(t *testing.T) {
	rootDir := t.TempDir()
	subDir := filepath.Join(rootDir, "src", "internal")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	personalPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	writeFile(t, filepath.Join(rootDir, ".gitl.yaml"),
		"policy:\n  fail_on: high\n  exclude_globs:\n    - \"secrets/**\"\nllm:\n  model: root-model\n")
	writeFile(t, filepath.Join(subDir, ".gitl.yaml"),
		"llm:\n  model: sub-model\n")

	cfg, err := Load(Options{RepoDir: subDir, RepoRootDir: rootDir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LLM.Model != "sub-model" {
		t.Errorf("llm.model = %q, want sub-model (cwd config wins on conflict)", cfg.LLM.Model)
	}
	if cfg.Policy.FailOn != "high" {
		t.Errorf("policy.fail_on = %q, want high (root-only key must survive the merge)", cfg.Policy.FailOn)
	}
	if !slices.Equal(cfg.Policy.ExcludeGlobs, []string{"secrets/**"}) {
		t.Errorf("policy.exclude_globs = %v, want [secrets/**] (root-only key)", cfg.Policy.ExcludeGlobs)
	}
}

// TestRepoRootWithoutRootConfig: RepoRootDir points at a git root with NO
// .gitl.yaml — the cwd config must apply exactly as in the single-dir case,
// with no error from the missing root file.
func TestRepoRootWithoutRootConfig(t *testing.T) {
	rootDir := t.TempDir()
	subDir := filepath.Join(rootDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	personalPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	writeFile(t, filepath.Join(subDir, ".gitl.yaml"), "policy:\n  fail_on: medium\n")

	cfg, err := Load(Options{RepoDir: subDir, RepoRootDir: rootDir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Policy.FailOn != "medium" {
		t.Errorf("policy.fail_on = %q, want medium (cwd config)", cfg.Policy.FailOn)
	}
}

// TestRepoRootNoConfigAnywhere: neither the root nor the cwd dir has a
// .gitl.yaml — Load must still succeed with pure defaults (the existing
// missing-file leniency must survive the second repo layer).
func TestRepoRootNoConfigAnywhere(t *testing.T) {
	cfg, err := Load(Options{
		RepoDir:      t.TempDir(),
		RepoRootDir:  t.TempDir(),
		PersonalPath: filepath.Join(t.TempDir(), "does-not-exist.yaml"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Policy.FailOn != "never" {
		t.Errorf("policy.fail_on = %q, want the never default", cfg.Policy.FailOn)
	}
}

// TestRepoRootSameAsRepoDir: when the discovered root IS the cwd dir (running
// gitl from the repo root — the common case), the single file must merge once
// and behave exactly as before RepoRootDir existed.
func TestRepoRootSameAsRepoDir(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	writeFile(t, filepath.Join(dir, ".gitl.yaml"), "policy:\n  fail_on: low\n")

	cfg, err := Load(Options{RepoDir: dir, RepoRootDir: dir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Policy.FailOn != "low" {
		t.Errorf("policy.fail_on = %q, want low", cfg.Policy.FailOn)
	}
}

// TestDigestReposResolvedAgainstDeclaringDir: with two repo-level configs,
// relative digest.repos paths must resolve against the directory of the file
// that actually declared the WINNING list (lists replace wholesale on merge),
// not blindly against the cwd — the resolveDigestRepoPaths §10.4 promise.
func TestDigestReposResolvedAgainstDeclaringDir(t *testing.T) {
	rootDir := t.TempDir()
	subDir := filepath.Join(rootDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	personalPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	// Declared ONLY at the root → resolved against rootDir, not subDir.
	writeFile(t, filepath.Join(rootDir, ".gitl.yaml"), "digest:\n  repos:\n    - path: \"../other-repo\"\n")
	writeFile(t, filepath.Join(subDir, ".gitl.yaml"), "policy:\n  fail_on: low\n")

	cfg, err := Load(Options{RepoDir: subDir, RepoRootDir: rootDir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Digest.Repos) != 1 {
		t.Fatalf("Digest.Repos = %+v, want exactly 1 entry", cfg.Digest.Repos)
	}
	if want := filepath.Join(rootDir, "../other-repo"); cfg.Digest.Repos[0].Path != want {
		t.Errorf("Digest.Repos[0].Path = %q, want %q (resolved against the declaring ROOT dir)", cfg.Digest.Repos[0].Path, want)
	}

	// Now the sub config declares its own list → it wins wholesale and
	// resolves against subDir.
	writeFile(t, filepath.Join(subDir, ".gitl.yaml"), "digest:\n  repos:\n    - path: \"../nested-repo\"\n")
	cfg, err = Load(Options{RepoDir: subDir, RepoRootDir: rootDir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Digest.Repos) != 1 {
		t.Fatalf("Digest.Repos = %+v, want exactly 1 entry (cwd list replaces wholesale)", cfg.Digest.Repos)
	}
	if want := filepath.Join(subDir, "../nested-repo"); cfg.Digest.Repos[0].Path != want {
		t.Errorf("Digest.Repos[0].Path = %q, want %q (resolved against the declaring CWD dir)", cfg.Digest.Repos[0].Path, want)
	}
}

// TestValidateRejectsZeroRemoteTimeout: an explicit cache.remote.timeout_ms of
// 0 combined with a configured remote URL must be a loud config error — a zero
// timeout means "no timeout" in net/http and could hang a review forever
// against an unresponsive remote cache server.
func TestValidateRejectsZeroRemoteTimeout(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "cache:\n  remote:\n    url: https://cache.example.com/gitl\n    timeout_ms: 0\n")
	_, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
	if err == nil {
		t.Fatal("expected error for cache.remote.timeout_ms = 0 with a remote URL set")
	}
	if !strings.Contains(err.Error(), "cache.remote.timeout_ms") {
		t.Errorf("error %q should mention cache.remote.timeout_ms", err)
	}
}

// TestValidateAllowsZeroRemoteTimeoutWithoutURL: when the remote tier is off
// (empty URL), timeout_ms is unused — a zero value must not fail validation.
func TestValidateAllowsZeroRemoteTimeoutWithoutURL(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "cache:\n  remote:\n    timeout_ms: 0\n")
	if _, err := Load(Options{RepoDir: dir, PersonalPath: personalPath}); err != nil {
		t.Errorf("Load() error = %v (timeout_ms 0 must be legal when the remote tier is off)", err)
	}
}

// TestValidateAcceptsDefaultRemoteTimeout: a remote URL with no explicit
// timeout must pass validation via the built-in 3000ms default.
func TestValidateAcceptsDefaultRemoteTimeout(t *testing.T) {
	dir := t.TempDir()
	personalPath := filepath.Join(dir, "config.yaml")
	writeFile(t, personalPath, "cache:\n  remote:\n    url: https://cache.example.com/gitl\n")
	cfg, err := Load(Options{RepoDir: dir, PersonalPath: personalPath})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Cache.Remote.TimeoutMS != 3000 {
		t.Errorf("cache.remote.timeout_ms = %d, want the 3000 default", cfg.Cache.Remote.TimeoutMS)
	}
}
