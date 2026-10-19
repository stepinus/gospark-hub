# Приёмка этапа 1

Состояние на 2026-10-19. Проверки воспроизводимы: `docs/PARITY.md`, `go test ./...`,
команды ниже.

## Что проверено машинно

| Проверка | Как | Итог |
|---|---|---|
| Все GET-маршруты апстрима существуют у нас | `go run ./cmd/parity -sweep` (читает `/openapi.json` апстрима; записывающие пути считаются, но не вызываются) | 21 из 21 (28 записывающих пропущено, покрыты кейсами и ручным прогоном) |
| Совпадение ответов | `go run ./cmd/parity` — 19 кейсов, дифф по путям | 19 из 19 |
| Кадр метрик по WebSocket | `-sweep` + ws-кейс | структура совпадает |
| Обложки и логотипы | `parity`-кейс `assets` — GET по всем путям из реестра | 126 из 126 отдаются одинаково |
| Схема рецепта (57 полей) | `parity` кейс `recipes` — все 148 записей, значения | совпадает |
| Тесты | `go test ./... -count=1` | 134 теста, 12 пакетов, зелёные |
| Экраны фронтенда | headless Chrome по маршрутам `/`, `/running`, `/system`, `/files`, `/users`, `/account`, `/agents`, `/about` | все отрисованы, метрики живые, ошибок нет |
| Сборка фронтенда из копии | `npm run build` в `frontend/` | собирается без правок исходников |

## Что намеренно отличается от апстрима

1. **Порты**: демон 9110, публичный 9100 (апстрим 9010/9000) — стенд стоит рядом с живым хабом.
2. **Префикс docker**: `gospark-hub` вместо `spark-ai-hub` (сеть, имена контейнеров, проект
   compose). Иначе тестовый хаб трогал бы контейнеры живого. Отсюда же разница в
   `installed`/`running`/`ready`/`has_leftovers` — это вопросы к конкретной docker-установке.
3. **`proxy_enabled=false`**: демон сам слушает публичный порт. В апстриме в этом режиме
   публичный порт не слушает никто (баг R3 из `docs/inventory/containers.md`).
4. **Идеи на этап 2** — `docs/IDEAS.md` (встроенный прокси вместо контейнера Caddy и др.).

## Что ещё не проверено живьём (и как проверять)

| Пункт | Почему не проверено | Как проверить |
|---|---|---|
| Реальная установка приложения | — | **проверено живьём** на `searxng`: install → pull по `docker compose -p spark-ai-hub-searxng` → `[spark-ai-hub] searxng installed successfully!`, образ на месте |
| Запуск/остановка и `/run/{slug}/` | — | **проверено живьём**: launch → контейнер `spark-ai-hub-searxng` Up, статус `starting` → `running+ready` после health-check; `/run/searxng/` без сессии → `302 /` от Caddy, с сессией → `200 text/html` с живым SearXNG |
| Полное удаление | — | **проверено живьём**: stop → контейнер погас, remove → контейнер и образ `searxng/searxng:2026.9.19-…` удалены (`docker images` пусто), `installed=false`, `has_leftovers=false` |
| Кадры `/ws/build` и `/ws/logs` | — | **проверено живьём**: `/ws/logs/searxng` отдал `[spark-ai-hub:ready]`, затем сообщение приложения и чистое закрытие; build-лог шёл теми же строками, что и `build-status` |
| Вызовы MCP-инструментов | — | **проверено живьём**: `tools/call web_search` к запущенному SearXNG вернул настоящие результаты (NVIDIA DGX Spark), то есть путь MCP → приложение → ответ работает |
| `/v1` и `/v1/messages` против живой модели | — | **проверено живьём** на Qwen3.8-27B INT4 из готового образа: `/v1/models` отдаёт модель, `/v1/chat/completions` ответил «2+2 equals 4» за 2.8 с (`usage` 65/33, reasoning 24), `/v1/messages` вернул `hello` в Anthropic-формате, `usage_events` записал запрос и токены |
| Переиспользование готового образа | — | **проверено живьём**: образ `spark-ai-hub/tensorfold-qwen38-27b-uncensored-int4:tf-v0.6.0-14963e7` уже был на машине, установка его не собирала и не тянула (`Using existing local image …`), запуск шёл `up -d` без `--build` |
| OAuth-цикл | — | **проверено живьём**: регистрация клиента → `/oauth/authorize` с PKCE S256, вход и согласие → `code` → `/oauth/token` → `sah-oat-…` → `POST /mcp` с Bearer отвечает `initialize` |
| Список MCP-инструментов | — | **проверено живьём**: 21 инструмент, имена, `inputSchema` и описания совпадают с апстримом |
| `sah` CLI | отдаётся как есть, Python-скрипт | `curl -s /sah/install.sh \| sh` на чистой машине |
| Правила MANIFEST (offline-first, удаление без остатка) | требуют реальных образов и `--network none` | прогон install → launch → remove → purge с проверкой `docker images`/`docker volume ls` |

## Фаза 2b — реестры-источники и локальные рецепты (2026-10-19)

