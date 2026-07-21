# syntax=docker/dockerfile:1
# bitbucket-pipe/Dockerfile — image for the gitl Bitbucket Pipe.
#
# Bitbucket Pipes are Docker images by definition (unlike the GitHub/Gitea
# composite action and the GitLab CI/CD component, which are plain YAML), so
# this is the first place in the project where gitl itself is containerized —
# docker-compose.yml remains a dev-only Ollama dependency, unrelated to this.
#
# Build from the REPOSITORY ROOT (the build needs the Go sources and
# ci/comment.sh, both outside this directory):
#
#     docker build -f bitbucket-pipe/Dockerfile -t alkom68/gitl-review-pipe:dev .
#
# Supply-chain note (deliberate architectural difference from the GitLab
# component): everything the pipe executes — the gitl binary, ci/comment.sh
# and pipe.sh — is baked into this image at build time from ONE source tree
# and versioned together under the image tag. Nothing is fetched over the
# network at run time (the GitLab component has to curl ci/comment.sh from
# raw.githubusercontent.com with no integrity check, a known trade-off noted
# in templates/gitl-review.yml; the pipe closes that gap by construction).

# Keep in sync with go.mod (`go 1.22`) — same version the GitHub action pins
# via actions/setup-go and the GitLab component pins via `image: golang:1.22`.
ARG GO_VERSION=1.22

FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src

# Cache the module graph independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

# Build provenance, same -X vars goreleaser injects (.goreleaser.yaml) so
# `gitl version` inside the image reports the release it was built from.
ARG GITL_VERSION=dev
ARG GITL_COMMIT=unknown
ARG GITL_DATE=unknown

# Static binary: CGO_ENABLED=0 means no libc dependency, so the final stage
# is free to be any minimal base.
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X github.com/akomyagin/gitl/internal/cli.version=${GITL_VERSION} \
        -X github.com/akomyagin/gitl/internal/cli.commit=${GITL_COMMIT} \
        -X github.com/akomyagin/gitl/internal/cli.date=${GITL_DATE}" \
      -o /out/gitl ./cmd/gitl

# Final stage: alpine, NOT distroless — a deliberate choice, reasoning:
#   * gitl shells out to the system `git` binary by design (os/exec, not
#     go-git — docs/TECHNICAL_PLAN.md §4), and pipe.sh + the shared
#     ci/comment.sh need bash, curl and jq. Distroless ships none of these;
#     providing statically-built git/bash/curl/jq on top of distroless means
#     hand-maintaining four userland toolchains, which defeats distroless's
#     simplicity and would concentrate supply-chain risk in exactly the
#     hand-rolled parts.
#   * The attack-surface argument for distroless is weakest here: the image
#     runs short-lived, non-network-listening CI jobs on Bitbucket's
#     ephemeral runners, and alpine's apk-managed packages still receive CVE
#     patches (unlike hand-built static tools, which would not).
#   * Size stays small anyway: alpine + git + curl + jq + bash is ~30 MB of
#     layers on top of a ~8 MB base.
FROM alpine:3.20

# ca-certificates: TLS trust for curl → api.bitbucket.org (and for gitl's
# own LLM calls when GITL_API_KEY is set).
RUN apk add --no-cache bash git curl jq ca-certificates

COPY --from=builder /out/gitl /usr/local/bin/gitl

# The shared platform-neutral comment renderer is copied from the SAME
# source tree as the binary above — see the supply-chain note in the header.
COPY ci/comment.sh /opt/gitl/ci/comment.sh
COPY bitbucket-pipe/pipe.sh /opt/gitl/pipe.sh
RUN chmod 0755 /opt/gitl/pipe.sh /opt/gitl/ci/comment.sh

# Bitbucket Pipelines runs the pipe container with the repository clone
# mounted and the working directory set to $BITBUCKET_CLONE_DIR, then invokes
# the image's default entrypoint — which is pipe.sh.
ENTRYPOINT ["/opt/gitl/pipe.sh"]
