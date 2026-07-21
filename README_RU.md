# gitl

[![Action self-test](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml/badge.svg)](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml)

**AI-ревьюер git-истории для CLI и CI.** `gitl` (git-log-lens) читает git-историю
репозитория и через LLM превращает её в структурированный инженерный артефакт:

- **`gitl review <range>`** — AI-ревью диапазона/PR с машиночитаемым риск-скорингом
  (`low|medium|high`) для гейтинга в CI (`--fail-on=high` → ненулевой exit code);
  стримит токены в терминал в реальном времени; дисковый кэш LLM-ответов;
  кастомные системные шаблоны промптов; `--staged` — ревью staged (ещё не
  закоммиченных) изменений перед `git commit`.
- **`gitl changelog [<range>]`** — changelog в стиле Keep a Changelog, группировка
  по conventional-commits (по умолчанию — диапазон от последнего тега до `HEAD`);
  детерминированный по умолчанию, `--ai` опционально переписывает его моделью
  в читаемую прозу release notes;
- **`gitl digest [--days=N] [--repos=a,b,c]`** — сводка активности по авторам/темам/
  файлам, в т.ч. по **нескольким репозиториям параллельно**; интерактивный TUI (`--tui`).

Чистый CLI-бинарник плюс GitHub Action-обёртка — без сервера, БД и хостинга ключей.
**BYOK** (bring your own key) и мультипровайдерность: OpenAI-совместимый API,
Ollama (локально/self-hosted), Azure OpenAI, нативный Anthropic (Claude),
Google Gemini. Без телеметрии.

> **Статус:** выпущен `v0.4.3` — все три команды работают на реальных репозиториях,
> все три формата вывода (`md|text|json`); Action оставляет AI-ревью sticky-комментарием
> к PR и гейтит по риск-скорингу. Релизные бинари кросс-компилированы, подписаны cosign
> и покрыты SLSA L3 provenance (верификация — в [VERIFY.md](VERIFY.md)).

## Быстрый старт

Требуется **Go 1.22+** и системный **git** в `PATH`.

```bash
# собрать
go build ./...

# AI-ревью диапазона коммитов — токены стримятся в терминал в реальном времени
GITL_API_KEY=sk-... go run ./cmd/gitl review HEAD~5..HEAD

# без ключа — детерминированный offline-обзор (эвристика, без сети)
go run ./cmd/gitl review HEAD~5..HEAD

# ревью staged (ещё не закоммиченных) изменений перед `git commit`
go run ./cmd/gitl review --staged

# ревью GitHub PR по номеру — требуется `gh` CLI (установлен + залогинен);
# base/head резолвятся через gh, при необходимости делается локальный fetch
# `pull/N/head`, ревьюится diff от merge-base (base...head) — как показывает GitHub
go run ./cmd/gitl review pr/42

# машиночитаемый вывод для CI + гейтинг по риску
go run ./cmd/gitl review HEAD~5..HEAD --format=json
go run ./cmd/gitl review HEAD~5..HEAD --fail-on=high   # ненулевой exit при высоком риске

# оценка стоимости без реального вызова API
go run ./cmd/gitl review HEAD~5..HEAD --dry-run

# кастомный системный промпт задаётся только через конфиг
# (prompt.system_template_file), CLI-флага --system-template нет —
# см. Конфигурация → Кастомные шаблоны ниже

# пропустить дисковый кэш LLM-ответов (всегда вызывать модель)
go run ./cmd/gitl review HEAD~5..HEAD --no-cache

# отключить стриминг (буферизованный вывод)
go run ./cmd/gitl review HEAD~5..HEAD --no-stream

# changelog с последнего тега (или вся история, если тегов нет) — без LLM по умолчанию
go run ./cmd/gitl changelog
go run ./cmd/gitl changelog v1.2.0..HEAD --format=json

# AI-changelog: модель переписывает сгруппированный результат в прозу release notes
# и переносит значимые non-conventional коммиты из "Other" в подходящие категории.
# Без API-ключа (или при некорректном ответе модели) — откат к детерминированному
# changelog с предупреждением, команда никогда не падает. --dry-run/--max-cost-usd/
# --no-cache работают так же, как у review.
GITL_API_KEY=sk-... go run ./cmd/gitl changelog --ai

# сводка активности за последние N дней — без LLM
go run ./cmd/gitl digest --days=14

# мульти-репо digest: собирается параллельно, один недоступный репозиторий
# не валит остальные
go run ./cmd/gitl digest --repos=../service-a,../service-b --format=json

# интерактивный TUI-просмотрщик дайджеста (требует TTY)
go run ./cmd/gitl digest --days=14 --tui

go run ./cmd/gitl version
go run ./cmd/gitl --help

# тесты
go test ./...
```

Установка:

