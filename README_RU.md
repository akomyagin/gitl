# gitl

**AI-ревью кода для вашего CI с риск-скором, по которому можно гейтить merge — ваш ключ, любой провайдер или полностью офлайн.**

`gitl` (git-log-lens) читает диапазон коммитов и превращает его в структурированный
инженерный артефакт: AI-ревью с машиночитаемым уровнем риска, детерминированный
changelog и сводку активности по нескольким репозиториям — один Go-бинарник,
без сервера и без базы данных.

[![CI](https://github.com/akomyagin/gitl/actions/workflows/ci.yml/badge.svg)](https://github.com/akomyagin/gitl/actions/workflows/ci.yml)
[![Action self-test](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml/badge.svg)](https://github.com/akomyagin/gitl/actions/workflows/action-selftest.yml)
[![Последний релиз](https://img.shields.io/github/v/release/akomyagin/gitl)](https://github.com/akomyagin/gitl/releases)
[![Лицензия MIT](https://img.shields.io/github/license/akomyagin/gitl)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/akomyagin/gitl.svg)](https://pkg.go.dev/github.com/akomyagin/gitl)
[![Подписанные релизы](https://img.shields.io/badge/releases-signed%20(cosign%20%C2%B7%20SLSA%20L3)-blueviolet)](VERIFY.md)

![gitl review — риск-скор в терминале](site/assets/demo-review.gif)

*`gitl review` — AI-ревью диапазона коммитов с машиночитаемым уровнем риска
прямо в терминале.*

## Зачем

Почти все AI-ревьюеры — это SaaS: ваш дифф уезжает на чужой сервер, а обратно
приходит комментарий, а не контракт. Мне хотелось наоборот — CLI/CI-инструмент,
где ревью делается **моим ключом у моего провайдера (или вообще без провайдера)
и даёт уровень риска, по которому CI может ветвиться**. Один бинарник закрывает
три задачи, под которые обычно ставят три разных инструмента: ревью с
риск-гейтом, changelog и мульти-репо дайджест. Телеметрии нет, ключи нигде не
хостятся, а без ключа всё продолжает работать: детерминированная офлайн-эвристика
держит CI зелёным и бесплатным.

## Установка

```bash
# Homebrew (macOS/Linux)
brew install akomyagin/tap/gitl

# npm — скачивает готовый бинарник под вашу платформу и сверяет контрольную сумму
npx gitl-cli review HEAD~5..HEAD        # или: npm install -g gitl-cli

# Go-тулчейн
go install github.com/akomyagin/gitl/cmd/gitl@latest
```

Либо возьмите подписанный бинарник из [GitHub Releases](https://github.com/akomyagin/gitl/releases)
(как проверить подпись — [VERIFY.md](VERIFY.md)). Нужен `git` в `PATH`;
Go 1.22+ требуется только для `go install` / сборки из исходников.

> **Подписанные релизы (cosign keyless + SLSA L3 provenance), без телеметрии,
> BYOK — ваш API-ключ никогда не покидает вашу машину.** Любой релиз можно
> проверить до запуска: [VERIFY.md](VERIFY.md).

## Возможности

- **`gitl review <range>`** — AI-ревью с машинным уровнем риска
  (`low|medium|high`); `--fail-on=high` завершается кодом 2 — CI может гейтить
  merge; токены стримятся в терминал; дисковый кэш ответов (плюс opt-in общий
  remote-кэш для CI); `--staged` ревьюит незакоммиченные изменения; `pr/N`
  ревьюит GitHub PR через `gh`.
- **`gitl changelog [<range>]`** — changelog в стиле Keep a Changelog с
  группировкой по conventional commits, детерминированный по умолчанию; `--ai`
  переписывает его в прозу release notes, а без ключа откатывается к
  детерминированному результату — команда никогда не падает.
- **`gitl digest [--days=N] [--repos=a,b,c]`** — сводка активности по
  авторам/темам/файлам по нескольким репозиториям параллельно, с интерактивным
  TUI-просмотрщиком (`--tui`).

Провайдеры (BYOK): OpenAI-совместимый API, Ollama (локально/self-hosted),
Azure OpenAI, нативный Anthropic (Claude), Google Gemini — или вообще без
провайдера.

Кроме того, gitl — это MCP-сервер (`gitl mcp`) и CI-обёртки для GitHub Actions,
GitLab, Bitbucket и Gitea — всё это описано на
[сайте документации](https://akomyagin.github.io/gitl/ru/docs.html).

## Быстрый старт

```bash
# AI-ревью диапазона коммитов (стримится в терминал)
GITL_API_KEY=sk-... gitl review HEAD~5..HEAD

# без ключа — детерминированное офлайн-ревью (эвристический риск, без сети)
gitl review HEAD~5..HEAD

# машиночитаемый вывод + CI-гейт
gitl review HEAD~5..HEAD --format=json --fail-on=high   # exit 2 при высоком риске

# changelog с последнего тега; дайджест активности за 14 дней
gitl changelog
gitl digest --days=14
```

Exit-коды — часть контракта: `0` — ок, `1` — ошибка инструмента, `2` — сработал
риск-гейт `--fail-on`. Полный справочник команд — на
[сайте документации](https://akomyagin.github.io/gitl/ru/docs.html).

![gitl --fail-on=high роняет CI-проверку](site/assets/demo-gate.gif)

*`--fail-on=high` превращает высокорисковый диапазон в ненулевой exit-код (2),
по которому CI может гейтить.*

## Сценарии

- **Гейтить PR по AI-риску в CI** — GitHub Action оставляет sticky-комментарий с
  ревью и роняет проверку выше вашего порога.
- **Страховка перед коммитом** — `gitl review --staged` работает офлайн перед
  каждым коммитом, бесплатно.
- **Release notes одной командой** — `gitl changelog --ai`.
- **Мульти-репо дайджест к стендапу** — `gitl digest --repos=… --tui`.

Все сценарии разобраны от начала до конца на
[странице сценариев](https://akomyagin.github.io/gitl/ru/use-cases.html).

![gitl digest --tui — просмотр мульти-репо сводки](site/assets/demo-digest-tui.gif)

*`gitl digest --tui` — интерактивный просмотрщик сводки активности.*

## Сравнение

| | BYOK / любой провайдер | Офлайн-режим | Машинный риск-скор → CI-гейт | Ревью + changelog + дайджест |
|---|:---:|:---:|:---:|:---:|
| **gitl** | ✓ | ✓ | ✓ | ✓ |
| PR-Agent (Qodo) | ✓ | — | — | — |
| CodeRabbit | — (SaaS) | — | — | — |
| git-cliff | н/п (без LLM) | ✓ | — | только changelog |

Сравнение по состоянию на сентябрь 2026; поправки приветствуются.

## Документация

Полная документация, справочник конфигурации и разобранные сценарии — на
**[сайте документации](https://akomyagin.github.io/gitl/ru/)** (есть и
[английская версия](https://akomyagin.github.io/gitl/)).

English README — [README.md](README.md).

## Как поучаствовать

Проект я развиваю в открытую, и делаю его один — самое ценное, что можно мне
прислать, это issues, вопросы и баг-репорты с реальных CI-конфигураций.
Открывайте issue или PR — я читаю всё.

## Лицензия

[MIT](LICENSE).
