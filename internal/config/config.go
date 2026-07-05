// Package config loads and validates gitl configuration.
//
// Two config levels are merged by priority
// flag > env > .gitl.yaml (repo, cwd) > ~/.config/gitl/config.yaml (personal),
// via a per-call viper instance → struct Config. The personal path comes from
// os.UserConfigDir() (never a hardcoded ~/.config; on Windows → %AppData%\gitl).
// An empty api_key selects offline mode (a warning is printed to stderr by the
// caller — Load does not fail).
//
// The cost:/output:/policy:/diff: blocks and provider branching (openai /
// ollama / azure_openai) are all wired into behavior as of Этап 2 (см.
// docs/TECHNICAL_PLAN.md §5–§8). digest:/required_changelog_categories remain
// schema-only until Этап 3.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config is the fully merged, validated configuration for one gitl invocation.
type Config struct {
	LLM    LLMConfig    `mapstructure:"llm"`
	Cost   CostConfig   `mapstructure:"cost"`
	Output OutputConfig `mapstructure:"output"`
	Diff   DiffConfig   `mapstructure:"diff"`
	Policy PolicyConfig `mapstructure:"policy"`
}

// LLMConfig configures the LLM provider and request parameters.
type LLMConfig struct {
	Provider       string  `mapstructure:"provider"`
	APIKey         string  `mapstructure:"api_key"`
	BaseURL        string  `mapstructure:"base_url"`
	Model          string  `mapstructure:"model"`
	MaxTokens      int     `mapstructure:"max_tokens"`
	Temperature    float64 `mapstructure:"temperature"`
	TimeoutSeconds int     `mapstructure:"timeout_seconds"`
	MaxRetries     int     `mapstructure:"max_retries"`
	// AzureOpenAI is required only when Provider == "azure_openai".
	AzureOpenAI AzureOpenAIConfig `mapstructure:"azure_openai"`
}

// AzureOpenAIConfig holds the Azure OpenAI endpoint coordinates. The request
// URL is {endpoint}/openai/deployments/{deployment}/chat/completions?api-version={api_version},
// with auth via the "api-key" header (not "Authorization: Bearer").
type AzureOpenAIConfig struct {
	Endpoint   string `mapstructure:"endpoint"`
	Deployment string `mapstructure:"deployment"`
	APIVersion string `mapstructure:"api_version"`
}

// Timeout returns the per-request LLM timeout as a time.Duration.
func (c LLMConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// CostConfig holds cost-guard thresholds and optional pricing overrides. When
// PricePer1MInput/Output are 0 the built-in pricing table (internal/llm) is used
// (§8.2). max_cost_usd <= 0 disables the guard entirely (§8.4).
type CostConfig struct {
	MaxCostUSD       float64 `mapstructure:"max_cost_usd"`
	WarnAtUSD        float64 `mapstructure:"warn_at_usd"`
	PricePer1MInput  float64 `mapstructure:"price_per_1m_input"`
	PricePer1MOutput float64 `mapstructure:"price_per_1m_output"`
}

// OutputConfig holds output settings. All three formats (md/text/json) are
// rendered as of Этап 2.
type OutputConfig struct {
	Format string `mapstructure:"format"`
	Color  bool   `mapstructure:"color"`
}

// DiffConfig bounds the diff sent to the LLM: max_diff_bytes is the truncation
// limit, exclude_globs skip matching changed files before building the diff.
type DiffConfig struct {
	MaxDiffBytes int      `mapstructure:"max_diff_bytes"`
	ExcludeGlobs []string `mapstructure:"exclude_globs"`
}

// PolicyConfig is the repo-level governance policy. fail_on is the CI gating
// threshold wired into `review` as of Этап 2; required_changelog_categories is
// used by changelog in Этап 3.
type PolicyConfig struct {
	FailOn                      string   `mapstructure:"fail_on"`
	RequiredChangelogCategories []string `mapstructure:"required_changelog_categories"`
	ExcludeGlobs                []string `mapstructure:"exclude_globs"`
}

// Options controls how Load discovers config files.
type Options struct {
	// RepoDir is the directory searched for the repo-level ".gitl.yaml".
	// Empty means the current working directory.
	RepoDir string
	// PersonalPath overrides the personal config file path. Empty means
	// "<os.UserConfigDir()>/gitl/config.yaml".
	PersonalPath string
	// Flags, when non-nil, are bound so that explicitly-set flags win over
	// env and files. Only flags present in the set are considered.
	Flags *pflag.FlagSet
}

// defaults returns the built-in configuration, used as the lowest-priority
// layer beneath the personal file, repo file, env, and flags.
func defaults() map[string]any {
	return map[string]any{
		"llm.provider":             "openai",
		"llm.api_key":              "",
		"llm.base_url":             "https://api.openai.com/v1",
		"llm.model":                "gpt-4o-mini",
		"llm.max_tokens":           1500,
		"llm.temperature":          0.2,
		"llm.timeout_seconds":      60,
		"llm.max_retries":          3,
		"cost.max_cost_usd":        0.50,
		"cost.warn_at_usd":         0.10,
		"cost.price_per_1m_input":  0.0,
		"cost.price_per_1m_output": 0.0,
		"output.format":            "md",
		"output.color":             true,
		"diff.max_diff_bytes":      120000,
		"diff.exclude_globs":       []string{"*.lock", "*.min.js", "vendor/**", "*.svg"},
		"policy.fail_on":           "never",
	}
}

// PersonalConfigPath returns the default personal config file path, derived
// from os.UserConfigDir() so it is correct across platforms.
func PersonalConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "gitl", "config.yaml"), nil
}

