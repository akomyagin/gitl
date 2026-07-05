package llm

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},      // 4 bytes / 4 = 1
		{"abcde", 2},     // ceil(5/4) = 2
		{"abcdefgh", 2},  // 8 / 4 = 2
		{"abcdefghi", 3}, // ceil(9/4) = 3
	}
	for _, tc := range tests {
		if got := EstimateTokens(tc.in); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLookupPricing(t *testing.T) {
	t.Parallel()
	if p, ok := LookupPricing("gpt-4o-mini"); !ok || p.InputPer1M != 0.15 || p.OutputPer1M != 0.60 {
		t.Errorf("gpt-4o-mini pricing = %+v, ok=%v", p, ok)
	}
	if _, ok := LookupPricing("no-such-model"); ok {
		t.Error("expected LookupPricing miss for unknown model")
	}
}

func TestEstimateCostAppliesSafetyMargin(t *testing.T) {
	t.Parallel()
	// 400 bytes → 100 raw tokens → ceil(100*1.15) = 115 input tokens.
	prompt := strings.Repeat("x", 400)
	pricing := Pricing{InputPer1M: 1.0, OutputPer1M: 2.0}
	est := EstimateCost(prompt, 1000, pricing)

	if est.InputTokens != 115 {
		t.Errorf("input tokens = %d, want 115", est.InputTokens)
	}
	// 1000 * 1.15 = 1150 output tokens.
	if est.OutputTokens != 1150 {
		t.Errorf("output tokens = %d, want 1150", est.OutputTokens)
	}
	// cost = 115/1e6*1.0 + 1150/1e6*2.0
	want := 115.0/1e6*1.0 + 1150.0/1e6*2.0
	if est.CostUSD != want {
		t.Errorf("cost = %v, want %v", est.CostUSD, want)
	}
}

// TestCostGuardBlocksLargeDiff reproduces the Stage 2 acceptance criterion at
// the estimate level: a large synthetic diff estimated at gpt-4o-mini pricing
// must exceed a $0.01 limit.
func TestCostGuardBlocksLargeDiff(t *testing.T) {
	t.Parallel()
	// ~2 MB of prompt text → hundreds of thousands of tokens → well over $0.01.
	largePrompt := strings.Repeat("some diff content line\n", 100_000)
	pricing, ok := LookupPricing("gpt-4o-mini")
	if !ok {
		t.Fatal("gpt-4o-mini must be in the pricing table")
	}
	est := EstimateCost(largePrompt, 1500, pricing)
	est.Model = "gpt-4o-mini"

	const limit = 0.01
	if est.CostUSD <= limit {
		t.Errorf("expected estimate > $%.2f for a large diff, got $%.4f", limit, est.CostUSD)
	}
	if !strings.Contains(est.String(), "gpt-4o-mini") {
		t.Errorf("estimate string should name the model: %q", est.String())
	}
}
