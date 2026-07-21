package cli

// The `gitl mcp` command and the MCP tool registrations (gitl_review,
// gitl_digest).
//
// Architecture: this file lives in internal/cli — NOT in internal/mcpserver —
// because the dependency points from the CLI layer to the transport layer, not
// the other way around. internal/mcpserver stays a pure JSON-RPC/stdio
// transport with zero knowledge of gitl's business logic; the cli package uses
// it as a library and binds its tools to the same cmd-free cores
// (RunReviewCore / RunDigestCore) the cobra commands are built on. This also
// keeps the unexported diffSource machinery unexported.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/mcpserver"
	"github.com/akomyagin/gitl/internal/render"
)

// newMCPCmd builds the `gitl mcp` command: an MCP stdio server exposing the
// gitl_review and gitl_digest tools to an MCP host (e.g. Claude Code).
//
// stdout is the MCP protocol channel — nothing human-readable is ever written
// there. Warnings (offline-mode notice, cost warnings) and logs go to stderr.
func newMCPCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run gitl as an MCP stdio server (tools: gitl_review, gitl_digest)",
		Long: "mcp serves the Model Context Protocol over stdio, exposing two tools:\n\n" +
			"  gitl_review — AI review of a commit range, a GitHub PR, or staged changes\n" +
			"  gitl_digest — deterministic activity summary over the last N days\n\n" +
			"Tool results are always the JSON artifact (the same schema as --format=json).\n" +
			"Config is loaded once at startup from the current directory (.gitl.yaml +\n" +
			"personal config + GITL_* env), exactly like the plain commands. stdout is\n" +
			"reserved for the protocol; warnings go to stderr.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd, gf)
			if err != nil {
				return err
			}
			// Same name/version pair `gitl version` prints (ldflags-injected).
			srv := mcpserver.New("gitl", version)
			registerMCPTools(srv, cfg, cmd.ErrOrStderr())
			return srv.Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

// reviewToolSchema is the JSON Schema advertised for gitl_review. Kept in sync
// with reviewToolArgs by TestMCPToolSchemasMatchArgs.
const reviewToolSchema = `{
  "type": "object",
  "properties": {
    "range": {"type": "string", "description": "Git revision range to review, e.g. HEAD~5..HEAD"},
    "pr": {"type": "integer", "minimum": 1, "description": "GitHub pull request number to review (requires the gh CLI, installed and authenticated)"},
    "staged": {"type": "boolean", "description": "Review the staged (indexed, not yet committed) changes"},
    "provider": {"type": "string", "description": "Override the configured LLM provider for this call (openai | ollama | azure_openai | anthropic | gemini)"},
    "model": {"type": "string", "description": "Override the configured model name for this call"},
    "base_url": {"type": "string", "description": "Override the configured LLM API base URL for this call"}
  },
  "additionalProperties": false
}`

// digestToolSchema is the JSON Schema advertised for gitl_digest. "default"
// is sprintf'd from defaultDigestDays (not hand-copied) so the schema can't
// silently drift from the actual fallback in mcpDigestHandler.
var digestToolSchema = fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "days": {"type": "integer", "minimum": 1, "default": %d, "description": "Size of the activity window in days"},
    "repos": {"type": "array", "items": {"type": "string"}, "description": "Repository paths to digest. When omitted, the server's working directory (plus digest.repos from .gitl.yaml, if configured) is used."}
  },
  "additionalProperties": false
}`, defaultDigestDays)

// registerMCPTools binds the gitl toolset to srv. cfg is the merged config
// loaded once at server startup; errOut receives user-facing warnings (the
// offline-mode notice, cost-guard warnings) — the server's stderr, never
// stdout.
func registerMCPTools(srv *mcpserver.Server, cfg *config.Config, errOut io.Writer) {
	srv.RegisterTool(mcpserver.Tool{
		Name: "gitl_review",
		Description: "AI review of git changes with a structured risk score (low|medium|high). " +
			"Reviews exactly one of: a revision range (`range`), a GitHub pull request (`pr`), " +
			"or the staged changes (`staged`). Returns the review artifact as JSON, including " +
			"the review body in the `review_markdown` field. Without an API key configured it " +
			"falls back to a deterministic offline review (`offline: true` in the result).",
		InputSchema: json.RawMessage(reviewToolSchema),
	}, mcpReviewHandler(cfg, errOut))

	srv.RegisterTool(mcpserver.Tool{
		Name: "gitl_digest",
		Description: "Deterministic git activity summary over the last N days (default 7): commits, " +
			"lines added/removed, by-author and by-topic breakdowns, most-changed files. Never " +
			"calls an LLM. Returns the digest artifact as JSON. Without `repos` it digests the " +
			"server's working directory (or digest.repos from .gitl.yaml).",
		InputSchema: json.RawMessage(digestToolSchema),
	}, mcpDigestHandler(cfg))
}

// reviewToolArgs is the parsed arguments object of a gitl_review call. PR is a
// pointer so an explicit `"pr": 0` is distinguishable from an absent field and
// gets the "invalid PR number" error instead of silently selecting no mode.
type reviewToolArgs struct {
	Range    string `json:"range"`
	PR       *int   `json:"pr"`
	Staged   bool   `json:"staged"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
}

