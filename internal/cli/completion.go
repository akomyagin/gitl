package cli

import (
	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/render"
)

// fixedCompletion returns a cobra completion function that offers a fixed set
// of values (no file completion) — used for enum-valued flags like --format.
func fixedCompletion(values []string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

// registerEnumCompletions wires value-set completion for whichever of the
// shared enum flags (--format, --fail-on, --provider) the command actually
// declares. Safe to call for a command missing some of them: it only registers
// the flags present. Panics only on a genuine wiring bug (flag renamed).
func registerEnumCompletions(cmd *cobra.Command) {
	if cmd.Flags().Lookup("format") != nil {
		must(cmd.RegisterFlagCompletionFunc("format", fixedCompletion(render.FormatNames)))
	}
	if cmd.Flags().Lookup("fail-on") != nil {
		must(cmd.RegisterFlagCompletionFunc("fail-on", fixedCompletion(llm.FailOnValues)))
	}
	if cmd.Flags().Lookup("provider") != nil {
		must(cmd.RegisterFlagCompletionFunc("provider", fixedCompletion(llm.ProviderNames)))
	}
}

// must panics on err — used only for RegisterFlagCompletionFunc, whose sole
// failure mode (unknown flag name) is a programming bug caught by the first
// test run, not a runtime condition.
func must(err error) {
	if err != nil {
		panic(err)
	}
}
