# gitl

[![Action self-test](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml/badge.svg)](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml)

**AI-powered git history reviewer for CLI and CI.** `gitl` (git-log-lens) reads a
repository's git history and turns it into a structured engineering artifact via LLM:

- **`gitl review <range>`** — AI review of a commit range / PR with machine-readable
  risk scoring (`low|medium|high`) for CI gating (`--fail-on=high` → non-zero exit code);
  streams tokens to the terminal in real time; on-disk LLM response cache;
  custom system-prompt templates; `--staged` reviews staged (uncommitted) changes
  before `git commit`.
- **`gitl changelog [<range>]`** — Keep a Changelog-style changelog, grouped by
  conventional commits (defaults to last tag → `HEAD`); deterministic by default,
  `--ai` optionally rewrites it with the model as readable release-note prose;
- **`gitl digest [--days=N] [--repos=a,b,c]`** — activity summary by author/topic/file,
  including **multiple repositories in parallel**; interactive TUI viewer (`--tui`).

A clean CLI binary plus a GitHub Action wrapper — no server, no database, no hosted key
storage. **BYOK** (bring your own key) with multi-provider support: OpenAI-compatible API,
Ollama (local/self-hosted), Azure OpenAI, native Anthropic (Claude), Google Gemini.
No telemetry.

> **Status:** `v0.4.1` released — all three commands work on real repositories with all
> three output formats (`md|text|json`). The Action posts AI reviews as sticky PR comments
> and gates on risk score. Release binaries are cross-compiled, cosign-signed, and covered
> by SLSA L3 build provenance (see [VERIFY.md](VERIFY.md)).

## Quick start

Requires **Go 1.22+** and **git** in `PATH`.

```bash
# build
go build ./...

# AI review of a commit range — streams tokens to the terminal in real time
GITL_API_KEY=sk-... go run ./cmd/gitl review HEAD~5..HEAD

# no key = deterministic offline review (heuristic risk, no network call)
go run ./cmd/gitl review HEAD~5..HEAD

# review staged (not yet committed) changes before `git commit`
go run ./cmd/gitl review --staged

# review a GitHub PR by number — requires the `gh` CLI (installed + authenticated);
# resolves base/head via gh, fetches `pull/N/head` locally when needed, and reviews
# the merge-base diff (base...head), same as GitHub shows
go run ./cmd/gitl review pr/42

# machine-readable output for CI + risk gating
go run ./cmd/gitl review HEAD~5..HEAD --format=json
go run ./cmd/gitl review HEAD~5..HEAD --fail-on=high   # non-zero exit on high risk

# estimate cost without making an API call
go run ./cmd/gitl review HEAD~5..HEAD --dry-run

# custom system-prompt template (e.g. your team's review policy) — set via
# config only (prompt.system_template_file); there is no --system-template flag
# see Configuration → Custom templates below

# skip the on-disk LLM cache (always call the model)
go run ./cmd/gitl review HEAD~5..HEAD --no-cache

# disable streaming (non-interactive, buffered output)
go run ./cmd/gitl review HEAD~5..HEAD --no-stream

# changelog from last tag (or full history if no tags) — no LLM by default
go run ./cmd/gitl changelog
go run ./cmd/gitl changelog v1.2.0..HEAD --format=json

# AI changelog: the model rewrites the grouped result as release-note prose and
# reclassifies significant non-conventional commits out of "Other". Without an API
# key (or on a malformed model response) it falls back to the deterministic
# changelog with a warning — never fails. --dry-run/--max-cost-usd/--no-cache work
# the same as for review.
GITL_API_KEY=sk-... go run ./cmd/gitl changelog --ai

# activity summary for the last N days — no LLM
go run ./cmd/gitl digest --days=14

# multi-repo digest: runs in parallel; one unreachable repo does not fail the rest
go run ./cmd/gitl digest --repos=../service-a,../service-b --format=json

# interactive TUI viewer for digest (requires a TTY)
go run ./cmd/gitl digest --days=14 --tui

go run ./cmd/gitl version
go run ./cmd/gitl --help

# tests
go test ./...
```

Install:

```bash
# Go toolchain
go install github.com/akomyagin/gitl/cmd/gitl@latest

# Homebrew (macOS/Linux)
brew install akomyagin/tap/gitl

# Or download a signed release binary from GitHub Releases (see VERIFY.md)
```

### Local multi-provider test (Ollama)

`docker-compose.yml` starts **only the dev dependency** — a local Ollama instance for
testing the multi-provider LLM client (`gitl` itself is not containerized):

```bash
docker compose up ollama
```

## Configuration

Two levels, merged by priority:
**flag > env > `.gitl.yaml` (repo) > `~/.config/gitl/config.yaml` (personal)**.
The repo-level `.gitl.yaml` is committed as a shared team policy (risk threshold, excluded
paths, changelog categories). Without a key, `gitl` runs in deterministic offline mode.

In offline mode — or when a real model omits a valid risk block and `gitl` falls back to
the heuristic — the risk header is annotated with `*(heuristic)*` (and `"heuristic": true`
in `--format=json`), so a deterministic score is never mistaken for a model's own judgement.

### Providers (`llm.provider`)

