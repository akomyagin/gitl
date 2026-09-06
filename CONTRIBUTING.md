# Contributing to gitl

Thanks for taking a look — patches, bug reports, and even "this part of the
README confused me" issues are all welcome.

## Before you start

For anything non-trivial (a new flag, a new provider, a behavior change),
open an issue first. This is a solo-maintained project with a fairly
deliberate design (see the architecture notes in the README's "Why" section
and the exit-code/risk-score contract) — a quick conversation up front saves
a rewritten PR later.

Typos, doc fixes, and small bug fixes with a clear reproduction can go
straight to a PR.

## Setup

Requires **Go 1.22+** and `git` in `PATH`.

```bash
git clone https://github.com/akomyagin/gitl.git
cd gitl
go build ./...
go test ./...
```

No API key is needed to build, test, or run `gitl review`/`digest`/`changelog`
— everything falls back to the deterministic offline mode without one.

## Before opening a PR

CI runs exactly this, so it's worth running locally first:

```bash
go build ./...
go vet ./...
go test -race ./...
gofmt -l .   # should print nothing
```

A few conventions specific to this repo:

- Git history is parsed with a **NUL byte** delimiter (`%x00`), never
  `\x1f`/`\x1e` or a plain newline — those are injectable through an
  attacker-controlled commit message. If you touch `internal/gitlog/`, keep
  this invariant.
- New LLM providers go through the single `internal/llm` `Client`, dispatched
  by an explicit `provider` field — not URL-sniffing or auto-detection.
- New config fields need a matching entry in the config validation in
  `internal/config/config.go` and, if user-facing, a line in the README's
  configuration reference.
- Prefer table-driven tests colocated with the code they test
  (`foo.go` / `foo_test.go`).

## Reporting bugs

Include the `gitl` version (`gitl version`), your OS/arch, the exact command,
and — if possible — a minimal repo/commit range that reproduces it. Offline
vs. AI-mode matters: please say which one you were running.

## Security issues

Do **not** open a public issue for a security vulnerability — see
[SECURITY.md](SECURITY.md) for how to report it privately.

## License

By contributing, you agree your contribution is licensed under the
project's [MIT License](LICENSE).
