package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/llm"
)

func TestResolvePricingOneSidedOverrideWarnsAndFallsThrough(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Provider = "openai"
	cfg.LLM.Model = "not-a-real-model"
	cfg.Cost.PricePer1MInput = 5.0
	// PricePer1MOutput left at zero — a one-sided override.

	var errOut bytes.Buffer
	pricing, ok := resolvePricing(&errOut, cfg)

	if ok {
		t.Fatalf("resolvePricing ok = true, want false (unknown model, override ignored)")
	}
	if pricing != (llm.Pricing{}) {
		t.Fatalf("pricing = %+v, want zero value", pricing)
	}
	msg := errOut.String()
	if !strings.Contains(msg, "must both be set") {
		t.Errorf("errOut = %q, want a message about the one-sided override being ignored", msg)
	}
	if !strings.Contains(msg, "no pricing data") {
		t.Errorf("errOut = %q, want it to still fall through to the no-pricing-data message", msg)
	}
}

func TestResolvePricingBothSidesOverrideWins(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Provider = "openai"
	cfg.LLM.Model = "not-a-real-model"
	cfg.Cost.PricePer1MInput = 5.0
	cfg.Cost.PricePer1MOutput = 10.0

	var errOut bytes.Buffer
	pricing, ok := resolvePricing(&errOut, cfg)

	if !ok {
		t.Fatalf("resolvePricing ok = false, want true (full override set)")
	}
	if pricing.InputPer1M != 5.0 || pricing.OutputPer1M != 10.0 {
		t.Fatalf("pricing = %+v, want {5.0 10.0}", pricing)
	}
	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want no warning when both sides are set", errOut.String())
	}
}
