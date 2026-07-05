package llm

import (
	"fmt"
	"math"
)

// Cost estimation is a deliberately rough, offline, deterministic approximation
// (§8): no real tokenizer (tiktoken), no pricing API call — in the spirit of
// "$0/month infrastructure" and offline compatibility. The safety margin biases
// estimates upward so the cost-guard errs toward blocking, never toward a
// surprise bill.

// tokenSafetyMargin inflates the raw char/4 token estimate by 15% (§8.1). The
// margin is applied once, to the total estimate.
const tokenSafetyMargin = 1.15

// charsPerToken is the common "~4 chars per token" approximation (§8.1).
const charsPerToken = 4

// EstimateTokens approximates the token count of text as ceil(len(bytes)/4).
// It does NOT apply the safety margin — that is applied once by EstimateCost to
// the combined total, to avoid double-counting.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return int(math.Ceil(float64(len(text)) / charsPerToken))
}

// Pricing is the per-1M-token price of a model, in USD.
type Pricing struct {
	InputPer1M  float64
	OutputPer1M float64
}

// pricingTable is the built-in price table (§8.2), USD per 1M tokens, current as
// of 2026-07. Staleness is expected and not a bug; override via config for
// models not listed or changed prices.
var pricingTable = map[string]Pricing{
	"gpt-4o-mini":  {InputPer1M: 0.15, OutputPer1M: 0.60},
	"gpt-4o":       {InputPer1M: 2.50, OutputPer1M: 10.00},
	"gpt-4.1-mini": {InputPer1M: 0.40, OutputPer1M: 1.60},
}

// LookupPricing returns the built-in pricing for a model and whether it was
// found. Callers use the returned bool to decide between the table, a config
// override, or the permissive "no pricing data" path.
func LookupPricing(model string) (Pricing, bool) {
	p, ok := pricingTable[model]
	return p, ok
}

// Estimate is the result of a cost estimation for one review request.
type Estimate struct {
	Model        string
	InputTokens  int
	OutputTokens int // the configured max_tokens ceiling
	Pricing      Pricing
	CostUSD      float64
}

// EstimateCost estimates the USD cost of a request whose full prompt (system +
// user, exactly what goes on the wire) is promptText, capping output at
// maxOutputTokens (the configured llm.max_tokens). pricing is the price table
// entry (from LookupPricing or a config override). The ×1.15 safety margin is
// applied once, to the combined input+output token totals (§8.1).
func EstimateCost(promptText string, maxOutputTokens int, pricing Pricing) Estimate {
	inputTokens := int(math.Ceil(float64(EstimateTokens(promptText)) * tokenSafetyMargin))
	outputTokens := int(math.Ceil(float64(maxOutputTokens) * tokenSafetyMargin))

	cost := float64(inputTokens)/1_000_000*pricing.InputPer1M +
		float64(outputTokens)/1_000_000*pricing.OutputPer1M

	return Estimate{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Pricing:      pricing,
		CostUSD:      cost,
	}
}

// String renders a human-readable one-line summary of an estimate.
func (e Estimate) String() string {
	return fmt.Sprintf("≈$%.4f (%s input + %s output tokens @ %s)",
		e.CostUSD, humanInt(e.InputTokens), humanInt(e.OutputTokens), e.Model)
}

// humanInt formats an integer with thousands separators (e.g. 14231 → 14,231).
func humanInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	// Insert commas every three digits from the right.
	var out []byte
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
