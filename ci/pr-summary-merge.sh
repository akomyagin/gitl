#!/usr/bin/env bash
# ci/pr-summary-merge.sh — merge a gitl risk-summary fragment into a PR
# description body. Platform-neutral: the caller fetches the current
# description through its own API (GitHub `gh api`, future Gitea/GitLab/
# Bitbucket adapters through theirs), pipes it through this script together
# with the fragment rendered by ci/comment.sh (GITL_EMIT_SUMMARY=1), and
# PATCHes the merged result back. Only the block between the
# "<!-- gitl-review-summary -->" / "<!-- /gitl-review-summary -->" marker
# pair is ever replaced — human-written text outside the markers is
# preserved byte-for-byte.
#
# Usage:
#   bash ci/pr-summary-merge.sh <current-body-file> <fragment-file>
#
# Prints the merged body to stdout. Merge rules:
#   - First line exactly equal to the open marker, then the first line after
#     it exactly equal to the close marker, form "our" block: it is replaced
#     in place with the fragment. Only the FIRST such pair is touched; any
#     later pairs are left alone.
#   - No such pair (first run) → the fragment is appended to the end,
#     separated from a non-empty body by one blank line (an empty body
#     becomes just the fragment).
#   - Malformed markers (open with no close after it, or a stray close with
#     no open before it) → treated as a damaged block: replacing in place
#     would risk deleting arbitrary human text, so the existing body is left
#     untouched and a fresh fragment is appended instead. The next run then
#     finds that well-formed block and replaces exactly it.
#   - Idempotent: re-running with the same fragment yields byte-identical
#     output once a well-formed block exists.
set -euo pipefail

if [ "$#" -ne 2 ] || [ ! -f "$1" ] || [ ! -f "$2" ]; then
  echo "usage: $0 <current-body-file> <fragment-file> (both must exist)" >&2
  exit 2
fi
body_file="$1"
fragment_file="$2"

# POSIX awk only (no gawk extensions). The fragment file is read first
# (NR==FNR), then the body; all merge logic runs in END so the marker scan
# can look ahead. Marker comparison strips one trailing CR: PR bodies edited
# through the GitHub web UI come back with CRLF line endings, and a literal
# equality test would then miss our own block and append a duplicate — the
# CR is still preserved in the output for all non-replaced lines.
awk '
  function marker_eq(line, marker) {
    sub(/\r$/, "", line)
    return line == marker
  }
  NR == FNR { frag[++nfrag] = $0; next }
  { body[++nbody] = $0 }
  END {
    OPEN = "<!-- gitl-review-summary -->"
    CLOSE = "<!-- /gitl-review-summary -->"
    open_at = 0
    close_at = 0
    for (i = 1; i <= nbody; i++) {
      if (marker_eq(body[i], OPEN)) { open_at = i; break }
    }
    if (open_at) {
      for (i = open_at + 1; i <= nbody; i++) {
        if (marker_eq(body[i], CLOSE)) { close_at = i; break }
      }
    }
    if (open_at && close_at) {
      # Well-formed first pair: replace the whole block (markers included),
      # keep everything before and after untouched.
      for (i = 1; i < open_at; i++) print body[i]
      for (i = 1; i <= nfrag; i++) print frag[i]
      for (i = close_at + 1; i <= nbody; i++) print body[i]
    } else {
      # No pair (first run) or damaged markers: never edit in place —
      # append a fresh block, blank-line-separated from a non-empty body.
      for (i = 1; i <= nbody; i++) print body[i]
      if (nbody > 0) print ""
      for (i = 1; i <= nfrag; i++) print frag[i]
    }
  }
' "$fragment_file" "$body_file"