```bash
# Go-тулчейн
go install github.com/akomyagin/gitl/cmd/gitl@latest

# Homebrew (macOS/Linux)
brew install akomyagin/tap/gitl

# npm — скачивает готовый бинарь под вашу платформу из GitHub Releases
# и проверяет его SHA256-контрольную сумму (Go-тулчейн не нужен).
# ПРИМЕЧАНИЕ: ещё не опубликовано в реестре npm (публикация ждёт секрета
# NPM_TOKEN) — пока используйте go install / Homebrew / релизный бинарь.
npx gitl-cli review HEAD~5..HEAD   # или: npm install -g gitl-cli (после публикации)

# Либо подписанный релизный бинарь из GitHub Releases (см. VERIFY.md)
```

### Локальный тест мультипровайдерности (Ollama)

`docker-compose.yml` поднимает **только dev-зависимость** — локальный Ollama
для проверки мультипровайдерного LLM-клиента (сам `gitl` в контейнер не оборачивается):

```bash
docker compose up ollama
```

## Конфигурация

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

# Anthropic (нативный Claude Messages API)
llm:
  provider: "anthropic"
  api_key: ""            # или env GITL_API_KEY
  model: "claude-sonnet-4-6"
  # base_url необязателен; по умолчанию https://api.anthropic.com

# Google Gemini (Google AI Studio)
llm:
  provider: "gemini"
  api_key: ""            # или env GITL_API_KEY
  model: "gemini-2.5-flash"
  # base_url необязателен; по умолчанию https://generativelanguage.googleapis.com/v1beta
```

### Стриминг (`output.stream`)

При интерактивном ревью (`md` или `text` в TTY) `gitl` стримит токены в терминал
по мере поступления — не нужно ждать полного ответа. Стриминг включён по умолчанию
и автоматически отключается в CI (не-TTY stdout) или с `--format=json`.

Стриминг сейчас реализован **только для OpenAI-совместимого провайдера**
(`openai` / `ollama` / `azure_openai`). С нативным `anthropic` или `gemini`
провайдером `gitl` прозрачно отдаёт то же ревью одним буферизованным ответом
(без токен-за-токеном), независимо от `output.stream` / `--no-stream`.

```yaml
output:
  stream: true   # по умолчанию; false — всегда буферизовать
```

Отключить для одного вызова: `gitl review HEAD~5..HEAD --no-stream`

### Кэш LLM-ответов (`cache`)

`gitl review` кэширует ответы модели на диск (SHA-256 от провайдера + модели + промпта).
Одинаковые диффы переиспользуют кэш мгновенно — без API-вызова и без стоимости.

```yaml
cache:
  enabled: true    # по умолчанию
  ttl_hours: 24    # записи старше этого игнорируются
```

Кэш хранится в `~/.cache/gitl/review/` (XDG-совместимо). Пропустить для одного вызова:
`gitl review HEAD~5..HEAD --no-cache`

### Кастомные шаблоны (`prompt.system_template_file` / `output.template_file`)

Два независимых override'а, оба только через конфиг (CLI-флага для них нет):

- **`prompt.system_template_file`** — собственный **системный промпт ревью**
  (чеклист безопасности, архитектурные ограничения, правила команды):

  ```yaml
  prompt:
    system_template_file: "./review-policy.md"   # путь относительно CWD
  ```

  Шаблон системного промпта получает `{{ .Commits }}`, `{{ .Diff }}`,
  `{{ .Range }}`, `{{ .Staged }}` (см. `internal/prompt/templates.go`).

- **`output.template_file`** — собственный **шаблон рендера `md`-формата** для
  готового артефакта ревью:

  ```yaml
  output:
    template_file: "./review-output.tmpl"   # путь относительно CWD
  ```

  Шаблон вывода получает render-функции из `internal/render/render.go`
  (`render.TemplateFuncs()`).

> **Про доверие:** `prompt.system_template_file`/`output.template_file` можно
> задать через repo-level `.gitl.yaml`, а не только в личном конфиге — значит
> `gitl review` на склонированном чужом/недоверенном репозитории может
> указать на шаблон *внутри этого же репозитория*. Это осознанный механизм
> для team-shared review policy, не баг: `text/template` здесь не читает
> произвольные файлы и не исполняет код, но к `.gitl.yaml` недоверенного
> репозитория стоит относиться с той же осторожностью, что и к его
> `.git/hooks` или build-скриптам.

## GitHub Action

`gitl` можно подключить как GitHub Action: он AI-ревьюит коммиты пул-реквеста
и оставляет комментарий с риск-скорингом, опционально блокируя мерж по порогу
риска. Action собирает `gitl` из исходников (`go install` на пиннутой версии).
Также числится в [GitHub Marketplace](https://github.com/akomyagin/gitl), если
удобнее добавить оттуда.

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
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0    # обязательно: без полной истории base..head не резолвится

      - uses: akomyagin/gitl@v0.4.3
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
