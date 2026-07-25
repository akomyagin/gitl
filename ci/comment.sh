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
#   GITL_EMIT_SUMMARY (optional) when set to "1", ALSO write a compact
#                     PR-description summary fragment (risk line + link back
#                     to the full review comment, no review body duplication)
#                     to $GITL_SUMMARY_FILE, in addition to — never instead
#                     of — the regular comment file. Any other value
#                     (including unset/empty) leaves the output of this
#                     script byte-identical to the pre-summary behaviour.
#   GITL_SUMMARY_FILE (optional, default "pr-summary.md") summary fragment
#                     output file path; only used when GITL_EMIT_SUMMARY=1.
#
# Output: writes $GITL_COMMENT_FILE. Its first line is always the sticky
# marker "<!-- gitl-review -->", which platform wrappers use to find and
# edit an existing comment instead of posting a new one. With
# GITL_EMIT_SUMMARY=1 it additionally writes $GITL_SUMMARY_FILE, wrapped in
# the separate "<!-- gitl-review-summary -->" ... "<!-- /gitl-review-summary -->"
# marker pair (distinct from the comment marker so the two never get
# confused); platform wrappers merge that fragment into the PR description
# via ci/pr-summary-merge.sh. Never prints secrets and takes none; no
# `set -x` (docs/TECHNICAL_PLAN.md §12.5).
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
    # Separator only when the summary is non-empty, mirroring the Go renderer
    # (internal/render/render.go riskHeader): ParseRisk accepts an empty
    # summary, and "**Risk:** HIGH — " with a dangling dash looks broken. The
    # field itself is always present in the artifact (no omitempty on
    # jsonRisk.Summary), so a plain != "" check is enough.
    jq -r '"**Risk:** " + (.risk.level | ascii_upcase) + (if .risk.summary != "" then " — " + .risk.summary else "" end) + (if .risk.heuristic then " *(heuristic)*" else "" end)' "$review_json"
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

# Optional PR-description summary fragment. Deliberately a separate block
# AFTER the comment rendering above (re-running the cheap jq validity check)
# rather than woven into it: the default path must stay byte-identical for
# the four existing platform adapters that call this script without
# GITL_EMIT_SUMMARY. The fragment is host-agnostic markdown — future
# Gitea/GitLab/Bitbucket description adapters reuse it as-is; only the
# merge-and-PATCH step is platform-specific.
if [ "${GITL_EMIT_SUMMARY:-}" = "1" ]; then
  summary_file="${GITL_SUMMARY_FILE:-pr-summary.md}"
  {
    echo "<!-- gitl-review-summary -->"
    if jq -e . "$review_json" > /dev/null 2>&1; then
      # Same non-empty summary guard and *(heuristic)* style as the comment
      # risk line above; only the prefix differs ("gitl risk:" — the PR
      # description has no surrounding gitl heading to give "Risk:" context).
      jq -r '"**gitl risk: " + (.risk.level | ascii_upcase) + "**" + (if .risk.summary != "" then " — " + .risk.summary else "" end) + (if .risk.heuristic then " *(heuristic)*" else "" end)' "$review_json"
    else
      # Mirror of the comment fallback: gitl itself failed, but the
      # description block still gets refreshed so it never shows a stale
      # risk level from a previous run.
      echo "**gitl risk: unavailable** — gitl exited with an error (exit \`${review_exit}\`); see the review comment / CI logs."
    fi
    echo
    echo "<sub>Automated by [gitl](https://github.com/akomyagin/gitl) · see the full review comment on this PR.</sub>"
    echo "<!-- /gitl-review-summary -->"
  } > "$summary_file"
fi
