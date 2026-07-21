# gitl-cli (npm wrapper)

npm wrapper for [gitl](https://github.com/akomyagin/gitl) — an AI-powered git
history reviewer written in Go: AI review of commit ranges with risk scoring
(`low|medium|high`) for CI gating, Keep a Changelog-style changelogs, and
multi-repo activity digests. BYOK, multi-provider (OpenAI-compatible, Ollama,
Azure OpenAI, Anthropic, Gemini), no telemetry.

This package contains no code of its own: on install it downloads the
prebuilt `gitl` binary for your platform from the project's GitHub
Releases and verifies its SHA256 checksum.

> **Note:** `gitl-cli` is not yet published to the npm registry (the
> release pipeline's `npm publish` step is gated on an `NPM_TOKEN` secret
> that is not configured yet). Until it goes live, install `gitl` via
> `go install`, Homebrew, or a GitHub Release binary — see the main README.
> Once published, the commands below will work:

```bash
npx gitl-cli review HEAD~5..HEAD

# or install globally — puts the `gitl` command on your PATH
npm install -g gitl-cli
gitl review HEAD~5..HEAD
```

Full documentation, configuration reference, and sources:
<https://github.com/akomyagin/gitl>.

License: MIT.
