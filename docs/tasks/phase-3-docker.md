# Задача: фаза 3 — docker-жизнь

Читай сначала: `docs/PLAN.md`, `docs/inventory/containers.md`, `docs/contract/openapi.json`.
Источник истины — `~/spark-ai-hub/daemon/services/docker_service.py` (1204 строки),
`daemon/routers/containers.py`, `daemon/models/container.py` (только чтение).

## Что сделать

Пакеты `internal/dockerx` (обёртка над `docker`/`docker compose` CLI через `exec.Command`)
и `internal/api/containers`.

1. `dockerx`: клиент с методами inspect/list/run/stop/rm/logs/compose-up-down/build/pull.
   Никакого docker SDK — только CLI, ошибки с stderr в тексте.
2. Эндпоинты 1:1: `POST /api/recipes/{slug}/install|update|launch|stop|purge`,
   `GET /api/recipes/{slug}/build-status|launch-plan|compose`,
   `PUT /api/recipes/{slug}/compose`, `POST /api/recipes/{slug}/compose/reset`,
   `DELETE /api/recipes/{slug}`, `GET /api/containers`.
3. WS `GET /ws/build/{slug}` и `GET /ws/logs/{slug}`: тот же текстовый фрейминг, что в
   апстриме (`[done]`, `[spark-ai-hub:ready]`, `[spark-ai-hub:error] <текст>`), тот же
   порядок и те же терминальные маркеры. Библиотека `github.com/coder/websocket`.
4. `launch-plan` и конфликты: сравнение требуемой памяти с занятой, payload конфликта
   как в `docs/inventory/containers.md`.
5. Состояние в таблице `installed_recipes` (схема в `docs/contract/schema.sql`).
   Реализовать `InstalledRecipes(ctx)`/`HasLeftovers(ctx, slug)` — этот тип заменит
   заглушку `installed{}` в `cmd/sahd/main.go`; сам main.go не править, только
   предоставить тип в `internal/dockerx` (или `internal/store` — на твой выбор, но
   одна реализация).
6. Имена контейнеров/проекта compose — с префиксом из конфига (`cfg.Project`), не
   хардкодить `spark-ai-hub`.
7. Тесты: поддельный `docker` (shell-скрипт в `t.TempDir()` + `PATH`), проверяющий
   аргументы; юнит-тесты парсеров вывода; WS-тест на фрейминг. Реальных контейнеров
   в тестах не запускать.

## Границы

- Не трогать `cmd/sahd/main.go`, `internal/web/*`, `internal/auth/*`, `internal/registry/*`,
  `internal/api/{auth,recipes,system}`, `frontend/*`.
- Экспортировать `func Register(r *web.Router, deps Deps)`; `Deps` — конфиг, реестр
  (`*registry.Cache`), логгер, БД (`*store.DB`).
- Опасные операции (`purge`, бульдозер образов) — только в пределах `cfg.Project`;
  апстрим чистит все образы, для тестового инстанса это недопустимо.

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- `GET /api/containers` и `GET /api/recipes/{slug}/launch-plan` работают на живом
  docker этой машины (посмотреть глазами, убедиться, что контейнеры живого хаба видны
  только на чтение и ничего не запускается).
- Коммит в текущую ветку: `phase 3: docker-жизнь`.
