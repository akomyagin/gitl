# gitl demo: the --fail-on risk gate turning a high-risk range into exit code 2.
# Renders site/assets/demo-gate.gif. Run from the repo root:
#   PATH="$HOME/go/bin:$PATH" vhs vhs/demo-gate.tape
# Prereq: a built `gitl` binary in PATH (go build -o gitl ./cmd/gitl).
#
# The hidden setup isolates the environment (empty HOME/XDG config, no
# GITL_API_KEY) so the demo always runs the deterministic offline review,
# and paces stdout line-by-line for readability. The range HEAD~1..HEAD is
# high-risk on this repo's real history (large diff); if history changes,
# pick any range with >300 changed lines or a sensitive keyword
# (auth/secret/security/...) in a commit — verify with:
#   gitl review <range> --format=json --quiet | grep level

Require gitl
Require git

Output site/assets/demo-gate.gif

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
Type "gitl review HEAD~1..HEAD --fail-on=high"
Sleep 500ms
Enter
Sleep 5s
Type `echo "exit code: $?"`
Sleep 500ms
Enter
Sleep 3s