// Load builds the merged configuration for one invocation.
//
// Priority (lowest → highest): built-in defaults → personal config file →
// repo-level .gitl.yaml → environment (GITL_* / GITL_API_KEY) → flags. A
// fresh viper instance is used per call so tests never share global state.
//
// Missing config files are not an error — the file layers are simply skipped.
func Load(opts Options) (*Config, error) {
	v := viper.NewWithOptions(viper.KeyDelimiter("."))
	v.SetConfigType("yaml")

	for key, val := range defaults() {
		v.SetDefault(key, val)
	}

	// Environment: GITL_LLM_PROVIDER, GITL_DIFF_MAX_DIFF_BYTES, etc.
	v.SetEnvPrefix("GITL")
	v.AutomaticEnv()
	// Map dotted config keys to GITL_* env var names:
	// "llm.max_tokens" → "GITL_LLM_MAX_TOKENS".
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	// Documented special case: the API key env var is GITL_API_KEY, not
	// GITL_LLM_API_KEY. Bind it explicitly so the short, documented name wins.
	if err := v.BindEnv("llm.api_key", "GITL_API_KEY"); err != nil {
		return nil, fmt.Errorf("bind GITL_API_KEY: %w", err)
	}

	// Personal config (lower priority than repo config).
	personalPath := opts.PersonalPath
	if personalPath == "" {
		p, err := PersonalConfigPath()
		if err != nil {
			return nil, err
		}
		personalPath = p
	}
	if err := mergeFile(v, personalPath); err != nil {
		return nil, err
	}

	// Repo-level .gitl.yaml (higher priority than personal — merged last).
	repoDir := opts.RepoDir
	if repoDir == "" {
		repoDir = "."
	}
	if err := mergeFile(v, filepath.Join(repoDir, ".gitl.yaml")); err != nil {
		return nil, err
	}

	// Flags win over everything, but only when explicitly set by the user.
	if opts.Flags != nil {
		if err := bindChangedFlags(v, opts.Flags); err != nil {
			return nil, err
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// mergeFile merges a YAML file into v if it exists. A missing file is not an
// error; other read/parse errors are.
func mergeFile(v *viper.Viper, path string) error {
	f, err := os.Open(path) //nolint:gosec // path is derived from config discovery, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open config %q: %w", path, err)
	}
	defer f.Close()

	if err := v.MergeConfig(f); err != nil {
		return fmt.Errorf("parse config %q: %w", path, err)
	}
	return nil
}

// bindChangedFlags maps known flag names onto config keys, but only for flags
// the user actually set, so unset flag defaults never clobber file/env values.
func bindChangedFlags(v *viper.Viper, flags *pflag.FlagSet) error {
	// flagToKey maps a pflag name to its dotted config key.
	flagToKey := map[string]string{
		"provider":     "llm.provider",
		"model":        "llm.model",
		"base-url":     "llm.base_url",
		"format":       "output.format",
		"fail-on":      "policy.fail_on",
		"max-cost-usd": "cost.max_cost_usd",
	}
	var bindErr error
	flags.Visit(func(f *pflag.Flag) {
		key, ok := flagToKey[f.Name]
		if !ok {
			return
		}
		if err := v.BindPFlag(key, f); err != nil && bindErr == nil {
			bindErr = fmt.Errorf("bind flag %q: %w", f.Name, err)
		}
	})
	return bindErr
}

// validFailOnLevels are the only accepted policy.fail_on / --fail-on values.
// A typo here must be a loud config error, not a silent misfire: an unknown
// string falls through llm.RiskAtLeast's rank lookup as 0, which is BELOW
// every real risk level, making "l >= t" true for any risk — i.e. an
// unrecognized threshold would otherwise fail-gate on every review, the exact
// opposite of the project's "default WARN, hard gate is explicit opt-in"
// principle (§9).
var validFailOnLevels = map[string]bool{"never": true, "low": true, "medium": true, "high": true}

// validate checks invariants that must hold before the config is used. It also
// normalizes policy.fail_on to lowercase so downstream comparisons don't need
// to re-normalize.
func (c *Config) validate() error {
	if c.LLM.TimeoutSeconds <= 0 {
		return fmt.Errorf("llm.timeout_seconds must be > 0, got %d", c.LLM.TimeoutSeconds)
	}
	if c.LLM.MaxTokens <= 0 {
		return fmt.Errorf("llm.max_tokens must be > 0, got %d", c.LLM.MaxTokens)
	}
	failOn := strings.ToLower(strings.TrimSpace(c.Policy.FailOn))
	if failOn == "" {
		failOn = "never"
	}
	if !validFailOnLevels[failOn] {
		return fmt.Errorf("policy.fail_on must be one of never|low|medium|high, got %q", c.Policy.FailOn)
	}
	c.Policy.FailOn = failOn
	return nil
}

// OfflineMode reports whether gitl should use the deterministic offline
// provider instead of the network client (i.e. no API key is configured).
func (c *Config) OfflineMode() bool {
	return c.LLM.APIKey == ""
}
