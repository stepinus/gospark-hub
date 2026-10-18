# Задача: фаза 1 — хранилище и auth/admin

Читай сначала: `docs/PLAN.md`, `docs/inventory/auth.md`, `docs/contract/openapi.json`,
`docs/contract/schema.sql`. Источник истины — код апстрима `~/spark-ai-hub` (только чтение).

## Что сделать

Пакеты `internal/store` (SQLite) и `internal/auth` (+ роуты в `internal/api/auth`).

1. `internal/store`: открытие БД, создание схемы ровно как в `docs/contract/schema.sql`
   (9 таблиц, те же имена, те же индексы), CRUD пользователей, сессий, usage_events.
   Хранить `created_at`/`expires_at` как **строки** `YYYY-MM-DD HH:MM:SS` в UTC —
   апстрим сравнивает их строково, `time.Time` в колонки не писать.
2. Пароли: алгоритм апстрима `pbkdf2_sha256$<iterations>$<salt>$<hex>`
   (`daemon/services/auth_service.py:38-66`), иначе файл живого хаба не откроется.
   Проверка и генерация обе нужны; тест: хеш из живой базы `data/spark-ai-hub.db`
   апстрима проходит проверку с известным паролем нельзя — вместо этого тест
   round-trip + тест чтения формата.
3. Роуты 1:1 по `docs/inventory/auth.md`: `/api/auth/{state,register,login,logout,me}`,
   `PATCH /api/auth/me`, `POST /api/auth/me/api-key/rotate`, `GET /api/auth/me/usage`,
   `/api/admin/users`, `/api/admin/users/{id}`, `/api/admin/users/{id}/usage`.
4. Первый зарегистрированный — админ и сразу active; остальные — `pending` до одобрения.
5. Cookie `spark_ai_hub_session`: HttpOnly, SameSite=Lax, Path=/, `Secure` включается
   по `X-Forwarded-Proto: https` или по флагу конфига, TTL из конфига.
6. API-ключ `sah-<32 hex>`: в базе sha256, отдаётся один раз, ротация убивает старый.
7. Middleware: пропускает `/api/*`, `/ws/*`, `/v1/*`, `/mcp`, `/link/*` только с сессией
   или ключом; `/api/auth/{state,login,register}` и `/sah/*` — открыты; остальное —
   публично. Для `/mcp` и `/v1` дополнительно принимать OAuth-токен (Bearer) — если
   OAuth-сервер ещё не готов, оставь интерфейс `TokenResolver` и заглушку.
8. Писать `usage_events` при вызовах прокси и MCP: поля как в апстриме
   (`docs/inventory/auth.md`), включая `user_id`, `ts`, `route`, `model`, токены.
9. Уборка протухших сессий — раз в час, как janitor в `daemon/main.py:38-56`
   (ошибки писать в лог, не глотать молча).

## Границы

- Не трогать `internal/web/*`, `cmd/sahd/main.go`, `frontend/*`, `registry/*`.
- Экспортировать `func Register(r *web.Router, deps Deps)`; зависимости (`Deps`) —
  структура: БД, конфиг, логгер, `TokenResolver`.
- Зависимости уже в `go.mod` (`modernc.org/sqlite`, `golang.org/x/crypto`, `uuid`).
  Новые не добавлять без причины.
- Ошибки: те же коды и те же тексты, что в апстриме (`docs/inventory/auth.md`),
  тело `{"detail": "..."}`.

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- В `internal/auth` есть тесты через `httptest`: bootstrap первого админа, pending
  не пускает, login/logout, cookie-флаги, API-ключ и ротация, админ права, usage.
- Пункты 1-12 чек-листа из `docs/inventory/auth.md` покрыты тестами.
- Коммит в текущую ветку: `phase 1: store и auth`.
