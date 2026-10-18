# Задача: фаза 6 — файлы, медиа и короткие ссылки

Читай сначала: `docs/PLAN.md`, `docs/inventory/registry-media.md`, `docs/contract/schema.sql`.
Источник истины — `~/spark-ai-hub/daemon/routers/{uploads,files,links}.py`,
`daemon/services/{media_store,link_service}.py` (только чтение).

## Что сделать

Пакет `internal/media` + роуты `internal/api/files`.

1. `POST /api/uploads` — тело запроса сырое, не multipart (`uploads.py:36-42`), лимит
   размера как в апстриме, куда складывается файл, что возвращается.
2. `GET /api/media` — список своей медиатеки; `DELETE /api/media/{name}`;
   `PUT /api/media/{name}/thumb`; `GET /api/media/{name}/thumb` — превью, формат и
   кэш как в `media_store.py` (`_shape`, `_thumbs`), включая поведение при отсутствии
   превью.
3. Таблицы `media` и `media_links` — ровно схема `docs/contract/schema.sql`;
   `record()` не пишет при `user_id IS NULL` (воспроизвести), видимость по владельцу,
   админ видит всё.
4. `POST /link/upload/{token}` и `GET /link/download/{token}` — одноразовость, TTL,
   коды ошибок, что происходит со вторым обращением.
5. Уборка протухших ссылок — как janitor, раз в час (можно джобой в пакете).
6. Тесты: httptest на все эндпоинты, отдельно — одноразовость ссылки, лимит размера,
   видимость медиа между двумя пользователями.

## Границы

- Не трогать `cmd/sahd/main.go`, `internal/web/*`, `internal/auth/*`, `internal/registry/*`,
  `internal/dockerx/*`, `internal/api/{auth,recipes,system}`, `frontend/*`.
- Экспортировать `func Register(r *web.Router, deps Deps)`; `Deps` — конфиг, БД, логгер,
  текущий пользователь из контекста (его кладёт middleware в `internal/auth`).
- Зависимость `github.com/google/uuid` уже была в go.mod; если её нет — `go get`.

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- Коммит в текущую ветку: `phase 6: медиа и ссылки`.
