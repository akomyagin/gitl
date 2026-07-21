#!/usr/bin/env bash
# bitbucket-pipe/pipe.sh — entrypoint of the gitl Bitbucket Pipe image
# (bitbucket-pipe/Dockerfile). Bitbucket counterpart of action.yml (GitHub/
# Gitea) and templates/gitl-review.yml (GitLab): resolve the PR range from
# Bitbucket Pipelines' predefined variables, run `gitl review --format=json`,
# render the sticky comment via the shared platform-neutral ci/comment.sh,
# and create/update the PR comment through the Bitbucket Cloud REST API
# (same "<!-- gitl-review -->" sticky marker as on the other platforms).
#
# Supply-chain note (architectural advantage over the GitLab component):
# ci/comment.sh is executed from /opt/gitl/ci/comment.sh — baked into this
# image at build time from the same source tree as the gitl binary, NOT
# fetched over the network at run time. The GitLab component must curl the
# script from raw.githubusercontent.com with no integrity check (documented
# trade-off in templates/gitl-review.yml); here the whole pipe is one
# versioned, immutable image, so that fetch-and-exec gap does not exist.
#
# Pipe variables (pipe.yml):
#   GITL_API_KEY                 optional — BYOK LLM key; empty = deterministic
#                                offline review (no network call, no cost).
#   FAIL_ON                      optional, default "never" — risk threshold
#                                that fails the step (never|low|medium|high).
#   MAX_COST_USD                 optional, default "0.50" — cost guard.
#   GITL_BITBUCKET_TOKEN         auth option A: access token (repository/
#                                project/workspace access token or Atlassian
#                                API token usable as Bearer), scope
#                                pullrequest:write. Sent as
#                                "Authorization: Bearer <t>".
#   GITL_BITBUCKET_USER +        auth option B: Bitbucket username + app
#   GITL_BITBUCKET_APP_PASSWORD  password (scope pullrequest:write), sent as
#                                Basic auth. One of A/B is required.
#   BITBUCKET_API_URL            optional, default https://api.bitbucket.org/2.0.
#                                Bitbucket Pipes exist on Bitbucket Cloud only,
#                                so this is not a Server/DC switch — it exists
#                                for hermetic testing and API proxies.
#
# Predefined Bitbucket Pipelines variables consumed (per Atlassian's
# "Variables and secrets" documentation — ASSUMPTION, not yet observed in a
# live pipeline; see the README verification note):
#   BITBUCKET_PR_DESTINATION_COMMIT  base of the range (PR pipelines only)
#   BITBUCKET_COMMIT                 head of the range
#   BITBUCKET_PR_ID                  PR number for the comments API
#   BITBUCKET_WORKSPACE / BITBUCKET_REPO_SLUG  repo coordinates for the API
#   BITBUCKET_PR_DESTINATION_BRANCH  fetch fallback for shallow clones
#   BITBUCKET_GIT_HTTP_ORIGIN / BITBUCKET_BUILD_NUMBER  CI run URL (optional)
#
# Secrets discipline (docs/TECHNICAL_PLAN.md §12.5, same as action.yml and
# the GitLab component): tokens/keys live in env only — never hardcoded,
# never echoed, never on a curl argv (auth header goes in via stdin), and
# this script never enables `set -x`.
set -euo pipefail

FAIL_ON="${FAIL_ON:-never}"
MAX_COST_USD="${MAX_COST_USD:-0.50}"
BITBUCKET_API_URL="${BITBUCKET_API_URL:-https://api.bitbucket.org/2.0}"

# ---- fail-fast validation (before any LLM spend) --------------------------

# Range endpoints. BITBUCKET_PR_DESTINATION_COMMIT only exists in
# pull-request pipelines (the `pull-requests:` section of
# bitbucket-pipelines.yml) — anywhere else there is no PR to review/comment
# on, so fail with a clear message instead of a cryptic git error.
if [ -z "${BITBUCKET_PR_DESTINATION_COMMIT:-}" ] || [ -z "${BITBUCKET_COMMIT:-}" ]; then
  echo "gitl: BITBUCKET_PR_DESTINATION_COMMIT and/or BITBUCKET_COMMIT is not set." >&2
  echo "This pipe must run in a pull-request pipeline (the 'pull-requests:' section of bitbucket-pipelines.yml)." >&2
  exit 1
