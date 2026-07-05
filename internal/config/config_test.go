package config

import (
	"os"
	"path/filepath"
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
