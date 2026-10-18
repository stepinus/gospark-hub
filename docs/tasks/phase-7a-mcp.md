# Задача: фаза 7a — MCP-инструменты

Читай сначала: `docs/PLAN.md`, `docs/inventory/registry-media.md` (раздел про `/mcp`),
`~/spark-ai-hub/daemon/routers/mcp.py` (1153 строки) и `tests/test_mcp.py` (79 КБ —
это спецификация), `daemon/services/{image,video,audio,mesh}_service.py` (только чтение).

## Что сделать

Пакет `internal/mcp` + роут `internal/api/mcp`.

1. `POST /mcp` — JSON-RPC 2.0 по спецификации MCP: те же версии протокола, что в
   апстриме, `initialize`, `tools/list`, `tools/call`, обработка неизвестного метода и
   ошибок формата — байт в байт как апстрим (сообщения `isError` тоже).
2. Полный список инструментов из `daemon/routers/mcp.py`: имена, описания, `inputSchema`
   и поведение каждого. Медиа-инструменты вызывают установленное приложение (контейнер
   рецепта) по HTTP — резолв цели как в апстриме; если приложение не установлено/не
   запущено — та же ошибка, что у апстрима.
3. Артефакты: `GET /images/{name}`, `/videos/{name}`, `/audio/{name}`, `/meshes/{name}` —
   отдача файлов с теми же заголовками, путями и 404.
4. Каждый вызов пишет `usage_events` (ключ из `Authorization`/`X-Api-Key`, route, model,
   токены) — как в апстриме.
5. Тесты: httptest-сервер вместо приложения-контейнера, проверяющий, что наш MCP отдаёт
   ровно те же JSON-RPC ответы, что ожидает `tests/test_mcp.py`; отдельно — `tools/list`
   без установленных приложений, ошибка вызова, отдача артефакта.

## Границы

- Не трогать `cmd/sahd/main.go`, `internal/web/*`, `internal/auth/*`, `internal/registry/*`,
  `internal/dockerx/*`, `internal/media/*`, `internal/api/{auth,recipes,system,files,containers}`,
  `frontend/*`.
- `internal/media` можно использовать (публичный API есть после фазы 6); менять его —
  только если не хватает метода, и тогда отдельным коммитом с тестом.
- Экспортировать `func Register(r *web.Router, deps Deps)`.

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- `tools/list` содержит все инструменты апстрима (перечисли в отчёте, сколько их).
- Коммит в текущую ветку: `phase 7a: MCP`.
