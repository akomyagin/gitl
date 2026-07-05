// Package llm is a hand-written net/http client to an OpenAI-compatible API
// (intentionally without an SDK), plus a deterministic offline provider.
//
// Планируемая структура (реализация — Этап 1+, см. docs/TECHNICAL_PLAN.md §6):
//   - client.go:  net/http, таймауты, context-отмена, retry с backoff+jitter,
//                 различение ретраебельных (429/5xx) и фатальных (401/400) ошибок;
//                 мультипровайдерность через ProviderConfig (openai/ollama/azure_openai)
//   - stream.go:  разбор SSE-стрима (data: ... [DONE]) — post-MVP
//   - offline.go: детерминированный offline-провайдер (без сети), тот же интерфейс
package llm

// Заглушка Этапа 0: Client, ProviderConfig и offline-провайдер появятся в Этапе 1+.
