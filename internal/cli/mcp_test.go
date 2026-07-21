package cli

// Unit tests for the MCP tool handlers (gitl_review / gitl_digest) — the
// handlers are exercised directly, without the JSON-RPC transport (the full
// protocol path is covered by mcpserver tests and a future e2e). Offline mode
// (empty API key via coreTestConfig) keeps everything deterministic and
// network-free.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/mcpserver"
)

// chdir switches the working directory for the test and restores it on cleanup.
// The MCP handlers resolve git sources and the "." digest scope against the
// process cwd, exactly like `gitl mcp` launched from a repo.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// callTool invokes a handler with raw JSON arguments.
func callTool(t *testing.T, h mcpserver.ToolHandler, args string) (*mcpserver.ToolsCallResult, error) {
	t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	return h(context.Background(), raw)
}

// resultJSON asserts the tool result is a single text block containing valid
// JSON and unmarshals it.
func resultJSON(t *testing.T, res *mcpserver.ToolsCallResult) map[string]any {
	t.Helper()
	if res == nil {
		t.Fatal("nil ToolsCallResult")
	}
	if res.IsError {
		t.Fatalf("unexpected IsError result: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("expected exactly one text content block, got %+v", res.Content)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &m); err != nil {
		t.Fatalf("tool result is not valid JSON: %v\n%s", err, res.Content[0].Text)
	}
	return m
}

func TestMCPReviewHandlerOfflineRange(t *testing.T) {
	dir := setupRepo(t, false)
	chdir(t, dir)
	cfg := coreTestConfig(t)
	var errOut bytes.Buffer

	res, err := callTool(t, mcpReviewHandler(cfg, &errOut), `{"range":"HEAD~1..HEAD"}`)
	if err != nil {
		t.Fatalf("gitl_review: %v", err)
	}
	m := resultJSON(t, res)

	if m["schema_version"] != float64(1) {
		t.Errorf("schema_version = %v, want 1", m["schema_version"])
	}
	if m["range"] != "HEAD~1..HEAD" {
		t.Errorf("range = %v, want HEAD~1..HEAD", m["range"])
	}
	if m["offline"] != true {
		t.Error("offline = false, want true (no API key configured)")
	}
	risk, _ := m["risk"].(map[string]any)
	switch risk["level"] {
	case "low", "medium", "high":
	default:
		t.Errorf("risk.level = %v, want low|medium|high", risk["level"])
	}
	if s, _ := m["review_markdown"].(string); s == "" {
		t.Error("review_markdown is empty, want the offline review body")
	}
	// The offline notice must land on errOut (the server's stderr), never
	// inside the protocol result.
	if !strings.Contains(errOut.String(), "no LLM API key configured") {
		t.Errorf("expected the offline-mode notice on errOut, got %q", errOut.String())
	}
}

func TestMCPReviewHandlerOfflineStaged(t *testing.T) {
	dir := setupRepo(t, false)
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("pending change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "staged.txt")
	chdir(t, dir)
	cfg := coreTestConfig(t)

	res, err := callTool(t, mcpReviewHandler(cfg, &bytes.Buffer{}), `{"staged":true}`)
	if err != nil {
		t.Fatalf("gitl_review staged: %v", err)
	}
	m := resultJSON(t, res)
	if m["range"] != "staged" {
		t.Errorf("range = %v, want %q", m["range"], "staged")
	}
	stats, _ := m["stats"].(map[string]any)
	if stats["files_changed"] != float64(1) {
		t.Errorf("stats.files_changed = %v, want 1", stats["files_changed"])
	}
}

// TestMCPReviewHandlerModeValidation: the range/pr/staged selectors are
// mutually exclusive and exactly one is required — the same validation the CLI
// enforces (shared resolveReviewSource), plus the MCP-only range-vs-pr check.
func TestMCPReviewHandlerModeValidation(t *testing.T) {
	cfg := coreTestConfig(t)

	tests := []struct {
		name    string
		args    string
		wantErr string
	}{
		{"none selected", `{}`, "provide a revision range"},
		{"empty args object", ``, "provide a revision range"},
		{"range and staged", `{"range":"HEAD~1..HEAD","staged":true}`, "cannot combine"},
		{"pr and staged", `{"pr":5,"staged":true}`, "cannot combine"},
		{"range and pr", `{"range":"HEAD~1..HEAD","pr":5}`, "mutually exclusive"},
		{"pr zero", `{"pr":0}`, "invalid PR number"},
		{"unknown argument", `{"revrange":"HEAD~1..HEAD"}`, "invalid tool arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callTool(t, mcpReviewHandler(cfg, &bytes.Buffer{}), tt.args)
			if err == nil {
				t.Fatalf("args %q: expected an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("args %q: error %q should contain %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

// TestMCPDigestHandlerDefaultScope: without an explicit repos argument the
// digest is scoped to the server's working directory only (no .gitl.yaml
// digest.repos configured), with the default 7-day window.
func TestMCPDigestHandlerDefaultScope(t *testing.T) {
	dir := setupRepo(t, false)
	chdir(t, dir)
	cfg := coreTestConfig(t)

	res, err := callTool(t, mcpDigestHandler(cfg), `{}`)
	if err != nil {
		t.Fatalf("gitl_digest: %v", err)
	}
	m := resultJSON(t, res)

	if m["days"] != float64(defaultDigestDays) {
		t.Errorf("days = %v, want default %d", m["days"], defaultDigestDays)
	}
	repos, _ := m["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("repos length = %d, want 1 (the current directory only)", len(repos))
	}
	repo, _ := repos[0].(map[string]any)
	if repo["path"] != "." {
		t.Errorf("repos[0].path = %v, want %q (cwd scope)", repo["path"], ".")
	}
	if repo["ok"] != true {
		t.Errorf("repos[0].ok = %v, want true (error: %v)", repo["ok"], repo["error"])
	}
}

// TestMCPDigestHandlerConfigRepos: without an explicit repos argument,
// digest.repos from the loaded config (i.e. .gitl.yaml) defines the scope.
func TestMCPDigestHandlerConfigRepos(t *testing.T) {
	repoA := setupRepo(t, false)
	chdir(t, t.TempDir()) // cwd is NOT a git repo — proves cfg repos are used
	cfg := coreTestConfig(t)
	cfg.Digest.Repos = []config.RepoRef{{Path: repoA}}

	res, err := callTool(t, mcpDigestHandler(cfg), `{"days":3}`)
	if err != nil {
		t.Fatalf("gitl_digest: %v", err)
	}
	m := resultJSON(t, res)

	if m["days"] != float64(3) {
		t.Errorf("days = %v, want 3", m["days"])
	}
	repos, _ := m["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("repos length = %d, want 1 (from digest.repos)", len(repos))
	}
	repo, _ := repos[0].(map[string]any)
	if repo["path"] != repoA {
		t.Errorf("repos[0].path = %v, want %q", repo["path"], repoA)
	}
	if repo["ok"] != true {
		t.Errorf("repos[0].ok = %v, want true (error: %v)", repo["ok"], repo["error"])
	}
}

// TestMCPDigestHandlerExplicitRepos: an explicit repos argument replaces both
// the cwd default and digest.repos wholesale — the client's deliberate choice
// is honored as-is.
func TestMCPDigestHandlerExplicitRepos(t *testing.T) {
	repoA := setupRepo(t, false)
	repoB := setupRepo(t, false)
	ignored := setupRepo(t, false)
	chdir(t, t.TempDir())
	cfg := coreTestConfig(t)
	cfg.Digest.Repos = []config.RepoRef{{Path: ignored}}

	args, err := json.Marshal(map[string]any{"repos": []string{repoA, repoB}})
	if err != nil {
		t.Fatal(err)
	}
	res, callErr := callTool(t, mcpDigestHandler(cfg), string(args))
	if callErr != nil {
		t.Fatalf("gitl_digest: %v", callErr)
	}
	m := resultJSON(t, res)

	repos, _ := m["repos"].([]any)
	if len(repos) != 2 {
		t.Fatalf("repos length = %d, want 2 (explicit list replaces config wholesale)", len(repos))
	}
	got := make([]string, 0, 2)
	for _, r := range repos {
		repo, _ := r.(map[string]any)
		if repo["ok"] != true {
			t.Errorf("repo %v not ok (error: %v)", repo["path"], repo["error"])
		}
		got = append(got, repo["path"].(string))
	}
	want := []string{repoA, repoB}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("digested repos = %v, want %v (ignored config repo %q must not appear)", got, want, ignored)
	}
}

func TestMCPDigestHandlerRejectsBadArgs(t *testing.T) {
	cfg := coreTestConfig(t)

	tests := []struct {
		name    string
		args    string
		wantErr string
	}{
		{"zero days", `{"days":0}`, "positive integer"},
		{"negative days", `{"days":-2}`, "positive integer"},
		{"unknown argument", `{"day":7}`, "invalid tool arguments"},
		{"blank explicit repos", `{"repos":["  "]}`, "no repository paths"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callTool(t, mcpDigestHandler(cfg), tt.args)
			if err == nil {
				t.Fatalf("args %q: expected an error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("args %q: error %q should contain %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

// schemaProperties parses an inputSchema and returns its property-name set.
func schemaProperties(t *testing.T, schema string) map[string]bool {
	t.Helper()
	var s struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	dec := json.NewDecoder(strings.NewReader(schema))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if s.Type != "object" {
		t.Errorf("schema type = %q, want object", s.Type)
	}
	if s.AdditionalProperties {
		t.Error("schema must set additionalProperties:false to match strict arg decoding")
	}
	props := make(map[string]bool, len(s.Properties))
	for name := range s.Properties {
		props[name] = true
	}
	return props
}

// jsonTags collects the json tag names of a struct type.
func jsonTags(t *testing.T, v any) map[string]bool {
	t.Helper()
	tags := make(map[string]bool)
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("field %s has no usable json tag", rt.Field(i).Name)
		}
		tags[tag] = true
	}
	return tags
}

// TestMCPToolSchemasMatchArgs: the advertised JSON Schemas and the Go argument
// structs must agree on the exact property set — a drift would either reject
// documented arguments or silently advertise unimplemented ones.
func TestMCPToolSchemasMatchArgs(t *testing.T) {
	if got, want := schemaProperties(t, reviewToolSchema), jsonTags(t, reviewToolArgs{}); !reflect.DeepEqual(got, want) {
		t.Errorf("gitl_review schema properties %v != reviewToolArgs tags %v", got, want)
	}
	if got, want := schemaProperties(t, digestToolSchema), jsonTags(t, digestToolArgs{}); !reflect.DeepEqual(got, want) {
		t.Errorf("gitl_digest schema properties %v != digestToolArgs tags %v", got, want)
	}
}

// TestMCPCommandRegistered: `gitl mcp` exists in the command tree with no
// stdout side effects at registration time.
func TestMCPCommandRegistered(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "mcp" {
			return
		}
	}
	t.Error("root command has no `mcp` subcommand")
}
