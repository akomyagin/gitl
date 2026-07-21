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

> **Статус:** выпущен `v0.5.1` — все три команды работают на реальных репозиториях,
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
npx gitl-cli review HEAD~5..HEAD   # или: npm install -g gitl-cli

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

      - uses: akomyagin/gitl@v0.5.1
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

## Gitea Actions (экспериментально)

Тот же `action.yml` работает и на [Gitea Actions](https://docs.gitea.com/usage/actions/overview):
runner Gitea исполняет composite-actions GitHub-формата, а action gitl определяет
платформу в рантайме по переменной `GITEA_ACTIONS=true`, которую act_runner Gitea
подставляет в каждый job. Единственная платформо-специфичная часть — постинг
sticky-комментария — на Gitea идёт через её REST API
(`POST`/`PATCH /api/v1/repos/{owner}/{repo}/issues/...`) через `curl`, а не через
`gh` CLI (он умеет только GitHub API). Для пользователей GitHub ничего не
меняется: без `GITEA_ACTIONS` action ведёт себя ровно как раньше.

Добавьте `.gitea/workflows/gitl-review.yml` в свой репозиторий (полный пример с
комментариями: [`.gitea/workflows/gitl-review.yml`](.gitea/workflows/gitl-review.yml)
в этом репо):

```yaml
name: gitl review
on:
  pull_request:

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: https://github.com/actions/checkout@v7
        with:
          fetch-depth: 0
      - uses: https://github.com/akomyagin/gitl@v0.5.1
        with:
          gitl-api-key: ${{ secrets.GITL_API_KEY }}   # BYOK; без ключа — offline-режим
```

Требования: включённые Actions, свежий act_runner (с поддержкой node24) и образ
runner'а с `bash`, `git`, `curl`, `jq` и node. `GITL_API_KEY` кладётся в
Actions-секреты Gitea, никогда — в YAML; те же BYOK-правила, что и на GitHub.

> **Статус проверки — прочитайте, прежде чем полагаться.** `curl`-вызовы к REST
> API (список комментариев, создание, патч, поиск sticky-маркера) прогнаны
> end-to-end на реальном Gitea-инстансе (`gitea/gitea` в Docker) — пустой
> список → POST-создание → повторный поиск находит его → PATCH-обновление →
> по-прежнему ровно один комментарий. Эта часть работает как задумано. Что
> **ещё не проверено** — окружение самого `act_runner`: совпадают ли
> `GITEA_ACTIONS`/`GITHUB_API_URL`/структура PR-события внутри реального job'а
> с тем, что предполагалось (это сверено с исходниками Gitea/act_runner/форка
> act, но не запускалось внутри настоящего job'а). Считайте именно
> *CI-триггер* экспериментальным, пока кто-нибудь не подтвердит зелёный прогон
> end-to-end внутри реального Gitea Actions; баг-репорты с реальных инстансов
> очень приветствуются.

## GitLab CI (экспериментально)

У gitl есть и [GitLab CI/CD component](https://docs.gitlab.com/ee/ci/components/) —
[`templates/gitl-review.yml`](templates/gitl-review.yml) — зеркалящий GitHub Action:
ставит gitl через `go install` на закреплённой версии, ревьюит диапазон merge
request'а (`$CI_MERGE_REQUEST_DIFF_BASE_SHA..$CI_COMMIT_SHA`), собирает комментарий
через общий платформо-нейтральный [`ci/comment.sh`](ci/comment.sh) и создаёт/обновляет
**sticky-комментарий MR** через REST API GitLab (тот же маркер
`<!-- gitl-review -->`, что на GitHub/Gitea). Job запускается только в
merge-request-пайплайнах.

Репозиторий живёт на GitHub, поэтому компонента пока нет в GitLab CI/CD Catalog —
подключайте через `include:remote` (inputs работают и с remote-include):

```yaml
# .gitlab-ci.yml
include:
  - remote: "https://raw.githubusercontent.com/akomyagin/gitl/v0.5.1/templates/gitl-review.yml"
    inputs:
      fail_on: "never"      # по умолчанию; "high" — блокировать рискованные MR
      # max_cost_usd: "0.50"
      # gitl_version: "v0.5.1"
```

Настройка — две CI/CD-переменные (Settings → CI/CD → Variables, обе **masked**,
никогда — в YAML):

- **`GITL_API_KEY`** — BYOK-ключ LLM. Опционален: без него gitl выполняет
  детерминированное **offline-ревью** (без сети и затрат). Достаточно завести
  переменную проекта — она имеет приоритет над пустым дефолтом input'а
  `gitl_api_key`. Если всё же используете input, передавайте *ссылку на
  переменную* (`gitl_api_key: $MY_LLM_KEY`), никогда — сам ключ: значения
  inputs интерполируются в конфигурацию пайплайна.
- **`GITL_GITLAB_TOKEN`** — токен для постинга комментария (project access token
  или PAT, scope `api`, роль Reporter и выше; передаётся как `PRIVATE-TOKEN`).
  Если не задан, job откатывается на `CI_JOB_TOKEN` (заголовок `JOB-TOKEN`) —
  но в большинстве конфигураций GitLab `CI_JOB_TOKEN` **не** имеет прав на
  Notes API, так что fallback скорее всего упадёт (с явным сообщением об
  ошибке, а не молчаливым пропуском). Надёжный путь — явный `GITL_GITLAB_TOKEN`.

Полный self-test-пайплайн с комментариями — он же самый полный пример
использования — [`.gitlab-ci-selftest.yml`](.gitlab-ci-selftest.yml) (запускается
как `.gitlab-ci.yml` в GitLab-зеркале этого репозитория).

> **Статус проверки — прочитайте, прежде чем полагаться.** REST-вызовы GitLab
> (список MR-notes + поиск sticky-маркера, `POST`-создание, `PUT`-обновление) и
> сам YAML компонента (интерполяция `spec:`/`inputs:`, `include:local` с
> inputs — через CI Lint API) прогнаны end-to-end на реальном локальном GitLab
> CE (`gitlab/gitlab-ce` 19.2.0 в Docker) на настоящем merge request — пустой
> список → POST-создание → повторный поиск находит → PUT-обновление →
> по-прежнему ровно один комментарий — теми же `curl`/`jq`-командами, что в
> шаблоне. Что **ещё не проверено** — живой прогон
> пайплайна: значения `CI_MERGE_REQUEST_DIFF_BASE_SHA`/`CI_COMMIT_SHA`/
> `CI_JOB_URL` внутри реального merge-request-пайплайна записаны по документации
> GitLab, а не наблюдались; отказ fallback'а на `CI_JOB_TOKEN` задокументирован
> по allowlist'у job-token'а из доков GitLab, а не воспроизведён. Считайте
> именно *пайплайн-путь* экспериментальным, пока кто-нибудь не подтвердит
> зелёный end-to-end прогон; баг-репорты приветствуются.

> **Trust note.** Компонент скачивает `ci/comment.sh` с
> `raw.githubusercontent.com` на `gitl_version` и исполняет его — без
> проверки контрольной суммы/подписи, та же доверительная граница, что и у
> строки `go install ...@${gitl_version}` строкой выше (тот же репозиторий,
> тот же ref). Если это важно для вашей модели угроз — пиньте `gitl_version`
> на SHA коммита, а не тег (теги перемещаемы). Публикация компонента в
> GitLab CI/CD Catalog устранила бы это скачивание целиком — запланировано,
> но ещё не сделано.

## Bitbucket Pipelines (экспериментально)

Интеграция с Bitbucket поставляется как [Pipe](https://support.atlassian.com/bitbucket-cloud/docs/what-are-pipes/) —
а pipes по определению являются Docker-образами, поэтому, в отличие от GitHub/Gitea
action и GitLab-компонента (чистые YAML-обёртки), здесь это самодостаточный образ:
[`bitbucket-pipe/Dockerfile`](bitbucket-pipe/Dockerfile) собирает статический бинарь
`gitl` и вшивает в образ общий рендер [`ci/comment.sh`](ci/comment.sh) и точку входа
[`bitbucket-pipe/pipe.sh`](bitbucket-pipe/pipe.sh). Pipe резолвит диапазон PR
(`$BITBUCKET_PR_DESTINATION_COMMIT..$BITBUCKET_COMMIT`), запускает
`gitl review --format=json` и создаёт/обновляет **sticky-комментарий PR** через REST
API Bitbucket Cloud (тот же маркер `<!-- gitl-review -->`, что на остальных
платформах). Справочник переменных — [`bitbucket-pipe/pipe.yml`](bitbucket-pipe/pipe.yml).

> **Статус образа.** Образ **ещё не опубликован на Docker Hub** — job
> `docker-publish` релизного workflow пропускает push, пока не заведены секреты
> `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` (тот же graceful-skip-паттерн, что у
> npm). До тех пор соберите его сами из корня репозитория:
> `docker build -f bitbucket-pipe/Dockerfile -t alkom68/gitl-review-pipe:0.5.1 .`
> и запушьте в реестр, доступный вашему пайплайну.

```yaml
# bitbucket-pipelines.yml
pipelines:
  pull-requests:
    '**':
      - step:
          name: gitl review
          clone:
            depth: full   # дефолтный клон глубиной 50 может не содержать базовый коммит PR
          script:
            - pipe: docker://alkom68/gitl-review-pipe:0.5.1
              variables:
                GITL_API_KEY: $GITL_API_KEY                    # BYOK; уберите для offline-ревью
                GITL_BITBUCKET_TOKEN: $GITL_BITBUCKET_TOKEN    # постит комментарий PR
                # FAIL_ON: "high"        # по умолчанию "never" — только комментарий, без гейта
                # MAX_COST_USD: "0.50"
```

Настройка — две **secured**-переменные репозитория/workspace (Repository settings →
Pipelines → Repository variables; всегда ссылкой `$VAR`, никогда — литеральные
значения в YAML):

- **`GITL_API_KEY`** — BYOK-ключ LLM. Опционален: без него gitl выполняет
  детерминированное **offline-ревью** (без сети и затрат).
- **`GITL_BITBUCKET_TOKEN`** — креденшал для постинга комментария PR:
  **access token** репозитория/проекта/workspace со scope `pullrequest:write`,
  передаётся как `Authorization: Bearer`. Альтернатива: задайте
  `GITL_BITBUCKET_USER` + `GITL_BITBUCKET_APP_PASSWORD` (app password со scope
  `pullrequest:write`) — тогда Basic-аутентификация. Если не задано ни то, ни
  другое — pipe падает сразу с явным сообщением, ещё **до** каких-либо трат на LLM.

> **Supply-chain-заметка (чем это отличается от GitLab-компонента).** Pipe не
> исполняет ничего, скачанного в рантайме: бинарь `gitl`, `ci/comment.sh` и
> точка входа собраны в версионированный образ из одного дерева исходников.
> GitLab-компонент вынужден скачивать `ci/comment.sh` по сети без проверки
> целостности (см. его trust note выше); pipe закрывает эту брешь по
> построению.

> **Статус проверки — прочитайте, прежде чем полагаться.** Сборка образа и
> полный поток внутри контейнера проверены локально: `docker build` из этого
> репозитория, затем `docker run` на настоящем тестовом git-репозитории с
> эмулированными переменными `BITBUCKET_*` — offline-ревью → корректный
> sticky-`comment.md` → создание комментария (`POST`), sticky-обновление
> (`PUT`, по-прежнему ровно один комментарий) и проброс кода выхода
> `--fail-on`, прогнаны end-to-end против локального мока comments API
> Bitbucket; fail-fast-пути (нет креденшала/PR-переменных) и fallback-заметка
> на битом диапазоне тоже прогнаны в контейнере. Что **ещё не проверено**: всё,
> что касается настоящей инфраструктуры Bitbucket — REST-вызовы к
> api.bitbucket.org (формы взяты из документации Atlassian API), точные
> predefined-переменные внутри живого PR-пайплайна
> (`BITBUCKET_PR_DESTINATION_COMMIT` и др. — задокументированные допущения, а
> не наблюдавшиеся значения) и то, как Pipelines монтирует клон в контейнеры
> pipe'ов. Считайте именно *live-пайплайн-путь* экспериментальным, пока
> кто-нибудь не подтвердит зелёный прогон на реальном Bitbucket-workspace;
> баг-репорты приветствуются.

## MCP-сервер

`gitl mcp` запускает gitl как [Model Context Protocol](https://modelcontextprotocol.io)
stdio-сервер — отдельный, дополнительный канал к CLI/CI-использованию выше, для работы с
`gitl` интерактивно внутри агентской сессии (Claude Desktop, Cursor, Windsurf и т.д.)
вместо вызова через shell. Экспонирует два tool'а:

- **`gitl_review`** — тот же движок ревью, что `gitl review`: `range`/`pr`/`staged`
  (ровно один), опциональные per-call оверрайды `provider`/`model`/`base_url`. Всегда
  возвращает структурированный JSON-артефакт (без md/text-рендера, без стриминга — tool
  result атомарен). `risk.level` возвращается как данные; `--fail-on` в MCP-режиме нет,
  так как нет exit-кода процесса, который можно было бы гейтить.
- **`gitl_digest`** — то же, что `gitl digest`: `days` (по умолчанию 7), опциональный
  `repos`. Без явного `repos` tool дайджестит только рабочую директорию сервера (плюс
  `digest.repos` из `.gitl.yaml`, если настроен) — никогда не обходит произвольные пути по
  собственной инициативе. Явный `repos` учитывается как есть (у вызывающего агента и так
  есть доступ к файловой системе через свои средства; это не access-control граница, а
  просто «не удивлять пользователя» по умолчанию).

Добавьте в конфиг вашего MCP-клиента (Claude Desktop, Cursor и т.д.):

```json
{
  "mcpServers": {
    "gitl": {
      "command": "gitl",
      "args": ["mcp"]
    }
  }
}
```

Конфиг загружается один раз при старте так же, как у обычных команд (`.gitl.yaml` +
личный конфиг + `GITL_*` env, из директории, в которой запущен `gitl mcp`). Без ключа
tool-вызовы идут в том же детерминированном offline-режиме, что и CLI. `stdout`
зарезервирован под MCP-протокол — ничего человекочитаемого туда не пишется никогда;
предупреждения идут в stderr.

## Лицензия

[MIT](LICENSE).