```yaml
# OpenAI-compatible API (default)
llm:
  provider: "openai"
  api_key: ""            # or env GITL_API_KEY
  base_url: "https://api.openai.com/v1"
  model: "gpt-4o-mini"

# Ollama — local/self-hosted, no key, free
llm:
  provider: "ollama"
  base_url: "http://localhost:11434/v1"
  model: "llama3.1"

# Azure OpenAI — custom auth/endpoint format
llm:
  provider: "azure_openai"
  api_key: ""             # or env GITL_API_KEY
  model: "gpt-4o-mini"    # used only for cost estimation
  azure_openai:
    endpoint: "https://<resource>.openai.azure.com"
    deployment: "<deployment-name>"
    api_version: "2024-08-01-preview"

# Anthropic (native Claude Messages API)
llm:
  provider: "anthropic"
  api_key: ""            # or env GITL_API_KEY
  model: "claude-sonnet-4-6"
  # base_url optional; defaults to https://api.anthropic.com

# Google Gemini (Google AI Studio)
llm:
  provider: "gemini"
  api_key: ""            # or env GITL_API_KEY
  model: "gemini-2.5-flash"
  # base_url optional; defaults to https://generativelanguage.googleapis.com/v1beta
```

### Streaming (`output.stream`)

When reviewing interactively (`md` or `text` format on a TTY), `gitl` streams tokens
to the terminal as they arrive — no waiting for the full response. Streaming is on by
default and switches off automatically in CI (non-TTY stdout) or with `--format=json`.

Streaming is currently implemented **only for the OpenAI-compatible provider**
(`openai` / `ollama` / `azure_openai`). With the native `anthropic` or `gemini`
provider, `gitl` transparently produces the same review as a single buffered
response (no token-by-token output) regardless of `output.stream` / `--no-stream`.

```yaml
output:
  stream: true   # default; set false to always buffer
```

Disable per-call: `gitl review HEAD~5..HEAD --no-stream`

### LLM response cache (`cache`)

`gitl review` caches model responses on disk (SHA-256 of provider + model + prompt).
Identical diffs reuse the cached result instantly, with no API call or cost.

```yaml
cache:
  enabled: true    # default
  ttl_hours: 24    # entries older than this are ignored
```

Cache lives in `~/.cache/gitl/review/` (XDG-compliant). Disable per-call:
`gitl review HEAD~5..HEAD --no-cache`

### Custom templates (`prompt.system_template_file` / `output.template_file`)

Two independent, config-only overrides (there is no CLI flag for either):

- **`prompt.system_template_file`** — your own **review system prompt**, to steer
  the model's focus (security checklist, architecture constraints, team rules):

  ```yaml
  prompt:
    system_template_file: "./review-policy.md"   # path relative to CWD
  ```

  The system-prompt template has access to `{{ .Commits }}`, `{{ .Diff }}`,
  `{{ .Range }}`, `{{ .Staged }}` (see `internal/prompt/templates.go`).

- **`output.template_file`** — your own **`md`-format render template** for the
  finished review artifact:

  ```yaml
  output:
    template_file: "./review-output.tmpl"   # path relative to CWD
  ```

  The output template has the render template functions in
  `internal/render/render.go` (`render.TemplateFuncs()`).

## GitHub Action

`gitl` can be wired up as a GitHub Action: it AI-reviews a pull request's commits and
posts a comment with the risk score, optionally blocking merge above a threshold. The
Action builds `gitl` from source (`go install` at a pinned version).

Add `.github/workflows/gitl-review.yml` to your repository:

```yaml
name: gitl review
on:
  pull_request:

permissions:
  contents: read          # for checkout
  pull-requests: write    # to post the review comment

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0    # required: without full history base..head won't resolve

      - uses: akomyagin/gitl@v0.4.1
        with:
          gitl-api-key: ${{ secrets.GITL_API_KEY }}   # BYOK, see below
          fail-on: high                               # optional: block merge on high risk
```

Security best practices:

- **Key via `secrets.*` only.** `gitl-api-key` comes from `secrets.GITL_API_KEY` (set
  under Settings → Secrets and variables → Actions), never hardcoded in YAML or committed.
  If the secret is not set, the Action runs in deterministic **offline mode** (no network,
  no cost).
- **Minimal `permissions:`.** Only `pull-requests: write` (posting the comment) and
  `contents: read` (checkout) are needed — do not grant broader rights.
- **`fetch-depth: 0` is required.** GitHub provides `base`/`head` SHAs in the
  `pull_request` event, but a shallow clone won't resolve `base.sha..head.sha`.
- **`fail-on` defaults to `never`.** The Action only comments; it does not block merges
  unless you opt in explicitly (`fail-on: high`, etc.) — same "WARN by default, hard gate
  is explicit opt-in" principle as the CLI (`--fail-on`).
- **Diff privacy.** In CI, the diff is sent to whichever LLM provider is configured
  (default: OpenAI-compatible API). For private code, use a self-hosted/enterprise provider
  (Ollama, Azure OpenAI) — see Providers above.
- **Secret masking.** GitHub automatically masks `secrets.*` values in runner logs as
  `***`, but that's not a reason to print the key in your own workflow steps.

## License

[MIT](LICENSE).
