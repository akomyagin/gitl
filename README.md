# gitl

**AI-ревьюер git-истории для CLI и CI.** `gitl` (git-log-lens) читает git-историю
репозитория и через LLM превращает её в структурированный инженерный артефакт:

- **`gitl review <range>`** — AI-ревью диапазона/PR с машиночитаемым риск-скорингом
  (`low|medium|high`) для гейтинга в CI (`--fail-on=high` → ненулевой exit code);
- **`gitl changelog [<range>]`** — changelog в стиле Keep a Changelog, группировка
  по conventional-commits (по умолчанию — диапазон от последнего тега до `HEAD`);
- **`gitl digest [--days=N] [--repos=a,b,c]`** — сводка активности по авторам/темам/
  файлам, в т.ч. по **нескольким репозиториям параллельно**.

Чистый CLI-бинарник плюс GitHub Action-обёртка — без сервера, БД и хостинга ключей.
**BYOK** (bring your own key) и мультипровайдерность: OpenAI-совместимый API,
Ollama (локально/self-hosted), Azure OpenAI. Без телеметрии.

> Статус: **Этап 3 — полный набор команд + мульти-репо digest**. Все три команды
> (`review`/`changelog`/`digest`) работают на реальных репозиториях, все три
> формата вывода (`md|text|json`); `changelog`/`digest` полностью детерминированы
> (без LLM), `digest --repos=...` собирает несколько репозиториев параллельно
> и корректно деградирует, если один из них недоступен. GitHub Action и релизный
> инжиниринг — с Этапа 4. Подробности — в [`docs/PLAN.md`](docs/PLAN.md).

## Быстрый старт

Требуется **Go 1.22+** и системный **git** в `PATH`.

```bash
# собрать
go build ./...

# AI-ревью диапазона коммитов (без ключа — детерминированный offline-обзор)
go run ./cmd/gitl review HEAD~5..HEAD

# с ключом — реальный обзор через OpenAI-совместимый API, с риск-скорингом
GITL_API_KEY=sk-... go run ./cmd/gitl review HEAD~5..HEAD

# машиночитаемый вывод для CI + гейтинг по риску
go run ./cmd/gitl review HEAD~5..HEAD --format=json
go run ./cmd/gitl review HEAD~5..HEAD --fail-on=high   # ненулевой exit при высоком риске

# оценка стоимости без реального вызова API
go run ./cmd/gitl review HEAD~5..HEAD --dry-run

# changelog с последнего тега (или вся история, если тегов нет) — без LLM
go run ./cmd/gitl changelog
go run ./cmd/gitl changelog v1.2.0..HEAD --format=json

# сводка активности за последние N дней — без LLM
go run ./cmd/gitl digest --days=14

# мульти-репо digest: собирается параллельно, один недоступный репозиторий
# не валит остальные
go run ./cmd/gitl digest --repos=../service-a,../service-b --format=json

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

### Провайдеры (`llm.provider`)

```yaml
# OpenAI-совместимый API (дефолт)
llm:
  provider: "openai"
  api_key: ""            # или env GITL_API_KEY
  base_url: "https://api.openai.com/v1"
  model: "gpt-4o-mini"

# Ollama — локально/self-hosted, без ключа, бесплатно
llm:
  provider: "ollama"
  base_url: "http://localhost:11434/v1"
  model: "llama3.1"

# Azure OpenAI — свой формат auth/endpoint
llm:
  provider: "azure_openai"
  api_key: ""             # или env GITL_API_KEY
  model: "gpt-4o-mini"    # используется только для оценки стоимости
  azure_openai:
    endpoint: "https://<resource>.openai.azure.com"
    deployment: "<deployment-name>"
    api_version: "2024-08-01-preview"
```

## Документация

- [`docs/PLAN.md`](docs/PLAN.md) — видение, этапы, что за пределами MVP.
- [`docs/TECHNICAL_PLAN.md`](docs/TECHNICAL_PLAN.md) — стек, архитектура,
  детальная разбивка по этапам, риски.
- [`docs/POST_MVP_PLAN.md`](docs/POST_MVP_PLAN.md) — nice-to-have, монетизация,
  Excluded-Never.

## Лицензия

[MIT](LICENSE).
