# Security

## Reporting a vulnerability

Please **don't open a public issue** for a security vulnerability. Use
[GitHub's private vulnerability reporting](https://github.com/akomyagin/gitl/security/advisories/new)
for this repository (Security tab → "Report a vulnerability"). You'll get a
private thread to discuss the issue and coordinate a fix before it's public.

If that's not workable for some reason, open an issue asking for another
contact method — just don't include exploit details in it.

## Supported versions

This is a solo-maintained project without a long-term support policy — only
the **latest release** gets fixes. Please upgrade before reporting; a fix
for an old tag is unlikely.

## What's actually in gitl's threat model

Worth knowing before you audit or report:

- **Your API key never leaves your machine except to the provider you
  configured.** gitl is BYOK — there's no gitl-operated server, no hosted
  key storage, and no telemetry. If you find a code path that sends
  anything to an endpoint other than the configured provider, that's a
  real bug, report it.
- **Git log parsing uses a NUL byte (`%x00`) delimiter**, specifically
  because the alternatives (`\x1f`/`\x1e`, or splitting on `\n`) are
  reachable through an attacker-controlled commit message (`git commit -F`)
  and can corrupt or crash the parser. NUL is the one byte `git fsck`
  guarantees can't appear in a commit. If you can get a crafted commit
  message to break `internal/gitlog`'s parser, that's a real finding.
- **Release binaries are signed and attested**, not just built and
  uploaded: keyless [cosign](https://github.com/sigstore/cosign) signatures
  plus [SLSA level 3](https://slsa.dev/) build provenance. See
  [VERIFY.md](VERIFY.md) for the exact commands to verify a download before
  running it — if verification fails for a release that isn't yours,
  that's worth reporting immediately.
- **Offline mode makes no network calls.** If `gitl review`/`digest`/`changelog`
  reach the network without an API key configured, that's a bug.

## Dependencies

Dependabot security updates and secret scanning (with push protection) are
enabled on this repository.