| Проверка | Как | Итог |
|---|---|---|
| Рецепты только из `~/.config/gospark/registry` | старт с пустым `REGISTRY_HOME` | `sources.yaml` создан с источником по умолчанию, каталог рецептов пуст, 0 рецептов, ошибок нет |
| Синхронизация по кнопке | `POST /api/registries/sync` | 24 с, `commit b23f558`, 148 рецептов, `covers registry/covers` |
| Новый источник виден без перезапуска | `GET /api/recipes` сразу после sync | 148 (кэш следует за `Manager.Roots()` через провайдер корней) |
| Копия рецепта | `POST /api/recipes/searxng/fork {"slug":"my-searxng"}` | 200, `local/my-searxng`, `GET /api/recipes/my-searxng` → `local: true` |
| Занятый слаг | повторный форк | 409 «Slug already taken» |
| Установка копии | `POST /api/recipes/my-searxng/install` | 200, `status: done`, образ `searxng/searxng:2026.9.19` скачан, проект `spark-ai-hub-my-searxng` |
| Запуск и остановка копии | `POST …/launch` → `POST …/stop` | `launched` / `stopped` |
| Удаление установленной копии | `DELETE /api/recipes/my-searxng/local` | 409 «Recipe is installed, stop and remove the app first», файлы на месте |
| Полный цикл удаления | `DELETE /api/recipes/my-searxng` → `DELETE …/local` | `removed` → `deleted`, `local/` пуст, 148 рецептов |
| Обложки из нескольких корней | `GET /cover-sources/app-acestep.jpg` из vendored | 200, 267 КБ |
| Паритет после фазы | `go run ./cmd/parity` | 19 из 20 кейсов совпали; единственное расхождение — `/api/admin/users`, и это данные тестовых аккаунтов (`name`, `last_login_at`), а не контракт |
| Обход путей | `-sweep` | 21 GET-путь отвечает, 28 записывающих пропущено, 0 отсутствующих |
| Тесты | `go test ./... -count=1` | 17 пакетов зелёные (тесты на реальных рецептах берут их из источника через `GOSPARK_TEST_RECIPES` и пропускаются без него) |
| Два источника по умолчанию | пустой `REGISTRY_HOME` → `sources.yaml` | `spark-ai-hub` (main, 148 рецептов) и `stepinus/gospark-hub` (master, 0 рецептов), ошибок нет |
| Страница настроек | headless Chrome, `/settings` | источник виден: URL, ветка `main`, коммит `b23f558`, подпапка, «synced 7 minutes ago», 148 рецептов |

Из репозитория удалены `registry/recipes` и `registry/covers`: рецепты больше не
поставляются с кодом, их приносит источник. `TestRegistryList` и data-driven тесты
пропускаются, когда каталога нет.

## Фаза 9 — движки на хосте и нативные рецепты (2026-10-19)

| Проверка | Как | Итог |
|---|---|---|
| Движок виден до установки | `GET /api/engines`, пустой `ENGINE_HOME` | `{"name":"tensorfold","installed":false,"latest":"1.1.1"}` |
| Установка | `POST /api/engines/tensorfold/install` | 5,5 с: скачивание 22 МБ, сумма sha256 сверена, `tensorfold/1.1.1/`, симлинк `current`; `current/bin/tensorfold-native --version` → `1.1.1` |
| Повторная установка | тот же вызов | 409 «Engine tensorfold 1.1.1 is already installed» |
| Обновление без новой версии | `POST …/update` | `{"version":"1.1.1","changed":false}` |
| Удаление | `DELETE /api/engines/tensorfold` | каталог снесён, `installed:false`; установка заново — 200 |
| Запущенный нативный рецепт | юнит `gospark-native-qwen38-flash-next-native` поднят, `DELETE`/`update` | 409 «A native recipe is running on this engine» |
| Рецепт-натив | `GET /api/recipes/qwen38-flash-next-native` | `kind: native`, блок `native` с движком, `hf:`-ссылкой, аргументами и env |
| Флаги запуска нативного рецепта | юнит активен / остановлен | `running`/`ready` идут за `systemctl --user is-active` |
| Проводка запуска | временный рецепт с несуществующим путём модели → `POST …/launch` | 200, юнит `gospark-native-bogus-native` создан, движок запустился и записал в `<data>/logs/bogus-native.log`: «… is neither a directory nor a Hugging Face repo id» |
| Остановка | `POST …/stop` | `{"status":"stopped"}`, юнита нет |
| Страница Engines | headless Chrome, `/engines` | карточка TensorFold, «Version 1.1.1 · up to date», `Update` задизейблен, `Delete` активен |
| Карточка нативного рецепта | `/app/qwen38-flash-next-native` | бейдж «Native», кнопка `Launch`, установки нет |
| Контейнерные рецепты | `GET /api/recipes`, parity | без поля `kind`, всё как раньше |
| Тесты | `go test ./... -count=1` | 20 пакетов зелёные |

Не проверено живьём: загрузка самого чекпоинта Qwen3.8-Flash-Next (потолок памяти движка
97,24 ГиБ, на машине в момент проверки свободно 91 гибибайт). Проводка запуска проверена
движком на заведомо неверном пути модели.

## Вывод

Функциональный паритет по HTTP, данным рецептов, статике и экранам достигнут и проверяется
одной командой. Непроверенное — то, что требует настоящих образов и контейнеров (установка,
запуск, прокси приложения, MCP-инструменты) и OAuth-цикл, который сейчас пишется.
