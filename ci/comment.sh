#!/usr/bin/env bash
# ci/comment.sh — render the sticky review comment body (comment.md) from a
# `gitl review --format=json` output file. Platform-neutral: this is the part
# of the CI integration that is identical across GitHub Actions, Gitea
# Actions, GitLab CI and Bitbucket Pipes. Each platform wrapper is expected
# to (1) resolve the base..head range from its own event payload, (2) run
# `gitl review "$GITL_RANGE" --format=json ... > review.json` itself, then
# (3) call this script to build comment.md, and (4) post comment.md through
# its own comments API (that last step is the only platform-specific part).
#
# Contract (environment variables):
#   GITL_RANGE        (required) git range under review, e.g. "abc123..def456".
#                     Shown verbatim in the comment heading.
#   GITL_REVIEW_EXIT  (optional, default 0) exit code of the `gitl review`
#                     invocation; shown in the fallback notice when
#                     review.json is not valid JSON.
#   GITL_CI_RUN_URL   (optional) URL of the CI run/job to link from the
#                     fallback notice. Deliberately a single opaque URL — the
#                     GitHub wrapper builds it from GITHUB_SERVER_URL/
#                     GITHUB_REPOSITORY/GITHUB_RUN_ID, a GitLab wrapper would
#                     pass CI_JOB_URL, etc. If unset/empty, the fallback
#                     notice points at the CI logs generically instead.
#   GITL_REVIEW_JSON  (optional, default "review.json") input file path.
#   GITL_COMMENT_FILE (optional, default "comment.md") output file path.
#
# Output: writes $GITL_COMMENT_FILE. Its first line is always the sticky
# marker "<!-- gitl-review -->", which platform wrappers use to find and
# edit an existing comment instead of posting a new one. Never prints
# secrets and takes none; no `set -x` (docs/TECHNICAL_PLAN.md §12.5).
set -euo pipefail

: "${GITL_RANGE:?GITL_RANGE is required (base..head range under review)}"
review_json="${GITL_REVIEW_JSON:-review.json}"
comment_file="${GITL_COMMENT_FILE:-comment.md}"
review_exit="${GITL_REVIEW_EXIT:-0}"

# If review.json is valid JSON, render the full review. Otherwise gitl itself
# failed (bad range, provider error, cost-guard) — write a fallback notice so
# the PR isn't left silent, satisfying §12.4.Б's "show reasoning before
# failing" principle even on the tool-error path (not only the risk-gate path).
if jq -e . "$review_json" > /dev/null 2>&1; then
  {
    echo "<!-- gitl-review -->"
    echo "## gitl review \`$GITL_RANGE\`"
    echo
    jq -r '"**Risk:** " + (.risk.level | ascii_upcase) + " — " + .risk.summary + (if .risk.heuristic then " *(heuristic)*" else "" end)' "$review_json"
    echo
    jq -r '.review_markdown' "$review_json"
  } > "$comment_file"
else
  if [ -n "${GITL_CI_RUN_URL:-}" ]; then
    run_ref="Check the [workflow run](${GITL_CI_RUN_URL}) for details."
  else
    run_ref="Check the CI job logs for details."
  fi
  {
    echo "<!-- gitl-review -->"
    echo "## gitl review \`$GITL_RANGE\`"
    echo
    echo ":warning: **gitl exited with an error (exit \`${review_exit}\`)** — no review output was produced. ${run_ref}"
  } > "$comment_file"
fi
