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

> **Status:** `v0.4.3` released — all three commands work on real repositories with all
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

# npm — downloads the prebuilt binary for your platform from GitHub Releases
# and verifies its SHA256 checksum (no Go toolchain needed).
npx gitl-cli review HEAD~5..HEAD   # or: npm install -g gitl-cli

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

> **Trust note:** `prompt.system_template_file`/`output.template_file` can be
> set by a repo-level `.gitl.yaml`, not just your personal config — so running
> `gitl review` against a cloned repository you don't control can point it at
> a template *inside that same repository*. This is the intended mechanism
> for a team's shared review policy, not a bug: `text/template` here can't
> read arbitrary files or execute code, but treat an untrusted repo's
> `.gitl.yaml` with the same caution you'd give its `.git/hooks` or build
> scripts.

## GitHub Action

`gitl` can be wired up as a GitHub Action: it AI-reviews a pull request's commits and
posts a comment with the risk score, optionally blocking merge above a threshold. The
Action builds `gitl` from source (`go install` at a pinned version). Also listed on the
[GitHub Marketplace](https://github.com/akomyagin/gitl) if you'd rather add it from there.

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

      - uses: akomyagin/gitl@v0.4.3
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

## Gitea Actions (experimental)

The same `action.yml` also runs on [Gitea Actions](https://docs.gitea.com/usage/actions/overview) —
Gitea's runner executes GitHub-style composite actions, and gitl's action detects the
platform at run time via the `GITEA_ACTIONS=true` variable that Gitea's act_runner
injects into every job. The only platform-specific part — posting the sticky PR
comment — then goes through Gitea's REST API
(`POST`/`PATCH /api/v1/repos/{owner}/{repo}/issues/...`) with `curl` instead of the
`gh` CLI, which only speaks GitHub's API. GitHub users are unaffected: without
`GITEA_ACTIONS` the action behaves exactly as before.

Add `.gitea/workflows/gitl-review.yml` to your repository (full commented example:
[`.gitea/workflows/gitl-review.yml`](.gitea/workflows/gitl-review.yml) in this repo):

```yaml
name: gitl review
on:
  pull_request:

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: https://github.com/actions/checkout@v7
        with:
          fetch-depth: 0
      - uses: https://github.com/akomyagin/gitl@v0.4.3
        with:
          gitl-api-key: ${{ secrets.GITL_API_KEY }}   # BYOK; omit for offline mode
```

Requirements: Actions enabled, a recent act_runner (node24-capable), and a runner
image providing `bash`, `git`, `curl`, `jq` and node. `GITL_API_KEY` goes into
Gitea's Actions secrets, never into the YAML — same BYOK rules as on GitHub.

> **Verification status — read before relying on this.** The `curl`-based REST
> calls (list comments, create, patch, sticky-detection) were run against a real
> Gitea instance (`gitea/gitea` in Docker) end-to-end — list-empty → POST-create →
> re-list-finds-it → PATCH-update → still exactly one comment. That part works as
> written. What's **not yet verified** is the surrounding `act_runner` CI context:
> whether `GITEA_ACTIONS`/`GITHUB_API_URL`/the PR event payload look exactly as
> assumed inside a live workflow run (this was cross-checked against Gitea/
> act_runner/act-fork source, not run inside an actual job). Treat the
> *CI-triggering* path as experimental until someone confirms a green run
> end-to-end inside real Gitea Actions; bug reports from real instances are very
> welcome.

## GitLab CI (experimental)

gitl also ships a [GitLab CI/CD component](https://docs.gitlab.com/ee/ci/components/) —
[`templates/gitl-review.yml`](templates/gitl-review.yml) — mirroring the GitHub Action:
it installs gitl with `go install` at a pinned version, reviews the merge request's
range (`$CI_MERGE_REQUEST_DIFF_BASE_SHA..$CI_COMMIT_SHA`), renders the comment through
the shared platform-neutral [`ci/comment.sh`](ci/comment.sh), and creates/updates a
**sticky MR note** through GitLab's REST API (same `<!-- gitl-review -->` marker as on
GitHub/Gitea). The job only runs in merge request pipelines.

This repository lives on GitHub, so the component is not in a GitLab CI/CD Catalog
yet — consume it via `include:remote` (inputs work with remote includes):

```yaml
# .gitlab-ci.yml
include:
  - remote: "https://raw.githubusercontent.com/akomyagin/gitl/v0.4.3/templates/gitl-review.yml"
    inputs:
      fail_on: "never"      # default; set "high" to block risky MRs
      # max_cost_usd: "0.50"
      # gitl_version: "v0.4.3"
```

Setup — two CI/CD variables (Settings → CI/CD → Variables, both **masked**, never
in YAML):

- **`GITL_API_KEY`** — the BYOK LLM key. Optional: without it gitl runs the
  deterministic **offline review** (no network, no cost). Defining the project
  variable is enough — it takes precedence over the component's empty
  `gitl_api_key` input default. If you use the input instead, pass a *variable
  reference* (`gitl_api_key: $MY_LLM_KEY`), never a literal key: input values are
  interpolated into the pipeline config.
- **`GITL_GITLAB_TOKEN`** — token for posting the MR note (project access token or
  PAT, `api` scope, Reporter role or higher; sent as `PRIVATE-TOKEN`). If unset,
  the job falls back to `CI_JOB_TOKEN` (`JOB-TOKEN` header) — but on most GitLab
  configurations `CI_JOB_TOKEN` is **not** authorized for the Notes API, so the
  fallback is expected to fail (with an explicit error message, not a silent
  skip). An explicit `GITL_GITLAB_TOKEN` is the reliable path.

A full commented self-test pipeline — also the closest thing to a complete usage
example — is [`.gitlab-ci-selftest.yml`](.gitlab-ci-selftest.yml) (runnable as
`.gitlab-ci.yml` in a GitLab mirror of this repo).

> **Verification status — read before relying on this.** The GitLab REST calls
> (list MR notes + sticky-marker detection, `POST` create, `PUT` update) and the
> component YAML itself (`spec:`/`inputs:` interpolation, `include:local` with
> inputs, via the CI Lint API) were verified end-to-end against a real local
> GitLab CE instance (`gitlab/gitlab-ce` 19.2.0 in Docker) on a real merge
> request — list-empty → POST-create → re-list-finds-it → PUT-update → still
> exactly one note — using the exact `curl`/`jq` commands from the template. What's **not yet
> verified** is a live pipeline run: the values of
> `CI_MERGE_REQUEST_DIFF_BASE_SHA`/`CI_COMMIT_SHA`/`CI_JOB_URL` inside a real
> merge-request pipeline are written from GitLab docs, not observed, and the
> `CI_JOB_TOKEN`-fallback rejection is documented per GitLab's job-token
> allowlist docs, not reproduced. Treat the *pipeline* path as experimental
> until someone confirms a green end-to-end run; bug reports welcome.

> **Trust note.** The component downloads `ci/comment.sh` from
> `raw.githubusercontent.com` at `gitl_version` and executes it — with no
> checksum/signature check, same trust boundary as the `go install
> ...@${gitl_version}` line right above it (same repo, same ref). If that
> matters for your threat model, pin `gitl_version` to a commit SHA rather
> than a tag (tags are movable). Publishing this component to the GitLab
> CI/CD Catalog would remove the fetch entirely — planned, not done yet.

## Bitbucket Pipelines (experimental)

The Bitbucket integration ships as a [Pipe](https://support.atlassian.com/bitbucket-cloud/docs/what-are-pipes/) —
and pipes are Docker images by definition, so unlike the GitHub/Gitea action and the
GitLab component (plain YAML wrappers) this one is a self-contained image:
[`bitbucket-pipe/Dockerfile`](bitbucket-pipe/Dockerfile) builds a static `gitl`
binary and bakes in the shared [`ci/comment.sh`](ci/comment.sh) renderer plus the
entrypoint [`bitbucket-pipe/pipe.sh`](bitbucket-pipe/pipe.sh). The pipe resolves the
PR range (`$BITBUCKET_PR_DESTINATION_COMMIT..$BITBUCKET_COMMIT`), runs
`gitl review --format=json`, and creates/updates a **sticky PR comment** through the
Bitbucket Cloud REST API (same `<!-- gitl-review -->` marker as on the other
platforms). Variables reference: [`bitbucket-pipe/pipe.yml`](bitbucket-pipe/pipe.yml).

> **Image status.** The image is **not published to Docker Hub yet** — the
> release workflow's `docker-publish` job skips the push until
> `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` secrets are provisioned (same
> graceful-skip pattern as npm). Until then, build it yourself from the
> repository root:
> `docker build -f bitbucket-pipe/Dockerfile -t akomyagin/gitl-review-pipe:0.4.3 .`
> and push it to a registry your pipeline can pull from.

```yaml
# bitbucket-pipelines.yml
pipelines:
  pull-requests:
    '**':
      - step:
          name: gitl review
          clone:
            depth: full   # the default depth-50 clone may not contain the PR base commit
          script:
            - pipe: docker://akomyagin/gitl-review-pipe:0.4.3
              variables:
                GITL_API_KEY: $GITL_API_KEY                    # BYOK; omit for offline review
                GITL_BITBUCKET_TOKEN: $GITL_BITBUCKET_TOKEN    # posts the PR comment
                # FAIL_ON: "high"        # default "never" — comment only, no gate
                # MAX_COST_USD: "0.50"
```

Setup — two **secured** repository/workspace variables (Repository settings →
Pipelines → Repository variables; always referenced as `$VAR`, never literal values
in the YAML):

- **`GITL_API_KEY`** — the BYOK LLM key. Optional: without it gitl runs the
  deterministic **offline review** (no network, no cost).
- **`GITL_BITBUCKET_TOKEN`** — credential for posting the PR comment: a
  repository/project/workspace **access token** with the `pullrequest:write`
  scope, sent as `Authorization: Bearer`. Alternative: set
  `GITL_BITBUCKET_USER` + `GITL_BITBUCKET_APP_PASSWORD` (app password with
  `pullrequest:write`) for Basic auth instead. If neither is configured the
  pipe fails fast with an explicit message — before spending any LLM budget.

> **Supply-chain note (why this differs from the GitLab component).** The pipe
> executes nothing fetched at run time: the `gitl` binary, `ci/comment.sh` and
> the entrypoint are all built into the versioned image from one source tree.
> The GitLab component has to download `ci/comment.sh` over the network with no
> integrity check (see its trust note above); the pipe closes that gap by
> construction.

> **Verification status — read before relying on this.** The image build and
> the full in-container flow were verified locally: `docker build` from this
> repo, then `docker run` against a real test git repository with emulated
> `BITBUCKET_*` variables — offline review → correct sticky `comment.md` →
> comment create (`POST`), sticky update (`PUT`, still exactly one comment)
> and `--fail-on` exit-code propagation, exercised end-to-end against a local
> mock of the Bitbucket comments API; the fail-fast paths (missing
> credential/PR variables) and the fallback notice on a bad range were also
> exercised in the container. What's **not yet verified**: anything touching
> real Bitbucket infrastructure — the REST calls against api.bitbucket.org
> (shapes taken from Atlassian's API docs), the exact predefined variables
> inside a live PR pipeline (`BITBUCKET_PR_DESTINATION_COMMIT` etc. are
> documented assumptions, not observed values), and how Pipelines mounts the
> clone into pipe containers. Treat the *live pipeline* path as experimental
> until someone confirms a green run on a real Bitbucket workspace; bug
> reports welcome.

## License

[MIT](LICENSE).
