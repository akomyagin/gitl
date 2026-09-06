# gitl demo: the interactive digest TUI (bubbletea viewer).
# Renders site/assets/demo-digest-tui.gif. Run from the repo root:
#   PATH="$HOME/go/bin:$PATH" vhs vhs/demo-digest-tui.tape
# Prereq: a built `gitl` binary in PATH (go build -o gitl ./cmd/gitl).
#
# The hidden setup isolates the environment (empty HOME/XDG config, no
# GITL_API_KEY) so the run is reproducible. Keys are the viewer's real
# keymap (internal/tui/model.go): j/k or arrows scroll, PgUp/PgDn page,
# q quits. VHS provides a real PTY, so the TUI renders normally.

Require gitl
Require git

Output site/assets/demo-digest-tui.gif

Set Shell "bash"
Set FontSize 18
Set Width 1200
Set Height 700
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms
Set Padding 16

Hide
Type `mkdir -p /tmp/gitl-demo-home; export XDG_CONFIG_HOME=/tmp/gitl-demo-home HOME=/tmp/gitl-demo-home; unset GITL_API_KEY GITL_QUIET; clear`
Enter
Show

Sleep 500ms
Type "gitl digest --days=30 --tui"
Sleep 500ms
Enter
Sleep 3s
Type "j"
Sleep 600ms
Type "j"
Sleep 600ms
Type "j"
Sleep 600ms
Type "j"
Sleep 600ms
Type "j"
Sleep 600ms
Type "j"
Sleep 1500ms
Type "k"
Sleep 600ms
Type "k"
Sleep 1500ms
Type "q"
Sleep 1s
