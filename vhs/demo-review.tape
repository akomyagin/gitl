# gitl demo: offline AI-style review of a commit range.
# Renders site/assets/demo-review.gif. Run from the repo root:
#   PATH="$HOME/go/bin:$PATH" vhs vhs/demo-review.tape
# Prereq: a built `gitl` binary in PATH (go build -o gitl ./cmd/gitl).
#
# The hidden setup isolates the environment (empty HOME/XDG config, no
# GITL_API_KEY) so the demo always runs the deterministic offline review —
# no network, reproducible on any machine — and paces stdout line-by-line
# so the risk header is readable before the output scrolls.

Require gitl
Require git

Output site/assets/demo-review.gif

Set Shell "bash"
Set FontSize 18
Set Width 1200
Set Height 700
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms
Set Padding 16

Hide
Type `mkdir -p /tmp/gitl-demo-home; export XDG_CONFIG_HOME=/tmp/gitl-demo-home HOME=/tmp/gitl-demo-home; unset GITL_API_KEY GITL_QUIET; gitl() { command gitl "$@" | while IFS= read -r line; do printf '%s\n' "$line"; sleep 0.06; done; return "${PIPESTATUS[0]}"; }; clear`
Enter
Show

Sleep 500ms
Type "gitl review HEAD~5..HEAD"
Sleep 500ms
Enter
Sleep 8s
