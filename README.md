# gitl

**AI-ревьюер git-истории для CLI и CI.** `gitl` (git-log-lens) читает git-историю
репозитория и через LLM превращает её в структурированный инженерный артефакт:

- **`gitl review <range>`** — AI-ревью диапазона/PR с машиночитаемым риск-скорингом
  (`low|medium|high`) для гейтинга в CI (`--fail-on=high` → ненулевой exit code);
- **`gitl changelog --since=<ref>`** — changelog в стиле Keep a Changelog;
- **`gitl digest --days=N`** — сводка активности, в т.ч. по **нескольким репозиториям**.

Чистый CLI-бинарник плюс GitHub Action-обёртка — без сервера, БД и хостинга ключей.
**BYOK** (bring your own key) и мультипровайдерность: OpenAI-совместимый API,
Ollama (локально/self-hosted), Azure OpenAI. Без телеметрии.

> Статус: **Этап 0 — bootstrap**. Каркас и документация готовы; реализация команд
> начинается с Этапа 1. Подробности — в [`docs/PLAN.md`](docs/PLAN.md).

## Быстрый старт

Требуется **Go 1.22+** и системный **git** в `PATH`.

```bash
# собрать
go build ./...

# запустить (пока печатает version/usage — реализация команд с Этапа 1)
go run ./cmd/gitl version
go run ./cmd/gitl --help

# тесты
go test ./...
```

Установка (после первого релиза):

```bash
go install github.com/akomyagin/gitl/cmd/gitl@latest
```

### Локальный тест мультипровайдерности (Ollama)

`docker-compose.yml` поднимает **только dev-зависимость** — локальный Ollama
для проверки мультипровайдерного LLM-клиента (сам `gitl` в контейнер не оборачивается):

```bash
docker compose up ollama
```

## Конфигурация (кратко)

Два уровня, сливаются по приоритету
**флаг > env > `.gitl.yaml` (репо) > `~/.config/gitl/config.yaml` (личный)**.
Repo-level `.gitl.yaml` коммитится в репозиторий как общая политика команды
(порог риска, исключённые пути, категории changelog). Без ключа `gitl` работает
в детерминированном offline-режиме. Полная схема — в
[`docs/TECHNICAL_PLAN.md`](docs/TECHNICAL_PLAN.md) §5.

## Документация

- [`docs/PLAN.md`](docs/PLAN.md) — видение, этапы, что за пределами MVP.
- [`docs/TECHNICAL_PLAN.md`](docs/TECHNICAL_PLAN.md) — стек, архитектура,
  детальная разбивка по этапам, риски.
- [`docs/POST_MVP_PLAN.md`](docs/POST_MVP_PLAN.md) — nice-to-have, монетизация,
  Excluded-Never.

## Лицензия

[MIT](LICENSE).
