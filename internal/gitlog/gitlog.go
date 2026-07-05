// Package gitlog reads and parses git history via the system `git` binary
// (os/exec), hidden behind an interface so a go-git backend could replace it
// without touching commands (see docs/TECHNICAL_PLAN.md §4).
//
// Планируемая структура (реализация — Этап 1+):
//   - runner.go: обёртка над exec.CommandContext("git", ...)
//   - parser.go: парсинг --pretty=format:%H%x1f...%x1e --name-status
//     (control-разделители %x1f/%x1e, НЕ split по \n)
//   - types.go:  Commit, FileChange, Range, DiffStat
package gitlog

// Заглушка Этапа 0: Runner, парсер и типы появятся в Этапе 1+.
