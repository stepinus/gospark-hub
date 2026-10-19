# phase-2b-core: источники реестров и корни кэша

Читать вместе с `docs/tasks/phase-2b-registries.md` (полный план, §2, §3.1–3.4).

## Цель

Демон читает рецепты **только** из `~/.config/gospark/registry`, умеет держать
несколько источников (папки GitHub-репозиториев) и синхронизировать их по
команде. Сети на старте нет, фолбэков на `repo/registry/recipes` нет.

## Файлы в скоупе

- `internal/registry/source.go` — новый, `Manager` из §3.1 плана.
- `internal/registry/cache.go` — `NewCache(roots ...string)`, `Origin`, `IsLocal`.
- `internal/registry/source_test.go`, `cache_roots_test.go` — новые тесты.
- `internal/config/config.go` — `RegistryHome` (`REGISTRY_HOME`, default
  `~/.config/gospark/registry`).
- `cmd/sahd/main.go` — создать `Manager`, передать `mgr.Roots()` в кэш, отдать
  сам кэш в API.
- `internal/api/recipes/register.go` — брать `*registry.Cache` из `Deps` вместо
  `registry.NewCache(cfg.RegistryPath)` (сейчас кэшей два).

## Не трогать

`internal/api/registries/**` (пишет другой воркер), `internal/api/containers/**`,
`frontend/**`, `docs/ACCEPTANCE.md`, `docs/PLAN.md`, `registry/**`.

## Требования

1. `sources.yaml` — единственный источник правды о реестрах; при отсутствии файла
   создаётся с источником по умолчанию
   `https://github.com/WaxacaBytes/spark-ai-hub` → `registry/recipes`, `ref: main`.
2. `Add` принимает строку со ссылками через запятую/перевод строки, парсит формы
   из таблицы §3.1, по каждой ошибку отдельно, остальные всё равно добавляет.
3. Синхронизация: `git clone --depth 1 --filter=blob:none --sparse --branch`,
   `sparse-checkout set <subpath> [<covers>]`, запись `commit`, `synced_at`,
   `error`. Обновление — `fetch --depth 1` + `reset --hard FETCH_HEAD`.
   Таймаут 120 с на операцию.
4. `Roots()` — сначала `vendored/*/<subpath>` в порядке добавления, затем
   `local/`. Несуществующие каталоги в список не попадают.
5. `Fork(src, newSlug)` — копия каталога рецепта в `local/<newSlug>`; если
   `local/<newSlug>` или слаг занят в vendored → ошибка `ErrSlugTaken`.
   `DeleteLocal(slug)` удаляет `local/<slug>`, для не-локального — `ErrNotLocal`.
6. `NewManager` не ходит в сеть и не падает, если home недоступен на запись
   (вернуть ошибку).
7. Хосты: только github.com/gitlab.com/codeberg.org; localhost, 127.0.0.1,
   приватные IP, `file://` — отказ.

## Готово когда

```
go build ./... && go vet ./... && go test ./... -count=1 && staticcheck ./...
```
зелёные, тесты из §6 плана написаны (парсер URL, приоритет, fork с проверкой
хешей оригинала, sync на локальном `git init` во временном каталоге, пустой home
→ 0 рецептов без ошибок), `.pi/` не закоммичен, и сделан один коммит:

```
git add -A && git commit -m "Реестры: источники, спарс-синхронизация, корни кэша"
```