fi

if [ -z "${BITBUCKET_PR_ID:-}" ] || [ -z "${BITBUCKET_WORKSPACE:-}" ] || [ -z "${BITBUCKET_REPO_SLUG:-}" ]; then
  echo "gitl: BITBUCKET_PR_ID/BITBUCKET_WORKSPACE/BITBUCKET_REPO_SLUG is not set — cannot address the PR comments API." >&2
  echo "This pipe must run in a pull-request pipeline on Bitbucket Pipelines." >&2
  exit 1
fi

# Auth for the comments API. Posting the review is the whole point of the
# pipe, so a missing credential is a hard configuration error (same policy
# as the GitLab component), detected BEFORE the review runs so a
# misconfigured pipeline never spends LLM money first.
if [ -n "${GITL_BITBUCKET_TOKEN:-}" ]; then
  AUTH_HEADER="Authorization: Bearer ${GITL_BITBUCKET_TOKEN}"
elif [ -n "${GITL_BITBUCKET_USER:-}" ] && [ -n "${GITL_BITBUCKET_APP_PASSWORD:-}" ]; then
  # base64 without line wrapping (busybox base64 has no -w flag).
  AUTH_HEADER="Authorization: Basic $(printf '%s:%s' "$GITL_BITBUCKET_USER" "$GITL_BITBUCKET_APP_PASSWORD" | base64 | tr -d '\n')"
else
  echo "gitl: no Bitbucket credential configured — set GITL_BITBUCKET_TOKEN (access token, scope pullrequest:write)" >&2
  echo "or GITL_BITBUCKET_USER + GITL_BITBUCKET_APP_PASSWORD (app password, scope pullrequest:write)" >&2
  echo "as secured repository variables, then pass them to the pipe via 'variables:'." >&2
  exit 1
fi

# ---- git setup ------------------------------------------------------------

# The clone is bind-mounted into the pipe container and owned by a different
# uid than the one running here — without safe.directory every git command
# aborts with "detected dubious ownership".
git config --global --add safe.directory "${BITBUCKET_CLONE_DIR:-$PWD}"
git config --global --add safe.directory "$PWD"

# Bitbucket clones with --depth 50 by default, so for longer-lived branches
# the destination commit may be absent locally (same reason the GitHub
# example mandates fetch-depth: 0 and the GitLab component sets GIT_DEPTH:
# "0"). Best effort: fetch the exact SHA, then the destination branch; if
# the base still cannot be resolved, `gitl review` itself fails and the
# fallback-notice path of ci/comment.sh reports it on the PR. The clean fix
# on the consumer side is `clone: depth: full` on the step (see README).
if ! git rev-parse --quiet --verify "${BITBUCKET_PR_DESTINATION_COMMIT}^{commit}" > /dev/null; then
  git fetch --no-tags origin "${BITBUCKET_PR_DESTINATION_COMMIT}" > /dev/null 2>&1 \
    || { [ -n "${BITBUCKET_PR_DESTINATION_BRANCH:-}" ] \
         && git fetch --no-tags origin "${BITBUCKET_PR_DESTINATION_BRANCH}" > /dev/null 2>&1; } \
    || echo "gitl: warning: could not fetch the PR destination commit — the range may not resolve (consider 'clone: depth: full')." >&2
fi

# ---- run the review -------------------------------------------------------

GITL_RANGE="${BITBUCKET_PR_DESTINATION_COMMIT}..${BITBUCKET_COMMIT}"

# Single gitl invocation (one LLM call at most, docs/TECHNICAL_PLAN.md
# §12.3): --format=json carries both the review body (review_markdown) and
# the machine risk level, so the comment and the risk gate share one run.
review_exit=0
gitl review "$GITL_RANGE" \
  --format=json \
  --fail-on="$FAIL_ON" \
  --max-cost-usd="$MAX_COST_USD" \
  > review.json || review_exit=$?

