# gitl-cli (npm wrapper)

npm wrapper for [gitl](https://github.com/akomyagin/gitl) — an AI-powered git
history reviewer written in Go: AI review of commit ranges with risk scoring
(`low|medium|high`) for CI gating, Keep a Changelog-style changelogs, and
multi-repo activity digests. BYOK, multi-provider (OpenAI-compatible, Ollama,
Azure OpenAI, Anthropic, Gemini), no telemetry.

This package contains no code of its own: on install it downloads the
prebuilt `gitl` binary for your platform from the project's GitHub
Releases and verifies its SHA256 checksum.

![gitl review streaming a risk score](https://raw.githubusercontent.com/akomyagin/gitl/master/site/assets/demo-review.gif)

```bash
npx gitl-cli review HEAD~5..HEAD

# or install globally — puts the `gitl` command on your PATH
npm install -g gitl-cli
gitl review HEAD~5..HEAD
```

Full documentation, configuration reference, and sources:
<https://github.com/akomyagin/gitl>. Use cases and a full command/config
reference are also on the
[documentation site](https://akomyagin.github.io/gitl/docs.html).

License: MIT.
