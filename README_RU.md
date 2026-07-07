# gitl

[![Action self-test](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml/badge.svg)](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml)

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

> Статус: MVP завершён. Все три команды (`review`/`changelog`/`digest`) работают
> на реальных репозиториях, все три формата вывода (`md|text|json`); ниже —
> готовый Action, оставляющий AI-ревью sticky-комментарием к PR и гейтящий по
> риск-скорингу. `v0.2.0` выпущен с кросс-компилированными, подписанными cosign
> бинарями (верификация — в [VERIFY.md](VERIFY.md)). Публикация в Marketplace —
> оставшийся ручной шаг.

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

Установка:

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

Два уровня, сливаются по приоритету:
**флаг > env > `.gitl.yaml` (репо) > `~/.config/gitl/config.yaml` (личный)**.
Repo-level `.gitl.yaml` коммитится в репозиторий как общая политика команды
(порог риска, исключённые пути, категории changelog). Без ключа `gitl` работает
в детерминированном offline-режиме.

В offline-режиме — а также когда реальная модель не вернула валидный risk-блок и
`gitl` падает назад на эвристику — risk-шапка помечается суффиксом `*(heuristic)*`
(и `"heuristic": true` в `--format=json`), чтобы детерминированную оценку нельзя
было принять за собственное суждение модели.

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

## GitHub Action

`gitl` можно подключить как GitHub Action: он AI-ревьюит коммиты пул-реквеста
и оставляет комментарий с риск-скорингом, опционально блокируя мерж по порогу
риска. Action собирает `gitl` из исходников (`go install` на пиннутой версии).

Добавьте в свой репозиторий `.github/workflows/gitl-review.yml`:

```yaml
name: gitl review
on:
  pull_request:

permissions:
  contents: read          # для checkout
  pull-requests: write    # чтобы Action мог оставить комментарий-ревью

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0    # обязательно: без полной истории base..head не резолвится

      - uses: akomyagin/gitl@v0.2.0
        with:
          gitl-api-key: ${{ secrets.GITL_API_KEY }}   # BYOK, см. ниже
          fail-on: high                               # опционально: блокировать мерж при высоком риске
```

Безопасное использование в CI:

- **Ключ — только через `secrets.*`.** `gitl-api-key` передаётся из
  `secrets.GITL_API_KEY` (создаётся в Settings → Secrets and variables →
  Actions вашего репозитория), никогда не хардкодится в YAML и не коммитится.
  Если секрет не задан — Action работает в детерминированном **offline-
  режиме** (без сети и без стоимости), а не падает.
- **Минимальные `permissions:`.** Нужны только `pull-requests: write`
  (постинг комментария) и `contents: read` (checkout) — не выдавайте Action'у
  более широкие права.
- **`fetch-depth: 0` обязателен.** GitHub даёт Action'у события `pull_request`
  с `base`/`head` SHA, но не готовый диапазон коммитов; `actions/checkout` по
  умолчанию делает shallow-клон, при котором `base.sha..head.sha` не
  разрешится. Нужна полная история.
- **`fail-on` по умолчанию — `never`.** Action только комментирует, не
  блокирует мерж, пока вы явно не включите гейт (`fail-on: high` и т.п.) —
  тот же принцип «WARN по умолчанию, hard gate — явный opt-in», что и в CLI
  (`--fail-on`).
- **Приватность диффов.** В CI дифф уходит тому LLM-провайдеру, что указан
  в конфиге (по умолчанию — OpenAI-совместимый API). Для закрытого кода
  используйте self-hosted/enterprise-провайдер (Ollama, Azure OpenAI) — см.
  «Провайдеры» выше.
- **Маскировка секретов.** GitHub автоматически маскирует значения
  `secrets.*` в логах runner'а как `***`, но это не повод печатать ключ
  в собственных шагах workflow.

## Лицензия

[MIT](LICENSE).
