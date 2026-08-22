package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/render"
)

// TestEnumFlagCompletions asserts that every enum-valued flag on every command
// that declares it completes to exactly its canonical value set, with file
// completion disabled. Exact-value comparison is deliberate: a value added to
// (or removed from) a canonical slice without the matching wiring shows up
// here immediately.
func TestEnumFlagCompletions(t *testing.T) {
	gf := &globalFlags{}
	cases := []struct {
		name string
		cmd  *cobra.Command
		flag string
		want []string
	}{
		{"review/format", newReviewCmd(gf), "format", render.FormatNames},
		{"review/fail-on", newReviewCmd(gf), "fail-on", llm.FailOnValues},
		{"review/provider", newReviewCmd(gf), "provider", llm.ProviderNames},
		{"changelog/format", newChangelogCmd(gf), "format", render.FormatNames},
		{"changelog/provider", newChangelogCmd(gf), "provider", llm.ProviderNames},
		{"digest/format", newDigestCmd(gf), "format", render.FormatNames},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn, ok := tc.cmd.GetFlagCompletionFunc(tc.flag)
			if !ok || fn == nil {
				t.Fatalf("no completion func registered for --%s", tc.flag)
			}
			got, directive := fn(tc.cmd, nil, "")
			if !slices.Equal(got, tc.want) {
				t.Fatalf("--%s completions = %v, want %v", tc.flag, got, tc.want)
			}
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Fatalf("--%s directive = %v, want NoFileComp", tc.flag, directive)
			}
		})
	}
}

// TestEnumFlagCompletionValues pins the canonical slices to their documented
// literal values, so help text, docs and completions cannot silently diverge
// from what the flags actually accept.
func TestEnumFlagCompletionValues(t *testing.T) {
	if want := []string{"md", "text", "json"}; !slices.Equal(render.FormatNames, want) {
		t.Errorf("render.FormatNames = %v, want %v", render.FormatNames, want)
	}
	if want := []string{"never", "low", "medium", "high"}; !slices.Equal(llm.FailOnValues, want) {
		t.Errorf("llm.FailOnValues = %v, want %v", llm.FailOnValues, want)
	}
	if want := []string{"openai", "ollama", "azure_openai", "anthropic", "gemini"}; !slices.Equal(llm.ProviderNames, want) {
		t.Errorf("llm.ProviderNames = %v, want %v", llm.ProviderNames, want)
	}
}

// TestProvidersHelpMatchesNames guards ProvidersHelp (a const, used in flag
// help text) against drifting from ProviderNames (the completion source).
func TestProvidersHelpMatchesNames(t *testing.T) {
	want := strings.Join(llm.ProviderNames, " | ")
	if llm.ProvidersHelp != want {
		t.Fatalf("llm.ProvidersHelp = %q, want join(ProviderNames) = %q", llm.ProvidersHelp, want)
	}
}

// TestFailOnValuesAllValid guards FailOnValues against drifting from the
// riskOrder-backed validator: every advertised completion must actually be
// accepted by --fail-on validation.
func TestFailOnValuesAllValid(t *testing.T) {
	for _, v := range llm.FailOnValues {
		if !llm.ValidFailOnLevel(v) {
			t.Errorf("FailOnValues contains %q, which ValidFailOnLevel rejects", v)
		}
	}
}
