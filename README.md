# gitl

**AI code review for your CI, with a risk score you can gate merges on — your key, any provider, or fully offline.**

`gitl` (git-log-lens) reads a commit range and turns it into a structured engineering
artifact: an AI review with a machine-readable risk level, a deterministic changelog,
and a multi-repo activity digest — one Go binary, no server, no database.

[![CI](https://github.com/akomyagin/gitl/actions/workflows/ci.yml/badge.svg)](https://github.com/akomyagin/gitl/actions/workflows/ci.yml)
[![Action self-test](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml/badge.svg)](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml)
[![Latest release](https://img.shields.io/github/v/release/akomyagin/gitl)](https://github.com/akomyagin/gitl/releases)
[![License: MIT](https://img.shields.io/github/license/akomyagin/gitl)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/akomyagin/gitl.svg)](https://pkg.go.dev/github.com/akomyagin/gitl)
[![Signed releases](https://img.shields.io/badge/releases-signed%20(cosign%20%C2%B7%20SLSA%20L3)-blueviolet)](VERIFY.md)

![gitl review streaming a risk score](site/assets/demo-review.gif)

*`gitl review` — AI review of a commit range with a machine-readable risk level,
streamed to the terminal.*

## Why

Most AI code reviewers are SaaS: your diff goes to someone else's server, and what
you get back is a comment, not a contract. I wanted the opposite — a CLI/CI tool
where the review runs on **my key, my provider (or no provider at all), and produces
a risk level CI can branch on**. One binary covers three jobs that usually take
three separate tools: a review with a risk gate, a changelog, and a multi-repo
digest. There's no telemetry and no hosted key storage, and without a key it still
works: a deterministic offline heuristic keeps CI runs green and free.

## Install

```bash
# Homebrew (macOS/Linux)
brew install akomyagin/tap/gitl

# npm — downloads the prebuilt, checksum-verified binary for your platform
npx gitl-cli review HEAD~5..HEAD        # or: npm install -g gitl-cli

# Go toolchain
go install github.com/akomyagin/gitl/cmd/gitl@latest
```

Or grab a signed binary from [GitHub Releases](https://github.com/akomyagin/gitl/releases)
(see [VERIFY.md](VERIFY.md) for how to verify it). Requires `git` in `PATH`;
Go 1.22+ is only needed for `go install` / building from source.

> **Signed releases (cosign keyless + SLSA L3 provenance), no telemetry, BYOK —
> your API key never leaves your machine.** Verify any release before you run it:
> [VERIFY.md](VERIFY.md).

## What it does

- **`gitl review <range>`** — AI review with a machine risk level
  (`low|medium|high`); `--fail-on=high` exits with code 2 so CI can gate on it;
  streams tokens live; on-disk response cache (plus an opt-in shared remote cache
  for CI); `--staged` reviews uncommitted changes; `pr/N` reviews a GitHub PR
  via `gh`.
- **`gitl changelog [<range>]`** — Keep-a-Changelog output grouped by conventional
  commits, deterministic by default; `--ai` rewrites it as release-note prose and
  falls back to the deterministic result without a key — it never fails.
- **`gitl digest [--days=N] [--repos=a,b,c]`** — activity summary by
  author/topic/file across multiple repos in parallel, with an interactive TUI
  viewer (`--tui`).

Providers (BYOK): OpenAI-compatible API, Ollama (local/self-hosted),
Azure OpenAI, native Anthropic (Claude), Google Gemini — or no provider at all.

It's also an MCP server (`gitl mcp`) and ships CI wrappers for GitHub Actions,
GitLab, Bitbucket, and Gitea — see the
[documentation site](https://akomyagin.github.io/gitl/docs.html) for all of it.

## Quick start

```bash
# AI review of a commit range (streams to the terminal)
GITL_API_KEY=sk-... gitl review HEAD~5..HEAD

# no key = deterministic offline review (heuristic risk, no network)
gitl review HEAD~5..HEAD

# machine-readable + CI gate
gitl review HEAD~5..HEAD --format=json --fail-on=high   # exit 2 on high risk

# changelog since the last tag; activity digest for 14 days
gitl changelog
gitl digest --days=14
```

Exit codes are part of the contract: `0` — ok, `1` — tool/runtime error,
`2` — the `--fail-on` risk gate triggered. Full command reference →
[documentation site](https://akomyagin.github.io/gitl/docs.html).

![gitl --fail-on=high failing a CI check](site/assets/demo-gate.gif)

*`--fail-on=high` turns a high-risk range into a non-zero exit code (2) your CI
can gate on.*

## Use cases

- **Gate a PR on AI risk in CI** — the GitHub Action posts a sticky review comment
  and fails the check above your threshold.
- **Pre-commit safety net** — `gitl review --staged` runs offline before every
  commit, at zero cost.
- **Release notes in one command** — `gitl changelog --ai`.
- **Multi-repo standup digest** — `gitl digest --repos=… --tui`.

See these worked end-to-end on the
[use cases page](https://akomyagin.github.io/gitl/use-cases.html).

![gitl digest --tui browsing a multi-repo activity summary](site/assets/demo-digest-tui.gif)

*`gitl digest --tui` — an interactive viewer for the multi-repo activity summary.*

## How it compares

| | BYOK / any provider | Offline mode | Machine risk score → CI gate | Review + changelog + digest |
|---|:---:|:---:|:---:|:---:|
| **gitl** | ✓ | ✓ | ✓ | ✓ |
| PR-Agent (Qodo) | ✓ | — | — | — |
| CodeRabbit | — (SaaS) | — | — | — |
| git-cliff | n/a (no LLM) | ✓ | — | changelog only |

Feature comparison as of 2026-09; corrections welcome.

## Documentation

Full usage docs, the configuration reference, and worked use cases live on the
**[documentation site](https://akomyagin.github.io/gitl/)** (also in
[Russian](https://akomyagin.github.io/gitl/ru/)).

Русская версия README — [README_RU.md](README_RU.md).

## Contributing

I develop this in the open, and it's a one-person project — issues, questions,
and bug reports from real CI setups are the most valuable thing you can send my
way. Open an issue or a PR; I read everything.

## License

[MIT](LICENSE).
