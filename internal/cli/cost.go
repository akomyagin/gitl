package cli

import (
	"fmt"
	"io"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/llm"
)

// resolvePricing determines the pricing for the configured model (§8.2):
// config override wins unconditionally; otherwise the built-in table; ollama is
// always free ($0). ok is false (and a warning is printed to errOut) when the
// model is not in the table and no override is set — the caller then skips the
// guard permissively. errOut is the CLI's stderr; cmd-free callers pass their
// own sink.
func resolvePricing(errOut io.Writer, cfg *config.Config) (pricing llm.Pricing, ok bool) {
	// Ollama is self-hosted: always free, guard/estimate skipped without a
	// warning (the expected free path).
	if cfg.LLM.Provider == llm.ProviderOllama {
		return llm.Pricing{}, true
	}

	// Explicit config override wins only when both sides are set; a one-sided
	// override would zero the other half of the estimate, silently under-reporting.
	if cfg.Cost.PricePer1MInput > 0 && cfg.Cost.PricePer1MOutput > 0 {
		return llm.Pricing{
			InputPer1M:  cfg.Cost.PricePer1MInput,
			OutputPer1M: cfg.Cost.PricePer1MOutput,
		}, true
	}

	if p, found := llm.LookupPricing(cfg.LLM.Model); found {
		return p, true
	}

	fmt.Fprintf(errOut,
		"gitl: no pricing data for model %q — cost estimate unavailable, proceeding without cost-guard (set cost.price_per_1m_input/output to enable it)\n",
		cfg.LLM.Model)
	return llm.Pricing{}, false
}

// estimateFor builds a cost estimate for the given full prompt text.
func estimateFor(cfg *config.Config, promptText string, pricing llm.Pricing) llm.Estimate {
	est := llm.EstimateCost(promptText, cfg.LLM.MaxTokens, pricing)
	est.Model = cfg.LLM.Model
	return est
}

// printDryRun prints a cost estimate to out (the CLI's stdout) and returns nil
// (exit 0), making no network call (§8.3). Warnings go to errOut.
func printDryRun(out, errOut io.Writer, cfg *config.Config, promptText string) error {
	if cfg.OfflineMode() {
		fmt.Fprintln(out, "offline mode — no API call, no cost")
		return nil
	}

	if cfg.LLM.Provider == llm.ProviderOllama {
		fmt.Fprintf(out, "provider ollama (%s) — self-hosted, no API cost\n", cfg.LLM.Model)
		return nil
	}

	pricing, ok := resolvePricing(errOut, cfg)
	if !ok {
		fmt.Fprintf(out, "estimate unavailable: no pricing data for model %q (estimate, not exact)\n", cfg.LLM.Model)
		return nil
	}

	est := estimateFor(cfg, promptText, pricing)
	fmt.Fprintf(out, "cost estimate (estimate, not exact):\n")
	fmt.Fprintf(out, "  provider:      %s\n", cfg.LLM.Provider)
	fmt.Fprintf(out, "  model:         %s\n", cfg.LLM.Model)
	fmt.Fprintf(out, "  input tokens:  ~%d\n", est.InputTokens)
	fmt.Fprintf(out, "  output tokens: ~%d (ceiling = llm.max_tokens)\n", est.OutputTokens)
	fmt.Fprintf(out, "  estimated cost: $%.4f\n", est.CostUSD)
	return nil
}

// costGuard estimates the cost of the request and enforces cost.max_cost_usd
// (§8.4). It runs before the provider call. A non-positive max_cost_usd disables
// the guard; between warn_at_usd and max_cost_usd it only warns (to errOut).
// Returns a non-nil error (non-zero exit) when the estimate exceeds the limit.
func costGuard(errOut io.Writer, cfg *config.Config, promptText string) error {
	// Ollama / unknown-pricing paths are free/permissive.
	pricing, ok := resolvePricing(errOut, cfg)
	if !ok || cfg.LLM.Provider == llm.ProviderOllama {
		return nil
	}

	maxCost := cfg.Cost.MaxCostUSD
	if maxCost <= 0 {
		// Guard explicitly disabled ("no limit").
		return nil
	}

	est := estimateFor(cfg, promptText, pricing)

	if est.CostUSD > maxCost {
		return fmt.Errorf(
			"estimated cost %s exceeds --max-cost-usd=%g — increase the limit, use --dry-run to inspect, or omit the API key for free offline output",
			est.String(), maxCost)
	}

	if cfg.Cost.WarnAtUSD > 0 && est.CostUSD > cfg.Cost.WarnAtUSD {
		fmt.Fprintf(errOut,
			"gitl: estimated cost %s approaches the limit --max-cost-usd=%g (warn_at=%g) — proceeding\n",
			est.String(), maxCost, cfg.Cost.WarnAtUSD)
	}
	return nil
}
