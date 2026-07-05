// Package render turns a computed artifact into md/text/json output.
//
// Планируемая реализация (Этап 1+, см. docs/TECHNICAL_PLAN.md §6):
// Render(artifact, format) → md | text | json; JSON-вывод версионируется полем
// schema_version как задокументированный контракт. Golden-тесты в testdata/.
package render

// Заглушка Этапа 0: Render() и форматтеры появятся в Этапе 1+.