# Render comment.md from review.json (including the fallback notice when
# gitl itself failed) via the shared renderer baked into the image. The
# pipeline-result page is Bitbucket's canonical "link to this run" — the
# generic GITL_CI_RUN_URL of the ci/comment.sh contract; left empty (generic
# "check the CI logs" fallback) if the build number is unavailable.
GITL_CI_RUN_URL=""
if [ -n "${BITBUCKET_BUILD_NUMBER:-}" ]; then
  repo_url="${BITBUCKET_GIT_HTTP_ORIGIN:-https://bitbucket.org/${BITBUCKET_WORKSPACE}/${BITBUCKET_REPO_SLUG}}"
  GITL_CI_RUN_URL="${repo_url}/pipelines/results/${BITBUCKET_BUILD_NUMBER}"
fi

GITL_RANGE="$GITL_RANGE" \
GITL_REVIEW_EXIT="$review_exit" \
GITL_CI_RUN_URL="$GITL_CI_RUN_URL" \
  bash /opt/gitl/ci/comment.sh

# ---- post the sticky PR comment (Bitbucket Cloud REST API) ----------------
# Post BEFORE propagating the risk-gate exit code, so a blocked (high-risk)
# PR still shows the reasoning, not just a failed step (docs/TECHNICAL_PLAN.md
# §12.4.Б — same "show reasoning before failing" principle as
# `gitl review --fail-on` itself).

# The auth header is fed to curl on stdin (-H @-), NOT as an argv element:
# argv is readable by other processes on the runner host, env+stdin are not
# (same discipline as the Gitea branch of action.yml and the GitLab
# component).
bitbucket_api() {
  curl -sSf -H @- -H "Content-Type: application/json" "$@" \
    <<< "$AUTH_HEADER"
}

COMMENTS_URL="${BITBUCKET_API_URL}/repositories/${BITBUCKET_WORKSPACE}/${BITBUCKET_REPO_SLUG}/pullrequests/${BITBUCKET_PR_ID}/comments"

# Sticky comment: find a previous gitl comment by the marker that
# ci/comment.sh always puts on the first line, and edit it in place so
# force-pushes don't spam the PR thread. The comments API is paginated
# ({values, next}); one 100-item page is plenty for the sticky marker on
# real-world PR threads (same trade-off as the GitHub/Gitea/GitLab paths).
# Matching runs on content.raw — Bitbucket stores the raw markdown verbatim
# there even though the rendered view strips the HTML comment. Deleted
# comments stay in the listing with deleted:true, so they are filtered out
# (updating a deleted comment would 4xx).
if ! COMMENTS_JSON=$(bitbucket_api "${COMMENTS_URL}?pagelen=100"); then
  echo "ERROR: could not list PR comments via ${COMMENTS_URL}." >&2
  echo "Check that the configured credential is valid and has the pullrequest:write scope on this repository." >&2
  exit 1
fi
EXISTING_ID=$(jq -r \
  '[.values[] | select((.deleted != true) and (.content.raw | startswith("<!-- gitl-review -->")))] | last | .id // empty' \
  <<< "$COMMENTS_JSON")

# JSON-encode the markdown body with jq (raw slurp) — no hand-rolled
# escaping of quotes/newlines in the review text. Bitbucket's comment shape
# is {content: {raw: <markdown>}}.
jq -Rs '{content: {raw: .}}' comment.md > comment.json

post_ok=true
if [ -n "$EXISTING_ID" ]; then
  bitbucket_api -X PUT --data @comment.json \
    "${COMMENTS_URL}/${EXISTING_ID}" > /dev/null || post_ok=false
else
  bitbucket_api -X POST --data @comment.json \
    "${COMMENTS_URL}" > /dev/null || post_ok=false
fi
if [ "$post_ok" != true ]; then
  echo "ERROR: failed to post the PR comment to ${COMMENTS_URL}." >&2
  echo "Check that the configured credential is valid and has the pullrequest:write scope on this repository." >&2
  exit 1
fi

exit "$review_exit"
