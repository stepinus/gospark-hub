# Задача: фаза 2 — реестр рецептов

Читай сначала: `docs/PLAN.md`, `docs/inventory/registry-media.md`,
`docs/contract/openapi.json`. Источник истины — `~/spark-ai-hub/daemon/models/recipe.py`,
`daemon/services/registry_service.py`, `daemon/routers/recipes.py` (только чтение).

## Что сделать

Пакет `internal/registry` + роуты `internal/api/recipes`.

1. Загрузка всех `registry/recipes/*/recipe.yaml` (148 штук) + одноимённых compose-файлов;
   кэш в памяти, перечитывание по mtime; ошибки одного рецепта не валят остальные.
2. Структуры YAML строго по `daemon/models/recipe.py`: все ключи и вложенные поля,
   дефолты, `params_b` как float (строка `"7"` тоже принимается), неизвестные ключи
   игнорируются и в JSON не попадают (как pydantic).
3. `GET /api/recipes` — точный ответ апстрима: список всех рецептов с полями, которые
   читает фронт (`frontend/src/store.js`, `pages/Catalog.jsx`), плюс оверлей состояния
   установки из БД (`installed`, `running`, `has_leftovers` — семантика как в
   `daemon/routers/recipes.py:37-106`, включая «липкий» `has_leftovers`).
4. `GET /api/recipes/{slug}` — деталь рецепта; неизвестный slug — 404 с тем же телом.
5. Статика: `/covers/*` (рендеренные обложки из `frontend/public/covers`),
   `/logos/*`, `/cover-sources/*` (уже смонтирован каркасом — не дублировать).
6. Никакой мутации общих структур при отдаче JSON: снимок на запрос или RWMutex,
   в апстриме тут гонка (`recipes.py:37-63`) — повторять её не нужно, ответ должен
   совпадать, а не гонка.
7. Добавить `internal/parity/list_test.go`: тест, который грузит все 148 рецептов
   и сверяет число и набор slug'ов с `registry/recipes/*`.

## Границы

- Не трогать `internal/web/*`, `cmd/sahd/main.go`, `internal/auth/*`, `frontend/*`.
- Экспортировать `func Register(r *web.Router, deps Deps)`.
- Зависимость `gopkg.in/yaml.v3` уже в `go.mod`.

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- Тест: `/api/recipes` на нашем registry отдаёт 148 записей; поля совпадают с
  перечнем из `docs/inventory/registry-media.md`.
- Коммит в текущую ветку: `phase 2: реестр рецептов`.
