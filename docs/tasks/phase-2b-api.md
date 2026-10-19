# phase-2b-api: эндпоинты реестров, копия и удаление локального рецепта

Стартует **после** слияния `phase-2b-core` (нужны `registry.Manager`,
`Cache.Origin/IsLocal`). Читать `docs/tasks/phase-2b-registries.md` §3.2, §3.5, §4, §5.

## Цель

HTTP-слой над менеджером источников + копия рецепта + удаление локального +
обложки из нескольких корней.

## Файлы в скоупе

- `internal/api/registries/register.go` — новый пакет: все маршруты из §3.5,
  админ-гард как у `/api/admin/*`.
- `internal/api/recipes/local.go` — новый: `POST /api/recipes/{slug}/fork`,
  `DELETE /api/recipes/{slug}/local`.
- `internal/api/recipes/register.go` — только: поле `"local"` в JSON рецепта,
  монтирование маршрутов из `local.go`.
- `internal/api/containers/register.go` — `defaultComposeContent` берёт корень из
  `cache.Origin(slug)` вместо `dirname^4`.
- `internal/web/server.go` — `/cover-sources/` из нескольких корней
  (`local`, затем `vendored/*/covers`), первый найденный файл.
- `cmd/sahd/main.go` — только вызов `registries.Register(r, deps)` (+ передать
  `mgr` и `reg`). Если `main.go` конфликтует при слиянии — правку делает Hermes.
- тесты: `internal/api/registries/register_test.go`, `internal/api/recipes/local_test.go`.

## Не трогать

`internal/registry/**` (менеджер уже готов, ошибки в нём — отдельная задача),
`frontend/**`, `docs/**`, `registry/**`.

## Требования

1. Все новые маршруты — только для роли `admin`, иначе 403 `{"detail":"Admin only"}`.
2. `POST /api/registries` возвращает и успешные источники, и ошибки по строкам,
   код 200 даже при частичных ошибках (ошибка целиком — 400).
3. `fork`: тело `{"slug":"new-slug"}`, валидация слага
   `^[a-z0-9][a-z0-9-]{1,62}$`, занятый → 409 `{"detail":...}`, неизвестный
   источник → 404.
4. `DELETE /api/recipes/{slug}/local`: если рецепт установлен (`installed_recipes`)
   или запущен — 409, каталог не тронут; иначе каталог удалён, `cache.Load()`,
   200 `{"deleted":true}`; для не-локального — 404.
5. `defaultComposeContent`: для локального рецепта без источника — 409
   «локальный рецепт, сброс недоступен»; для внешнего — `git show HEAD:<rel>` в
   корне источника.
6. Тесты на коды 403/400/409/404 и на то, что после удаления каталог исчез, а
   после fork оригинал не изменён.

## Готово когда

```
go build ./... && go vet ./... && go test ./... -count=1 && staticcheck ./...
```
зелёные, плюс живой curl-прогон на тестовом инстансе (порты 9111/9101):

```
POST /api/registries {"urls":"https://github.com/WaxacaBytes/spark-ai-hub/registry/recipes"}
GET  /api/registries           # commit, recipes=148
POST /api/recipes/searxng/fork {"slug":"my-searxng"}
DELETE /api/recipes/my-searxng/local
```
и один коммит:

```
git add -A && git commit -m "API: реестры, копия рецепта, удаление локального, обложки из корней"
```