// mcpReviewHandler returns the gitl_review tool handler.
//
// Differences from the CLI path, by design:
//   - The result is always the JSON artifact (render.FormatJSON) — no md/text,
//     no custom output templates.
//   - Streaming is structurally impossible: the handler goes through
//     RunReviewCore, which is always buffered — in MCP mode stdout is the
//     protocol channel, so cfg.Output.Stream/TTY detection never apply.
//   - policy.fail_on does not gate anything (there is no process exit code to
//     fail); the model reads the risk level from the JSON result instead.
//   - The cost guard (cost.max_cost_usd) IS enforced, inside RunReviewCore:
//     an agent session must not trigger an accidentally expensive call.
func mcpReviewHandler(cfg *config.Config, errOut io.Writer) mcpserver.ToolHandler {
	return func(ctx context.Context, raw json.RawMessage) (*mcpserver.ToolsCallResult, error) {
		var a reviewToolArgs
		if err := decodeToolArgs(raw, &a); err != nil {
			return nil, err
		}

		// range and pr both map onto the single range-or-pr/N selector of
		// resolveReviewSource, so their conflict is checked here; every other
		// mode combination (staged+range, staged+pr, none at all) is rejected
		// by the same resolveReviewSource validation the CLI uses.
		if a.Range != "" && a.PR != nil {
			return nil, fmt.Errorf(`"range" and "pr" are mutually exclusive — provide exactly one of "range", "pr", or "staged"`)
		}
		arg := a.Range
		if a.PR != nil {
			arg = fmt.Sprintf("pr/%d", *a.PR)
		}

		// Per-call provider/model/base_url overrides operate on a value copy of
		// the startup config — the same llm.* keys the --provider/--model/
		// --base-url flags bind to — so per-call overrides never leak into
		// later calls and the shared cfg stays untouched. Note: as with the CLI
		// flags, overriding provider alone keeps the configured base_url;
		// override base_url too when the provider needs a different endpoint.
		c := *cfg
		if a.Provider != "" {
			c.LLM.Provider = a.Provider
		}
		if a.Model != "" {
			c.LLM.Model = a.Model
		}
		if a.BaseURL != "" {
			c.LLM.BaseURL = a.BaseURL
		}

		src, err := resolveReviewSource(ctx, arg, a.Staged)
		if err != nil {
			return nil, err
		}
		art, err := RunReviewCore(ctx, &c, src, ReviewOptions{ErrOut: errOut})
		if err != nil {
			return nil, err
		}

		var buf bytes.Buffer
		if err := render.Render(&buf, art, render.FormatJSON); err != nil {
			return nil, err
		}
		return &mcpserver.ToolsCallResult{Content: mcpserver.TextContent(buf.String())}, nil
	}
}

// digestToolArgs is the parsed arguments object of a gitl_digest call. Days is
// a pointer so an absent field gets the default while an explicit `"days": 0`
// is rejected by the core's positive-days validation.
type digestToolArgs struct {
	Days  *int     `json:"days"`
	Repos []string `json:"repos"`
}

// mcpDigestHandler returns the gitl_digest tool handler.
//
// Path scope (a deliberate product decision): when the client omits `repos`,
// the digest covers only the directory `gitl mcp` was launched in plus
// digest.repos from that directory's .gitl.yaml — i.e. what the user set up,
// never a surprise walk of arbitrary paths. When the client passes `repos`
// explicitly, it is honored as-is: an agent already has filesystem access
// through its own tools, so this is not access control — it just ensures a
// digest of arbitrary paths only happens on an explicit request.
func mcpDigestHandler(cfg *config.Config) mcpserver.ToolHandler {
	return func(ctx context.Context, raw json.RawMessage) (*mcpserver.ToolsCallResult, error) {
		var a digestToolArgs
		if err := decodeToolArgs(raw, &a); err != nil {
			return nil, err
		}

		days := defaultDigestDays
		if a.Days != nil {
			days = *a.Days
		}

		art, err := RunDigestCore(ctx, cfg, DigestOptions{Days: days, Repos: a.Repos})
		if err != nil {
			return nil, err
		}

		var buf bytes.Buffer
		if err := render.RenderDigest(&buf, art, render.FormatJSON); err != nil {
			return nil, err
		}
		return &mcpserver.ToolsCallResult{Content: mcpserver.TextContent(buf.String())}, nil
	}
}

// decodeToolArgs strictly parses a tools/call arguments object into v. Unknown
// fields are rejected (the schemas advertise additionalProperties:false, so a
// typo'd argument name must fail loudly, not be silently ignored). A nil/empty
// arguments object leaves v at its zero value.
func decodeToolArgs(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}
