# Verifying gitl release artifacts

`gitl` release binaries are signed with [cosign](https://github.com/sigstore/cosign)
using **keyless** signing backed by [Sigstore](https://www.sigstore.dev/). Verifying
before you run a downloaded binary protects you against supply-chain tampering: it
proves each artifact was produced by this repository's release workflow and has not
been modified in transit.

There are **no long-lived signing keys**. Instead, each release carries:

- `checksums.txt` — SHA-256 checksums of every released binary/archive.
- `checksums.txt.sig` — the cosign signature over `checksums.txt`.
- `checksums.txt.pem` — the ephemeral [Fulcio](https://docs.sigstore.dev/certificate_authority/overview/)
  certificate that was used for that one signing operation. It ties the signature to
  the GitHub Actions identity that produced the release and then expires.

Replace `<version>` below with the release tag you downloaded (for example `v0.1.0`).

## 1. Verify the signature of `checksums.txt`

```bash
cosign verify-blob \
  --certificate https://github.com/akomyagin/gitl/releases/download/<version>/checksums.txt.pem \
  --signature https://github.com/akomyagin/gitl/releases/download/<version>/checksums.txt.sig \
  --certificate-identity-regexp "^https://github.com/akomyagin/gitl/" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  https://github.com/akomyagin/gitl/releases/download/<version>/checksums.txt
```

What the flags mean:

- `--certificate` / `--signature` — the ephemeral Fulcio certificate and the cosign
  signature for `checksums.txt`.
- `--certificate-identity-regexp` — requires the signing identity (the workflow that
  ran) to belong to `github.com/akomyagin/gitl`, so a signature from any other repo is
  rejected.
- `--certificate-oidc-issuer` — requires the OIDC token to have been issued by GitHub
  Actions, not some other provider.

A successful run prints `Verified OK`.

## 2. Verify the binary checksums

Once `checksums.txt` itself is trusted, verify the binaries/archives you downloaded
against it. Put the downloaded artifacts and `checksums.txt` in the same directory,
then run:

```bash
sha256sum -c checksums.txt
```

Each line prints `OK` for a matching file. `sha256sum` only checks the files present
in the current directory, so entries for artifacts you did not download are reported
as missing — that is expected. If any file prints `FAILED`, do **not** use it.

On macOS, `sha256sum` may not be installed; use `shasum -a 256 -c checksums.txt`
instead, or install GNU coreutils.

## 3. Verify SLSA L3 provenance

Starting with `v0.3.0`, each release also ships a SLSA Level 3 provenance
attestation (`*.intoto.jsonl`) alongside the cosign signature. It proves the
binary was produced by this repository's release workflow under SLSA L3
guarantees (isolated build, non-forgeable provenance).

Install [`slsa-verifier`](https://github.com/slsa-framework/slsa-verifier/releases)
and run:

```bash
slsa-verifier verify-artifact gitl_<ver>_linux_amd64.tar.gz \
  --provenance-path gitl-v<ver>.intoto.jsonl \
  --source-uri github.com/akomyagin/gitl \
  --source-tag v<ver>
```

Replace `<ver>` with the release tag (e.g. `v0.3.0`) and the platform suffix with
the one you downloaded. The provenance file (`gitl-v<ver>.intoto.jsonl`) is a single
attestation covering all release artifacts — download it once alongside any binary you
want to verify. A successful run prints `PASSED: SLSA verification passed`.

Both cosign (sections 1–2) and SLSA provenance (section 3) are independent; you
can use either or both to verify a release.
