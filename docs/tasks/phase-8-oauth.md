# Задача: фаза 8 — OAuth для `/mcp`

Читай сначала: `docs/PLAN.md`, `docs/inventory/auth.md` (раздел про OAuth),
`docs/contract/openapi.json`, `~/spark-ai-hub/daemon/routers/oauth.py` (333 строки),
`daemon/services/oauth_service.py` (355 строк), `tests/test_oauth.py` (только чтение).

## Что сделать

Пакет `internal/oauth` + роуты `internal/api/oauthapi`.

1. `GET /.well-known/oauth-authorization-server`,
   `GET /.well-known/oauth-protected-resource`,
   `GET /.well-known/oauth-protected-resource/mcp` — те же поля, те же адреса внутри.
2. `POST /oauth/register` — динамическая регистрация клиента: те же проверки, лимит
   числа клиентов, ответ с `client_id`/`client_secret`.
3. `GET /oauth/authorize` — экран согласия; `POST /oauth/authorize` — решение
   (approve/deny) с редиректом и `code`, ошибки в тех же формах.
4. `POST /oauth/token` — обмен `authorization_code` и `refresh_token`, сроки жизни,
   `scope`; токен ходит как `Bearer` в `/mcp` (интеграция: `auth.TokenResolver` уже
   есть после фазы 1 — подключить, если он в виде заглушки).
5. Хранить `oauth_clients`, `oauth_codes`, `oauth_tokens` ровно по схеме
   `docs/contract/schema.sql`; отзыв токена при деактивации пользователя.
6. Тесты: полный цикл (register → authorize → token → refresh), истёкший код,
   неверный redirect_uri, отозванный пользователь.

## Границы

- Не трогать `cmd/sahd/main.go`, `internal/web/*`, `internal/api/{auth,recipes,system,containers,files,mcp,models}`,
  `internal/{registry,dockerx,media,mcp,modelproxy,proxy}`, `frontend/*`.
- Экспортировать `func Register(r *web.Router, deps Deps)`; `Deps` — конфиг, БД, логгер.

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- Коммит в текущую ветку: `phase 8: OAuth`.
