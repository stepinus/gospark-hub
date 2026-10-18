# auth / admin / oauth — контракт реализации

Источник истины: апстрим `~/spark-ai-hub` @ `6eb4372`. Все ссылки `file:line` — относительно
корня апстрима. Схема HTTP — `docs/contract/openapi.json` (22 операции в этой области).

Область: `daemon/main.py`, `daemon/config.py`, `daemon/db.py`, `daemon/middleware/auth.py`,
`daemon/routers/{auth,admin,oauth}.py`, `daemon/services/{auth_service,oauth_service}.py`,
клиентский контракт `frontend/src/{auth.js,pages/Login.jsx,pages/Account.jsx,pages/Users.jsx}`.

---

## 1. Сборка приложения и порядок старта

`FastAPI(title="Spark AI Hub", version="0.1.0", lifespan=lifespan)` — `daemon/main.py:72`.

Lifespan, строго в этом порядке (`daemon/main.py:60-69`):

1. `await init_db()` — создаёт схему и добивает недостающие колонки (`daemon/main.py:62`, `daemon/db.py:133-140`).
2. `load_recipes()` — реестр (`daemon/main.py:63`).
3. `await proxy_service.ensure_proxy()` — Caddy на публичном порту (`daemon/main.py:66`).
4. `asyncio.create_task(_check_running_readiness())` — через 2 c прогревает readiness запущенных рецептов (`daemon/main.py:35-41, 67`).
5. `asyncio.create_task(_session_janitor())` — уборка (`daemon/main.py:68`).

Janitor (`daemon/main.py:44-57`): бесконечный цикл, тело выполняется **сразу** при старте, затем
`sleep(3600)`. В теле: `purge_expired_sessions()` + `oauth_service.purge_expired()` в одном
`try/except Exception: pass`, отдельно `link_service.purge_expired()` — тоже с проглатыванием
исключений. Oшибки уборки наружу не выходят и в лог не пишутся.

### Middleware

```
app.add_middleware(CORSMiddleware, allow_origins=["*"], allow_methods=["*"], allow_headers=["*"])   # main.py:74-79
app.add_middleware(AuthMiddleware)                                                                  # main.py:84
```

Порядок важен: `AuthMiddleware` добавлен последним, значит оборачивает **снаружи** и выполняется
раньше CORS (`daemon/main.py:81-84`). Следствия для паритета:

- Ни один запрос под `/api`, `/ws`, `/v1`, `/mcp` не доходит до роутера без пользователя.
- `allow_credentials` не задан ⇒ `false` (`daemon/main.py:74-79`); при `allow_origins=["*"]`
  Starlette отвечает `access-control-allow-origin: *` и не пропускает cookie cross-origin.
- Preflight `OPTIONS /api/...` **тоже** уходит в auth-гейт: метода-исключения нет
  (`daemon/middleware/auth.py:120-137`), поэтому без сессии preflight получает 401 без
  CORS-заголовков. Фронтенд same-origin, на практике не задевает.

Порядок include роутеров (`daemon/main.py:87-105`): `auth`, `admin`, `recipes`, `containers`,
`system`, `mcp`, `uploads`, `files`, `links`, `oauth`, `anthropic_proxy`, `openai_proxy`.
`anthropic_proxy` объязан быть раньше `openai_proxy` (`daemon/main.py:102-104`).

### Статика и SPA

- `/sah/install.sh` — динамический, подставляет список кандидатов через `compute_connect_info`
  в `@SAH_CANDIDATES@` (первое вхождение), `media_type="text/x-shellscript"` (`daemon/main.py:114-128`).
- `/sah/version` — `PlainTextResponse` с sha256 файла `sah/sah` (`daemon/main.py:132-136`).
- `/sah` — `StaticFiles` (`daemon/main.py:138`), `/cover-sources` (`daemon/main.py:143-146`),
  `/assets` — из `frontend/dist/assets` (`daemon/main.py:149-152`).
- SPA-fallback `GET /{full_path:path}` (`daemon/main.py:154-173`), порядок решений:
  1. существующий файл в `frontend/dist` → `FileResponse` (`:158-159`);
  2. суффикс из `ASSET_SUFFIXES` (`.png .jpg .jpeg .webp .gif .svg .ico .avif .css .js .mjs .map .json .woff .woff2 .ttf`) → `404` пустым телом, **без** HTML (`:28-31, :164-165`);
  3. иначе `index.html` с `Cache-Control: no-cache` (`:170-172`), нет файла → `404`.
- `/.well-known/*` перехватывается роутером oauth раньше SPA (`daemon/routers/oauth.py:88-92`).

---

## 2. Конфигурация

`daemon/config.py:5-39`, pydantic-settings, env-префикс `SPARK_AI_HUB_` (`:36`).

| Поле | Env | Значение |
|---|---|---|
| `port` | `SPARK_AI_HUB_PORT` | `9010` (`:9`) |
| `public_port` | `SPARK_AI_HUB_PUBLIC_PORT` | `9000` (`:16`) |
| `auth_enabled` | `SPARK_AI_HUB_AUTH_ENABLED` | `true` (`:26`) |
| `session_cookie_name` | `SPARK_AI_HUB_SESSION_COOKIE_NAME` | `spark_ai_hub_session` (`:27`) |
| `session_ttl_days` | `SPARK_AI_HUB_SESSION_TTL_DAYS` | `30` (`:28`) |
| `force_secure_cookie` | `SPARK_AI_HUB_FORCE_SECURE_COOKIE` | `false` (`:34`) |
| `db_path` | `SPARK_AI_HUB_DB_PATH` | `data/spark-ai-hub.db` (`:21`) |

`settings.data_dir.mkdir(parents=True, exist_ok=True)` на импорте (`daemon/config.py:40`).

---

## 3. Схема SQLite (релевантные таблицы)

`daemon/db.py:6-115`; соединение — `get_db()` с `PRAGMA foreign_keys = ON` на каждое соединение
(`daemon/db.py:118-122`), это и включает каскады.

**users** (`:17-28`)

```
id INTEGER PK AUTOINCREMENT
email TEXT NOT NULL UNIQUE COLLATE NOCASE
name TEXT NOT NULL DEFAULT ''
password_hash TEXT NOT NULL
role TEXT NOT NULL DEFAULT 'user'      -- 'admin' | 'user'
status TEXT NOT NULL DEFAULT 'pending' -- 'pending' | 'active' | 'rejected'
api_key TEXT NOT NULL UNIQUE
created_at TEXT NOT NULL DEFAULT (datetime('now'))
reviewed_at TEXT
last_login_at TEXT
```

**sessions** (`:32-41`): `token_hash TEXT PK` (sha256 cookie-значения), `user_id` → users
`ON DELETE CASCADE`, `created_at`, `last_seen_at`, `expires_at NOT NULL`, `user_agent`, `ip`,
индекс `idx_sessions_user(user_id)`.

**usage_events** (`:45-56`): `id`, `user_id` → users CASCADE, `ts` (default `datetime('now')`),
`model`, `endpoint`, `prompt_tokens`, `completion_tokens`, `total_tokens`, `source`
(`'api_key' | 'session'`), индекс `idx_usage_user_ts(user_id, ts)`.

**oauth_clients** (`:61-66`): `client_id TEXT PK`, `client_name`, `redirect_uris` (JSON-массив
строкой), `created_at`.

**oauth_codes** (`:69-78`): `code_hash PK`, `client_id` (FK не объявлен), `user_id` → users
CASCADE, `redirect_uri`, `code_challenge`, `resource`, `scope`, `expires_at`.

**oauth_tokens** (`:79-89`): `token_hash PK`, `kind` (`'access'|'refresh'`), `client_id`,
`user_id` → users CASCADE, `resource`, `scope`, `created_at`, `expires_at`, индекс по `user_id`.

Связанное: `media` (`:96-103`) и `media_links` (`:107-114`) — тоже CASCADE от `users`, поэтому
удаление аккаунта уносит файлы, ссылки, usage, сессии, коды и токены. Строки `oauth_clients`
при удалении пользователя **не** чистятся.

Все временные метки — UTC-текст `YYYY-MM-DD HH:MM:SS` (совпадает с SQLite `datetime('now')`,
`daemon/services/auth_service.py:293-299`), сравнения строковые.

---

## 4. Middleware: что публично, что под ключом

`daemon/middleware/auth.py:116-137`. Работает только для `scope["type"]` ∈ {`http`, `websocket`}
и только при `settings.auth_enabled` (`:121-123`).

Публичные пути — **точное** совпадение (`:31-36`):

```
/api/auth/state
/api/auth/login
/api/auth/register
/api/auth/logout
```

Всё остальное, что `startswith` один из префиксов `("/api", "/ws", "/v1", "/mcp")`, требует
пользователя (`:38-39, 56-59`). Сравнение — сырой `str.startswith`, без границы сегмента:
`/apiary`, `/apidocs`, `/mcpx` тоже попадают под гейт.

API-ключ принимается только на (`:48-53`):

```
/v1...                        (все модели)
/mcp...                       (MCP-сервер медиа)
/api/system/connect           (единственный /api-путь, который зовёт sah CLI)
```

OAuth access-token — **только** на путях, начинающихся с `/mcp` (`:106-111`). Ни `/v1`, ни `/api`
его не принимают.

### Порядок резолва (`resolve_user`, `:89-113`)

1. Cookie `settings.session_cookie_name` → `auth_service.user_for_session(token)` (`:93-97`),
   источник `"session"`.
2. Заголовок ключа (`:99-105`) — `_bearer()` (`:62-77`) принимает, по порядку:
   `Authorization: Bearer <k>` (регистронезависимо, обрезает пробелы), затем `x-api-key`,
   затем `api-key`. Источник `"api_key"`, только если путь в списке выше.
3. Тот же ключ как OAuth access-token при `path.startswith("/mcp")` → источник `"oauth"` (`:106-111`).
4. Иначе `(None, "")`.

Результат кладётся в `scope["state"]`: `state["user"]`, `state["auth_source"]` (`:128-131`).
Ничего не логируется; аноним при `auth_enabled=true` и защищённом пути не доходит до хендлера.

### Отказ

WebSocket: `receive()` затем `websocket.close` c кодом **1008**, без accept (`:140-145`).

HTTP — всегда `401`, тело JSON, заголовки `content-type: application/json`,
`content-length`, `www-authenticate` (`:174-185`).

- `/v1*`: `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"Missing or invalid API key. Sign in to Spark AI Hub and copy your key from Account, or run the sah installer again."}}` (`:147-159`).
- прочее (включая `/api`, `/ws`, `/mcp`): `{"detail":"Authentication required."}` (`:160-161`).
- `www-authenticate: Bearer realm="Spark AI Hub"` (`:163`); для `/mcp` дописывается
  `, resource_metadata="{origin}/.well-known/oauth-protected-resource/mcp", scope="mcp"`
  (`:164-170`), а если в запросе вообще был bearer — ещё `, error="invalid_token"` (`:171-172`).
  `origin` — из `request_origin()` с fallback `scheme://netloc` (`:167-168`).

При `auth_enabled=false` middleware — no-op (`:121-123`): все пути открыты, `request.state.user`
не выставляется, и зависимость `current_user` даёт `401` на `/api/auth/me` и остальных
(`daemon/routers/auth.py:16-25`).

---

## 5. Cookie и сессия

Токен: `secrets.token_urlsafe(32)` → 43 символа URL-safe base64 (`daemon/services/auth_service.py:89-90`).
В БД только `sha256(token)` hex (`:93-94`).

Установка (`daemon/routers/auth.py:52-61`), параметры Starlette:

| Атрибут | Значение |
|---|---|
| имя | `settings.session_cookie_name` = `spark_ai_hub_session` |
| value | сырой токен |
| `Max-Age` | `session_ttl_days*24*3600` = `2592000` |
| `Expires` | `now + Max-Age` (Starlette выводит из `max_age`) |
| `Path` | `/` |
| `HttpOnly` | да |
| `SameSite` | `Lax` (`:58`, это и есть CSRF-защита) |
| `Secure` | `_is_https(request)` (`:59`) |
| `Domain` | не задаётся |

`_is_https` (`daemon/routers/auth.py:38-49`): `force_secure_cookie` → всегда `true`; иначе
`request_scheme(request) == "https"`. `request_scheme` (`daemon/services/connect_service.py:215-227`)
берёт `proto=` из RFC 7239 `Forwarded` (первый элемент, `_forwarded` `:205-212`), затем
`X-Forwarded-Proto` (до первой запятой, lower), иначе `request.url.scheme`. Значения вне
{`http`,`https`} игнорируются. Обратный прокси **обязан** присылать один из этих заголовков,
иначе за TLS-cookie не будет `Secure`.

Удаление (`:64-71`): `delete_cookie` с теми же `path/httponly/samesite/secure`, `Max-Age=0`,
пустое значение.

Создание сессии (`daemon/services/auth_service.py:302-320`): INSERT в `sessions`
(`expires_at = now + ttl_days`, `user_agent[:256]`, `ip[:64]`), UPDATE `users.last_login_at`,
`invalidate_caches()`. `ip` — `client_ip()`: `cf-connecting-ip` → `x-forwarded-for` (первый) →
`x-real-ip` → peer (`daemon/services/connect_service.py:157-169`).

Резолв (`:356-387`): SQL `JOIN users` с `s.expires_at > now_iso()`, затем отбрасывание
не-`active` (`:375-376`); при найденной строке UPDATE `last_seen_at` (`:377-382`). Кэш
`sessions`-резолва — 15 c по digest (`:103-105, 361-364`), поэтому `last_seen_at` обновляется
не чаще ~15 c.

Инвалидация: `_session_cache`/`_apikey_cache` чистятся целиком в `create_session`,
`destroy_sessions_for_user`, `update_user`, `delete_user` (`:108-116, 257, 268, 319, 342`);
`destroy_session` убирает только свой digest (`:332`). TTL кэша 15 c — верхняя граница
задержки отзыва, если кэш не сбросили.

Каскад сессий: `destroy_sessions_for_user` (`:335-342`) вызывается админом при переводе в
не-`active` и при смене пароля (`daemon/routers/admin.py:142-145`); при смене собственного
пароля/почты через `PATCH /api/auth/me` — **не** вызывается.

Уборка: `purge_expired_sessions()` — `DELETE FROM sessions WHERE expires_at < now_iso()`
(`:345-353`), раз в час из janitor.

Несколько входов не дедуплицируются: каждый `login` создаёт новую строку `sessions`.

---

## 6. Пароли, API-ключи, кэши

Пароль (`daemon/services/auth_service.py:37-62`):

```
pbkdf2_sha256$260000$<salt_hex32>$<dk_hex>
```
`hashlib.pbkdf2_hmac("sha256", password, salt, 260000)`, соль `secrets.token_hex(16)`,
сравнение `hmac.compare_digest`. Формат обязан совпадать байт в байт, иначе БД живого хаба не
откроется.

Валидация: email — `^[^@\s]+@[^@\s]+\.[^@\s]+$` (`:40, 65-69`), пароль — минимум 8 символов
(`:41, 72-77`). Сообщения: `"Enter a valid email address."`,
`"Password must be at least 8 characters."`, `"An account with that email already exists."`
(`:68, 75, 208, 247`).

API-ключ (`:82-86`): `"sah-" + secrets.token_urlsafe(32)` → 47 символов, хранится **в открытом
виде** (обоснование — `:15-19`), `UNIQUE`. Один ключ на аккаунт на все модели. Резолв:
`SELECT * FROM users WHERE api_key = ? AND status = 'active'` (`:390-409`), кэш 15 c по сырому
ключу.

Ротация: `POST /api/auth/me/api-key/rotate` → `update_user(id, api_key=new_api_key())`
(`daemon/routers/auth.py:225-231`), старый ключ умирает немедленно через `invalidate_caches()`
(`daemon/services/auth_service.py:257`). Сессии и OAuth-токены при ротации не трогаются.

---

## 7. Первый пользователь = админ

Правило позиционное и применяется **только** когда `role` и `status` не переданы
(`daemon/services/auth_service.py:196-200`):

```python
if role is None or status is None:
    first = await needs_setup()          # COUNT(*) == 0
    role   = role   or ("admin"  if first else "user")
    status = status or ("active" if first else "pending")
```

`needs_setup()` — `user_count() == 0` (`:132-144`). Проверка и INSERT сериализованы
`asyncio.Lock` (`:169-172, 192-193`), чтобы два одновременных запроса не создали двух админов.

При `status != 'pending'` сразу пишется `reviewed_at = now` (`:209`).

Админский `POST /api/admin/users` всегда передаёт `role` и `status` явно (дефолты `user`/`active`,
`daemon/routers/admin.py:20-27, 98-101`), поэтому **первый аккаунт, созданный админом через API,
админом не становится** — правило первого пользователя обходится.

---

## 8. Эндпоинты: `/api/auth`

Общие ошибки:

- middleware `401 {"detail":"Authentication required."}` для всех, кроме четырёх публичных;
- `current_user` (`daemon/routers/auth.py:16-25`) — `401` с тем же телом, если пользователь не
  выставлен (актуально при `auth_enabled=false`);
- `require_admin` (`:28-33`) — `403 {"detail":"Only an administrator can do that."}`;
- pydantic-валидация тела — `422 {"detail":[{"type","loc","msg","input",...}]}` (стандартный
  обработчик FastAPI);
- `HTTPException` → `{"detail": "<строка>"}`.

### 8.1 `GET /api/auth/state` — публичный

`daemon/routers/auth.py:96-108`. Тело запроса нет. Cookie резолвится, но не требуется.

```json
{"auth_enabled": true, "needs_setup": false, "user": null}
```

`user` — `public_user()` (`daemon/services/auth_service.py:121-129`), ровно 8 полей:
`id, email, name, role, status, created_at, reviewed_at, last_login_at`. **Без `api_key`.**

Три состояния:

| Кто | `needs_setup` | `user` |
|---|---|---|
| аноним, аккаунтов нет | `true` | `null` |
| аноним, аккаунты есть | `false` | `null` |
| pending | `false` | `null` |
| active | `false` | объект с `role`/`status` |
| rejected | `false` | `null` |
| `auth_enabled=false` | по факту БД | всегда `null` |

Pending никогда не отдаётся как `user`: `user_for_session` отбрасывает не-`active`
(`daemon/services/auth_service.py:375-376`), а сессию pending-аккаунт получить не может
(см. 8.2/8.3), поэтому его cookie невалидна и запрос = анонимный. Ответ всегда `200`.

### 8.2 `POST /api/auth/register` — публичный

`daemon/routers/auth.py:111-140`.

Тело (`Credentials`, `:76-79`):

| Поле | Тип | Обяз. | Дефолт |
|---|---|---|---|
| `email` | string | да | — |
| `password` | string | да | — |
| `name` | string | нет | `""` |

Первый аккаунт (`status == "active"`, `:126`):

- `200 {"status":"active","user":{8 полей}}`;
- побочно: INSERT `users` (`role='admin'`, `status='active'`, `reviewed_at=now`, новый `api_key`),
  INSERT `sessions`, UPDATE `users.last_login_at`, `Set-Cookie` (`:134-140`).

Последующие (`:126-132`):

- `200 {"status":"pending","message":"Your account was created and is waiting for an administrator to approve it.","user":{...}}`
  (строка склеена из `:129-130`);
- cookie **не** ставится, сессия **не** создаётся;
- побочно: INSERT `users` c `status='pending'`, `reviewed_at=NULL`.

Ошибки:

- `400 {"detail":"Enter a valid email address."}` / `"Password must be at least 8 characters."` /
  `"An account with that email already exists."` (`:123-124`, источники — сервис);
- `422` при отсутствии `email`/`password`.

Проверка дубликата — `SELECT 1 ... COLLATE NOCASE` внутри `_create_user_locked`
(`daemon/services/auth_service.py:204-208`), плюс UNIQUE в схеме.

### 8.3 `POST /api/auth/login` — публичный

`daemon/routers/auth.py:143-169`. Тело (`LoginBody`, `:82-85`): `email` (string, обяз.),
`password` (string, обяз.).

Порядок проверок:

1. нет пользователя **или** неверный пароль → `401 {"detail":"Incorrect email or password."}`
   (одно сообщение на оба случая, `:146-151`);
2. `status == "pending"` → `403 {"detail":"Your account is waiting for an administrator to approve it."}` (`:153-157`);
3. `status != "active"` (т.е. `rejected`) → `403 {"detail":"This account has been disabled."}` (`:158-161`);
4. успех → `200 {"status":"active","user":{8 полей}}` + `Set-Cookie`;
   побочно INSERT `sessions` (новая строка на каждый вход) и UPDATE `last_login_at` (`:163-169`).

Проверка статуса идёт **после** проверки пароля: неверный пароль у pending-аккаунта даёт 401, а
не 403.

### 8.4 `POST /api/auth/logout` — публичный

`daemon/routers/auth.py:172-178`. Тело не читается. Если cookie есть —
`destroy_session(token)` (DELETE по `token_hash`, `:323-332`); затем cookie удаляется.
Ответ всегда `200 {"status":"signed_out"}`, даже без cookie или с протухшей.

### 8.5 `GET /api/auth/me` — требуется сессия или... только сессия

`daemon/routers/auth.py:181-188`. Зависимость `current_user` срабатывает только при наличии
`request.state.user`. На `/api` ключ не принимается (он разрешён лишь для `/api/system/connect`,
`daemon/middleware/auth.py:49-53`), а OAuth-токен — только на `/mcp` (`:106-111`), поэтому на
практике вход в этот эндпоинт возможен **только** с cookie-сессией; при `auth_enabled=false`
`request.state.user` не выставляется вовсе и ответ — `401`.

Ответ:

```json
{...8 полей public_user..., "api_key": "sah-...", "usage": {
  "requests": 0, "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
  "last_used_at": null, "requests_30d": 0, "tokens_30d": 0, "requests_24h": 0, "tokens_24h": 0}}
```

`usage` — `usage_summary(user_id)[id]` или `EMPTY_USAGE` (`daemon/services/auth_service.py:484-488`).
`api_key` отдаётся в открытом виде.

`401 {"detail":"Authentication required."}` без сессии.

### 8.6 `PATCH /api/auth/me`

`daemon/routers/auth.py:191-222`. Тело (`UpdateMeBody`, `:87-91`), все поля необязательны,
`string | null`: `name`, `email`, `current_password`, `new_password`.

Логика:

- `name` применяется всегда, если не `null` (`:201-202`);
- «чувствительные» изменения = `email` или `new_password` (`:204`); при них обязателен
  корректный `current_password`, иначе `403 {"detail":"Your current password is incorrect."}`
  (`:205-211`);
- `new_password` уходит в `update_user(..., password=...)` → `validate_password` (мин. 8) →
  при коротком `400 {"detail":"Password must be at least 8 characters."}` (`:214-220`);
- дубликат почты → `400 {"detail":"An account with that email already exists."}`;
- невалидная почта → `400 {"detail":"Enter a valid email address."}`.

Ответ: только 8 полей `public_user` — **без** `api_key` и `usage` (`:222`). Клиент это учитывает:
`Account.jsx:46` мержит ответ в текущий объект.

Побочно: UPDATE `users` (`name`/`email`/`password_hash`), `invalidate_caches()`
(`daemon/services/auth_service.py:249-257`). Сессии **не** удаляются, `reviewed_at` не меняется.

Пустое тело `{}` → `200` с текущим пользователем без изменений (`daemon/services/auth_service.py:235-237`).

### 8.7 `POST /api/auth/me/api-key/rotate`

`daemon/routers/auth.py:225-231`. Тело не нужно. `200 {"api_key":"sah-..."}`.
Побочно: UPDATE `users.api_key`, сброс кэшей. Сессии живы, старый ключ невалиден сразу.

### 8.8 `GET /api/auth/me/usage`

`daemon/routers/auth.py:234-240`. `200 {"usage": {...9 полей...}, "models": [{"model","requests","total_tokens"}, ...]}`
— `top_models(user_id)` (`daemon/services/auth_service.py:491-505`): до 5 моделей, `model != ''`,
`ORDER BY requests DESC`.

---

## 9. Эндпоинты: `/api/admin` (все — `require_admin`)

`daemon/routers/admin.py:15`. Любой не-админ, включая активного пользователя, получает
`403 {"detail":"Only an administrator can do that."}` (`daemon/routers/auth.py:28-33`); аноним —
`401` от middleware.

Сортировка списка: `0` для `pending`, затем по убыванию `usage.total_tokens`, затем
`email.lower()` (`daemon/routers/admin.py:74-80`).

### 9.1 `GET /api/admin/users`

`daemon/routers/admin.py:47-91`.

```json
{
  "users": [{...8 полей..., "usage": {9 полей}, "storage": {"bytes": 0, "files": 0}}],
  "totals": {"users": 2, "pending": 1, "active": 1, "requests": 0, "total_tokens": 0, "storage_bytes": 0},
  "models": [{"model": "...", "requests": 0, "total_tokens": 0}]
}
```

`storage` — из `media_store.space_by_user()` (`daemon/services/media_store.py:215-225`):
`{"bytes","files"}`; ключ `None` (файлы без владельца) в `users` не попадает, но входит в
`totals.storage_bytes` (`daemon/routers/admin.py:88-89`). `usage` отсутствующего — `EMPTY_USAGE`.

### 9.2 `POST /api/admin/users` → `201`

`daemon/routers/admin.py:94-108`. Тело (`CreateUserBody`, `:20-27`):

| Поле | Тип | Обяз. | Дефолт |
|---|---|---|---|
| `email` | string | да | — |
| `password` | string | да | — |
| `name` | string | нет | `""` |
| `role` | string | нет | `"user"` |
| `status` | string | нет | `"active"` |

`_validate_enums` (`:38-44`):
`400 {"detail":"Role must be admin or user."}`,
`400 {"detail":"Status must be pending, active or rejected."}`.

Ответ `201`: 8 полей + `"usage": EMPTY_USAGE` + `"storage": {"bytes":0,"files":0}` (`:104-108`).
Побочно: INSERT `users` с заданными ролью/статусом, `reviewed_at=now` при `status != 'pending'`.
Правило первого пользователя не действует (см. §7).

### 9.3 `PATCH /api/admin/users/{user_id}`

`daemon/routers/admin.py:111-151`. `user_id: int`. Тело (`UpdateUserBody`, `:30-35`), все
`string | null`: `name`, `email`, `password`, `role`, `status`.

Порядок:

1. `_validate_enums` — `400` как выше. Выполняется **до** поиска пользователя, поэтому
   невалидный `role` + несуществующий id → `400`, а не `404` (`:116`).
2. нет пользователя → `404 {"detail":"No such account."}` (`:117-119`).
3. Защита последнего админа (`:121-131`): если цель — `role=admin` **и** `status=active`, и
   запрос снимает админство (`role="user"` либо `status ∈ {pending, rejected}`), и
   `admin_count(exclude_id=user_id) == 0` →
   `400 {"detail":"This is the only administrator — promote someone else first."}`.
   `admin_count` считает только `role='admin' AND status='active'` (`daemon/services/auth_service.py:271-288`).
4. `fields` = непустые значения тела; смена `status` на другое значение проставляет
   `reviewed_at = now_iso()` (`:133-135`); `password` → `password_hash` с валидацией ≥8
   (`daemon/services/auth_service.py:231-234`) → `400 "Password must be at least 8 characters."`.
5. UPDATE `users`, `invalidate_caches()`.
6. Если итоговый `status != 'active'` **или** в запросе был `password` —
   `destroy_sessions_for_user(user_id)` (`:144-145`).

Ответ `200`: 8 полей + `"usage"` (без `storage`) (`:147-151`).

### 9.4 `DELETE /api/admin/users/{user_id}`

`daemon/routers/admin.py:154-172`.

- нет пользователя → `404 {"detail":"No such account."}`;
- свой id → `400 {"detail":"You cannot delete your own account."}`;
- удаляемый — единственный активный админ (и это не сам вызывающий, что уже отсечено) →
  `400 {"detail":"This is the only administrator — promote someone else first."}`;
- успех → `200 {"status":"deleted"}`.

Побочно: `DELETE FROM users WHERE id = ?` (`daemon/services/auth_service.py:261-268`) + каскады
по FK: `sessions`, `usage_events`, `media`, `media_links`, `oauth_codes`, `oauth_tokens`.
История usage исчезает — так и написано в подтверждении UI (`frontend/src/pages/Users.jsx:124-126`).
`oauth_clients` остаются.

### 9.5 `GET /api/admin/users/{user_id}/usage`

`daemon/routers/admin.py:175-183`. Нет пользователя → `404 {"detail":"No such account."}`.
Иначе `200 {"usage": {9 полей}, "models": [...]}`.

---

## 10. usage_events: что и когда пишется

Единственный писатель — `record_usage` (`daemon/services/auth_service.py:414-446`), вызывается
из `record_call` прокси (`daemon/routers/openai_proxy.py:230-248`):

- `user_id` — вызывающий; анонимные вызовы (возможны только при `auth_enabled=false`) не
  логируются (`openai_proxy.py:236-238`);
- `model` — из usage-блока ответа, `""` если нет;
- `endpoint` — `f"openai:{path.strip('/')}"` (`openai_proxy.py:402`) либо `"anthropic:messages"`
  (`daemon/routers/anthropic_proxy.py:464, 487`);
- `prompt_tokens`, `completion_tokens`, `total_tokens` — из usage; при `total_tokens == 0`
  подставляется сумма (`:429-430`);
- `source` — `request.state.auth_source` (`openai_proxy.py:247`), т.е. `"api_key"` или `"session"`;
  при `auth_enabled=false` — `""`;
- `ts` — дефолт SQLite `datetime('now')`.

Запись никогда не роняет запрос: любые исключения глотаются (`:431-446`). `/mcp` usage не пишет.

Агрегат (`usage_summary`, `:449-481`) отдаёт по пользователю:
`requests`, `prompt_tokens`, `completion_tokens`, `total_tokens`, `last_used_at`,
`requests_30d`, `tokens_30d`, `requests_24h`, `tokens_24h`; `SUM(...)` с `NULL` приводится к `0`
(`:480-481`), `last_used_at` может быть `null`.

---

## 11. OAuth для `/mcp`

Константы (`daemon/services/oauth_service.py:39-46`): `SCOPE = "mcp"`, `ACCESS_TTL = 3600`,
`REFRESH_TTL = 30*24*3600`, `CODE_TTL = 600`, `MAX_CLIENTS = 5000`, `CIMD_TTL = 600`,
`CIMD_MAX_BYTES = 64*1024`, loopback-хосты `127.0.0.1, localhost, [::1], ::1`.
`REQUEST_TTL = 600` для страницы согласия (`daemon/routers/oauth.py:30`).

Все `/oauth/*` и `/.well-known/*` — вне guarded-префиксов, т.е. публичны (`daemon/middleware/auth.py:38-39`),
но `request.state.user`/`auth_source` всё равно заполняются для всех запросов (`:126-131`).

Origin для всех документов вычисляется из запроса: `request_origin()` (RFC 7239 `Forwarded` →
`X-Forwarded-Host` → `Host`, валидация `^https?://(host|\[ip\])(:port)?$`,
`daemon/services/connect_service.py:202, 230-246`), fallback `scheme://netloc`
(`daemon/routers/oauth.py:39-40`). Один и тот же хаб — валидный issuer на любом своём адресе.

### 11.1 `GET /.well-known/oauth-protected-resource` и `GET /.well-known/oauth-protected-resource/mcp`

Один обработчик на два пути (`daemon/routers/oauth.py:56-66`), `200`:

```json
{"resource": "{origin}/mcp", "authorization_servers": ["{origin}"],
 "scopes_supported": ["mcp"], "bearer_methods_supported": ["header"],
 "resource_name": "Spark AI Hub"}
```

`Cache-Control` **не** ставится (в отличие от `/oauth/*`).

### 11.2 `GET /.well-known/oauth-authorization-server`

`daemon/routers/oauth.py:69-85`, `200`:

```json
{"issuer": "{origin}",
 "authorization_endpoint": "{origin}/oauth/authorize",
 "token_endpoint": "{origin}/oauth/token",
 "registration_endpoint": "{origin}/oauth/register",
 "scopes_supported": ["mcp"],
 "response_types_supported": ["code"],
 "response_modes_supported": ["query"],
 "grant_types_supported": ["authorization_code", "refresh_token"],
 "code_challenge_methods_supported": ["S256"],
 "token_endpoint_auth_methods_supported": ["none"],
 "client_id_metadata_document_supported": true,
 "authorization_response_iss_parameter_supported": true}
```

### 11.3 `GET|HEAD /.well-known/{rest}`

`daemon/routers/oauth.py:88-92`: `404 {"error":"not_found"}`. Нужен, чтобы SPA-fallback не отдал
HTML с `200` на неизвестный discovery-путь. Зарегистрирован после двух предыдущих, поэтому те
выигрывают.

### 11.4 `POST /oauth/register` (RFC 7591, public client)

`daemon/routers/oauth.py:98-111`; логика — `daemon/services/oauth_service.py:114-151`.

Тело — JSON-объект; читаются `redirect_uris` (list[str], обяз.), `token_endpoint_auth_method`
(дефолт `"none"`), `client_name` (обрезается до 100). Прочие поля игнорируются.

Ошибки (тело `{"error": ..., "error_description": ...}`):

| Условие | Код | `error` |
|---|---|---|
| тело не JSON-объект | `400` | `invalid_client_metadata`, `"Body must be a JSON object."` (`oauth.py:105-106`) |
| `redirect_uris` не непустой list[str] | `400` | `invalid_redirect_uri`, `"redirect_uris must be a non-empty list of URLs."` (`oauth_service.py:117-118`) |
| URI не https и не http-loopback | `400` | `invalid_redirect_uri`, `"{uri} must be https, or http on a loopback address."` (`:119-122`) |
| `token_endpoint_auth_method != "none"` | `400` | `invalid_client_metadata`, `"Only public clients are supported (token_endpoint_auth_method=none)."` (`:123-126`) |
| ≥ 5000 клиентов | `503` | `invalid_client_metadata`, `"This Hub has too many registered clients."` (`:129-134`) |

Валидация redirect URI (`:80-90`): без фрагмента, обязателен hostname; `https` — любой; `http`
— только loopback.

Успех `201`, заголовки `Cache-Control: no-store`, `Pragma: no-cache` (`oauth.py:47-50, 111`):

```json
{"client_id": "sah-client-…", "client_id_issued_at": 1760000000, "client_name": "",
 "redirect_uris": ["https://claude.ai/api/mcp/auth_callback"],
 "grant_types": ["authorization_code", "refresh_token"], "response_types": ["code"],
 "token_endpoint_auth_method": "none"}
```

`client_id = "sah-client-" + secrets.token_urlsafe(18)`. Секрета нет. Побочно: INSERT
`oauth_clients`.

### 11.5 Client ID Metadata Document

Если `client_id` начинается с `https://`, он трактуется как URL метаданных клиента
(`daemon/services/oauth_service.py:154-201`): GET без редиректов, таймаут 5 c, тело ≤ 64 KiB,
все resolved-адреса хоста должны быть глобальными (`web_service.is_public`,
`daemon/services/web_service.py:52-62`), документ обязан содержать `client_id`, равный этому
URL, и валидный непустой `redirect_uris`. Иначе клиент считается несуществующим. Кэш — 600 c,
в том числе отрицательный. В БД такие клиенты не пишутся.

### 11.6 `POST /oauth/token`

`daemon/routers/oauth.py:122-150`.

Требования: `Content-Type` содержит `application/x-www-form-urlencoded`, иначе
`400 {"error":"invalid_request","error_description":"Use application/x-www-form-urlencoded."}`.
Тело парсится `parse_qs(keep_blank_values=True)`, берётся первое значение (`:117-119`).
Читаются: `grant_type`, `client_id`, `code`, `redirect_uri`, `code_verifier`,
`refresh_token`, `resource`.

Порядок:

1. `client_id` не резолвится (ни БД, ни CIMD) → `401 {"error":"invalid_client","error_description":"Unknown client_id."}` (`:131-132`).
2. `grant_type=authorization_code` → `exchange_code`; `refresh_token` → `refresh`;
   иначе `400 {"error":"unsupported_grant_type","error_description":"grant_type 'x' is not supported."}` (`:133-147`).
3. Ответ всегда с `Cache-Control: no-store`, `Pragma: no-cache` (`:149-150`).

`exchange_code` (`daemon/services/oauth_service.py:259-283`), порядок проверок и тексты:

| Условие | `error` | Сообщение |
|---|---|---|
| код отсутствует/уже использован | `invalid_grant` | `The authorization code is invalid or was already used.` |
| `expires_at < now` | `invalid_grant` | `The authorization code has expired.` |
| другой `client_id`/`redirect_uri` | `invalid_grant` | `The code was issued to a different client or redirect_uri.` |
| PKCE не сходится | `invalid_grant` | `PKCE verification failed.` |
| `resource` не совпал | `invalid_target` | `resource does not match the authorization request.` |
| пользователь больше не active | `invalid_grant` | `The account is no longer active.` |

Все, кроме `invalid_target`, — `400` (дефолт `OAuthError`). Код **удаляется из БД до проверок**
(`:267-269`), поэтому неудачная попытка сжигает код.

PKCE (`:207-211`): verifier длиной 43..128, `BASE64URL(SHA256(verifier))` без `=`,
сравнение `hmac.compare_digest`.

`refresh` (`:286-307`): refresh-токен удаляется до проверок (ротация, `:295-296`);
`invalid_grant` — `The refresh token is invalid, expired or already used.` /
`The refresh token was issued to a different client.` / `The account is no longer active.`;
`invalid_target` — `resource does not match the original grant.`.

Успешный ответ (`_issue`, `:232-250`), `200`:

```json
{"access_token": "sah-oat-…", "token_type": "Bearer", "expires_in": 3600,
 "refresh_token": "sah-ort-…", "scope": "mcp"}
```

Побочно: два INSERT в `oauth_tokens` (`access` — 3600 c, `refresh` — 30 дней). Формат токенов:
`sah-oat-`/`sah-ort-` + `secrets.token_urlsafe(32)` (`:233-234`).

Резолв токена (`:310-332`): JOIN users, `kind='access'`, `expires_at > now`, `u.status='active'`;
кэш 15 c по digest.

### 11.7 `GET /oauth/authorize`

`daemon/routers/oauth.py:238-285`. Параметры query: `client_id`, `redirect_uri`,
`response_type`, `code_challenge`, `code_challenge_method`, `state`, `resource`.

Шаги:

1. Клиент неизвестен → HTML `400` (страница «Can't connect») с текстом
   `This app is not registered with the Hub. Remove the connector and add it again.` (`:241-243`).
2. `redirect_uri`: если не передан, берётся единственный зарегистрированный; иначе должен
   совпасть с зарегистрированным (`redirect_matches`, `oauth_service.py:93-108` — точное
   совпадение, либо http-loopback с любым портом при совпадении host/path/query). Не совпал →
   HTML `400` `The app asked to return to an address it did not register.` — **без** редиректа
   (`:245-250`).
3. Дальнейшие ошибки — редирект `302` на `redirect_uri` с `error`, `error_description`,
   `state`, `iss={origin}` (`:255-258`):
   - `response_type != "code"` → `unsupported_response_type`, `Only response_type=code is supported.`;
   - нет `code_challenge` или `code_challenge_method != "S256"` → `invalid_request`,
     `PKCE with code_challenge_method=S256 is required.`;
   - `resource` (после `rstrip("/")`) не равен `{origin}/mcp` → `invalid_target`,
     `The only resource here is {origin}/mcp.`
4. `_prune_pending()` (`:232-235`) выбрасывает записи старше 600 c; создаётся `request_id`
   (`secrets.token_urlsafe(24)`) и запись в памяти процесса (`_pending`, `:36`, `:268-284`) с
   `client_id`, `client_name` (`"An app"` если пусто), `redirect_uri`, `code_challenge`,
   `resource`, `scope="mcp"`, `state`, `origin`, `created`, `session` (digest cookie, если
   вошёл сессией), `user_id`.
5. Если запрос пришёл с cookie-сессией (`auth_source == "session"`) → страница согласия `200`;
   иначе — страница входа `200` (`:270-285`).

HTML-страницы (`_page`, `:163-189`): `X-Frame-Options: DENY`,
`Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'`,
`Referrer-Policy: no-referrer`, `Cache-Control: no-store`, `Pragma: no-cache`. Вход и согласие —
inline-стили, формы `POST /oauth/authorize`.

`_pending` живёт только в памяти процесса: рестарт или второй инстанс ломают начатую авторизацию.

### 11.8 `POST /oauth/authorize`

`daemon/routers/oauth.py:288-333`. Тело — form-urlencoded (`request_id`, `action`,
`email`, `password`).

- `request_id` неизвестен или старше 600 c → HTML `400` `This sign-in request expired. Start connecting again from the app.` (`:291-295`);
- `action == "login"` (`:298-312`):
  - неверные креды → страница входа с `Incorrect email or password.`, статус `401`;
  - `status != "active"` → `401`, текст `This account is not active yet.`;
  - успех → создаётся сессия (INSERT `sessions` + `last_login_at`), `pending["session"]` и
    `pending["user_id"]` привязываются к ней, ставится cookie, отдаётся страница согласия `200`;
- иначе (`allow`/`deny`) требуется: пользователь из сессии, `pending["session"]` не `None`,
  digest cookie совпадает, `user.id == pending["user_id"]`; иначе `403` HTML
  `Sign in on this page to approve the connection.` (`:314-317`);
- любое действие, кроме `allow` (включая отсутствующий `action`) → запись удаляется,
  `303` на `redirect_uri` c `error=access_denied`, `error_description=The user denied access.`,
  `state`, `iss` (`:319-324`);
- `allow` → `create_code` (INSERT `oauth_codes`, TTL 600) и `303` на `redirect_uri` c
  `code`, `state`, `iss` (`:326-333`).

`_redirect_with` (`:156-160`) сохраняет уже имеющиеся query-параметры и добавляет новые;
параметры со значением `None` отбрасываются (поэтому при отсутствии `state` его не будет).
Код/ошибка всегда уходят в query, несмотря на `response_modes_supported: ["query"]`.

Защита от CSRF — `SameSite=Lax` на cookie (`daemon/routers/auth.py:58`) плюс привязка записи
к digest сессии.

Протухание: `oauth_service.purge_expired()` в janitor удаляет `oauth_codes` и `oauth_tokens` с
`expires_at < now` (`daemon/services/oauth_service.py:347-355`). `revoke_user_tokens` (`:335-344`)
объявлен, но нигде не вызывается — отзыв обеспечивается фильтром `u.status='active'` в
`user_for_access_token` (`:324`) и `_active_user` в `exchange_code`/`refresh`.

---

## 12. Клиентский контракт (что ожидает фронтенд)

`frontend/src/auth.js`:

- `bootstrap()` — `GET /api/auth/state`, читает ровно `auth_enabled`, `needs_setup`, `user`
  (`:26-39`); `loading` гасится и при сетевой ошибке (`:36-38`).
- `login()` — POST JSON `{email, password}`; ошибки читаются как `body.detail || body.message`
  (`:3-10, :47-57`).
- `register()` — POST JSON `{email, password, name}`; при `data.status === "pending"` показывает
  `data.message` и **не** ставит `user` (`:59-73`).
- `logout()` — POST без тела, локальный стейт чистится в `finally` (`:75-81`).
- `refreshUser()` — `GET /api/auth/me`, мержит ответ в текущего пользователя (`:85-91`).
- 401 трактуется как «сессия истекла» → `clearSession()` (`:41-45`).

`frontend/src/pages/Login.jsx:24-25` — режим `setup` включается по `needs_setup`; `:68` —
текст «первый аккаунт владеет хабом».

`frontend/src/pages/Account.jsx`: `GET /api/auth/me` при монтировании (`:15`), карточки
api_key/usage/profile/password (`:44-48`); usage читает `requests`, `total_tokens`,
`tokens_30d`, `tokens_24h` (`:118-128`); ротация — `POST /api/auth/me/api-key/rotate`,
использует `data.api_key` (`:67-82`); профиль — `PATCH /api/auth/me` с `name` и, при смене
почты, `email` + `current_password` (`:142-164`); пароль — `PATCH` с
`current_password` + `new_password` (`:198-217`).

`frontend/src/pages/Users.jsx`: список — `GET /api/admin/users`, `403` → «Only an administrator
can view this page.» (`:17-25`); читает `data.users[].status/role/usage/storage`,
`data.totals.{users,pending,requests,total_tokens,storage_bytes}` (`:32-58, :150-160`); одобрение
— `PATCH {status:"active"}`, отклонение — `{status:"rejected"}`, смена роли — `{role:...}`,
сброс пароля — `{password}` , удаление — `DELETE` (`:104-137, :171-182, :221-230, :252-256`);
создание — `POST {name,email,password,role,status:"active"}` (`:290-305`). Все ошибки читаются
как `detail` (`:115, :132, :259, :301`).

---

## 13. Риски и открытые вопросы

1. `docs/PLAN.md:49` называет bcrypt, апстрим использует `pbkdf2_sha256$260000$…`
   (`daemon/services/auth_service.py:44-49`). Для совместимости с БД живого хаба нужен именно
   PBKDF2-HMAC-SHA256 в том же формате.
2. `frontend/src/pages/Account.jsx:93` в коммите содержит литерал `{shown ? apiKey : ***` —
   файл в таком виде не компилируется. Похоже на артефакт редакции в снимке репозитория; при
   копировании фронтенда «как есть» это надо проверить по `frontend/dist` или живому хабу.
3. `_pending` в памяти процесса (`daemon/routers/oauth.py:36`) — OAuth-авторизация не переживает
   рестарт и не работает при нескольких инстансах. Для этапа 1 это паритет, но потолок известен.
4. Нет rate-limit и блокировки после N неудач на `POST /api/auth/login`
   (`daemon/routers/auth.py:143-169`) — перебор пароля ничем не ограничен.
5. `PATCH /api/auth/me` при смене пароля не убивает остальные сессии
   (`daemon/routers/auth.py:191-222`) — асимметрия с админским сбросом пароля
   (`daemon/routers/admin.py:144-145`).
6. `revoke_user_tokens` (`daemon/services/oauth_service.py:335-344`) — мёртвый код: при
   `status=rejected` строки `oauth_tokens` остаются, доступ отсекается только на резолве.
7. Кэши резолва живут 15 c (`daemon/services/auth_service.py:103-105`,
   `daemon/services/oauth_service.py:48-50`): отзыв, не прошедший через `invalidate_caches()`,
   действует с задержкой до 15 c.
8. CORS `allow_origins=["*"]` без credentials (`daemon/main.py:74-79`) — фактически «любой
   origin может читать ответы, но не с cookie». Менять нельзя: контракт.
9. Env-префикс апстрима `SPARK_AI_HUB_` (`daemon/config.py:36`) против `SAH_` в плане Go-порта
   (`docs/PLAN.md:26-28`) — нужна явная таблица соответствия, иначе деплой разъедется.
10. Сравнение времени везде строковое по UTC-тексту `YYYY-MM-DD HH:MM:SS`; в Go нельзя
    подставлять `time.Time`/RFC3339 в те же колонки.
11. `oauth_clients` не чистится никогда, только лимит 5000 (`daemon/services/oauth_service.py:43, 129-134`).
12. Janitor глотает исключения молча (`daemon/main.py:47-56`) — сбой уборки не виден в логах.

---

## 14. Parity checklist

Каждый пункт — проверяемое HTTP-утверждение против живого хаба (или `sahd`). `$(H)` — базовый URL,
`$(JAR)` — cookie-jar.

1. `GET $(H)/api/auth/state` на пустой БД → `200`, `needs_setup == true`, `user == null`, `auth_enabled == true`.
2. `POST $(H)/api/auth/register` c `{"email":"a@x.io","password":"secret123","name":"A"}` на пустой БД → `200`, `status == "active"`, `user.role == "admin"`, `user.status == "active"`, `user.reviewed_at != null`, тело содержит ровно 8 полей пользователя и ни одного `api_key`.
3. Тот же ответ ставит `Set-Cookie: spark_ai_hub_session=…` c `HttpOnly`, `SameSite=Lax`, `Path=/`, `Max-Age=2592000`.
4. Тот же запрос с `X-Forwarded-Proto: https` → в `Set-Cookie` присутствует `Secure`; без заголовка (plain HTTP) — отсутствует.
5. `SPARK_AI_HUB_FORCE_SECURE_COOKIE=true` + запрос без `X-Forwarded-Proto` → `Secure` присутствует.
6. `GET $(H)/api/auth/state` с cookie админа → `200`, `user.email == "a@x.io"`, `user.role == "admin"`, `needs_setup == false`, поля `api_key` в `user` нет.
7. `POST /api/auth/register` вторым аккаунтом → `200`, `status == "pending"`, `message == "Your account was created and is waiting for an administrator to approve it."`, `user.status == "pending"`, `user.reviewed_at == null`, `Set-Cookie` отсутствует.
8. `POST /api/auth/login` этим pending-аккаунтом с верным паролем → `403`, `detail == "Your account is waiting for an administrator to approve it."`.
9. `POST /api/auth/login` несуществующим email → `401`, `detail == "Incorrect email or password."` (тот же текст, что и при неверном пароле).
10. `POST /api/auth/login` без поля `password` → `422`, `detail[0].type == "missing"`, `detail[0].loc == ["body","password"]`.
11. `POST /api/auth/register` с паролем из 7 символов → `400`, `detail == "Password must be at least 8 characters."`.
12. `POST /api/auth/register` с `email":"nope"` → `400`, `detail == "Enter a valid email address."`.
13. `POST /api/auth/register` с уже занятым email (в другом регистре) → `400`, `detail == "An account with that email already exists."`.
14. `GET /api/auth/me` без cookie → `401`, `detail == "Authentication required."`, заголовок `WWW-Authenticate: Bearer realm="Spark AI Hub"`.
15. `GET /api/auth/me` с cookie → `200`, есть `api_key`, начинающийся с `sah-`, длина 47, есть объект `usage` c 9 ключами (`requests,prompt_tokens,completion_tokens,total_tokens,last_used_at,requests_30d,tokens_30d,requests_24h,tokens_24h`).
16. `GET /api/recipes` без cookie → `401 {"detail":"Authentication required."}` (гейт шире auth-роутера).
17. `OPTIONS /api/auth/me` без cookie → `401` (preflight не обходит auth-гейт).
18. `PATCH /api/auth/me` c `{"name":"B"}` без `current_password` → `200`, `name == "B"`, ответ без `api_key` и без `usage`.
19. `PATCH /api/auth/me` c `{"email":"c@x.io"}` без `current_password` → `403`, `detail == "Your current password is incorrect."`.
20. `PATCH /api/auth/me` c `{"email":"c@x.io","current_password":"<верный>"}` → `200`, `email == "c@x.io"`.
21. `PATCH /api/auth/me` c `{"current_password":"<верный>","new_password":"1234567"}` → `400`, `detail == "Password must be at least 8 characters."`.
22. После смены пароля старая сессия (другой cookie-jar, полученный тем же аккаунтом) продолжает отвечать `200` на `/api/auth/me` (сессии не убиваются).
23. `POST /api/auth/me/api-key/rotate` → `200`, `api_key` отличается от прежнего и начинается с `sah-`; `GET /api/auth/me` отдаёт новый ключ.
24. После ротации запрос к `/v1/models` со старым ключом → `401`, тело `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"Missing or invalid API key. Sign in to Spark AI Hub and copy your key from Account, or run the sah installer again."}}`.
25. `GET /v1/models` без ключа → `401` с тем же телом; `GET /api/recipes` с валидным API-ключом вместо cookie → `401` (ключи не пускают в `/api`).
26. `GET /mcp` (или POST без тела) с API-ключом → не `401` по причине авторизации; `GET /mcp` без ключа → `401`, `WWW-Authenticate` содержит `resource_metadata="$(H)/.well-known/oauth-protected-resource/mcp"` и `scope="mcp"`; с неверным bearer — дополнительно `error="invalid_token"`.
27. `GET /api/admin/users` с cookie обычного (active, role=user) аккаунта → `403`, `detail == "Only an administrator can do that."`.
28. `GET /api/admin/users` админом → `200`, ключи `users`, `totals`, `models`; каждый `users[i]` содержит 8 полей пользователя + `usage` + `storage {bytes,files}`; `totals` содержит `users,pending,active,requests,total_tokens,storage_bytes`.
29. Сортировка `users`: все `status == "pending"` идут раньше остальных; внутри групп — по убыванию `usage.total_tokens`.
30. `POST /api/admin/users` админом c `{"email":"d@x.io","password":"secret123"}` → `201`, `role == "user"`, `status == "active"`, `reviewed_at != null`, `usage.requests == 0`, `storage == {"bytes":0,"files":0}`.
31. Тот же запрос на пустой БД (до любого register) даёт `role == "user"`, а не `"admin"`.
32. `POST /api/admin/users` c `{"role":"root"}` → `400`, `detail == "Role must be admin or user."`.
33. `PATCH /api/admin/users/{id}` c `{"status":"active"}` для pending → `200`, `status == "active"`, `reviewed_at` обновился; затем `POST /api/auth/login` этим аккаунтом → `200` + `Set-Cookie`.
34. `PATCH /api/admin/users/{id}` c `{"status":"rejected"}` для active → `200`, `status == "rejected"`, и cookie этого аккаунта сразу получает `401` на `/api/auth/me` (сессии удалены), а его API-ключ — `401` на `/v1/models`.
35. `PATCH /api/admin/users/999999` c `{"status":"active"}` → `404`, `detail == "No such account."`; тот же путь с `{"role":"root"}` → `400` (валидация раньше поиска).
36. `PATCH /api/admin/users/{admin_id}` c `{"role":"user"}`, если админ единственный → `400`, `detail == "This is the only administrator — promote someone else first."`.
37. `DELETE /api/admin/users/{own_id}` → `400`, `detail == "You cannot delete your own account."`.
38. `DELETE /api/admin/users/{other_id}` → `200`, `{"status":"deleted"}`; повторный `DELETE` → `404`; `SELECT count(*) FROM usage_events WHERE user_id=<id>` == 0 (каскад).
39. `GET /api/admin/users/999999/usage` → `404 {"detail":"No such account."}`; для существующего → `200 {"usage": {…9 ключей…}, "models":[…]}`.
40. Сделать вызов `/v1/chat/completions` с ключом пользователя → в `usage_events` появилась строка с `user_id`, `source == "api_key"`, `endpoint` вида `openai:chat/completions`, `total_tokens > 0`; `GET /api/auth/me/usage` показывает рост `requests` и `total_tokens`.
41. Тот же вызов с cookie вместо ключа → `source == "session"`.
42. `POST /api/auth/logout` → `200 {"status":"signed_out"}`, `Set-Cookie` с `Max-Age=0`; повторный `GET /api/auth/me` с тем же cookie → `401`; повторный `POST /api/auth/logout` без cookie → `200`.
43. `GET /.well-known/oauth-protected-resource` → `200`, `resource == "$(H)/mcp"`, `authorization_servers == ["$(H)"]`, `scopes_supported == ["mcp"]`, `bearer_methods_supported == ["header"]`, `resource_name == "Spark AI Hub"`.
44. `GET /.well-known/oauth-protected-resource/mcp` → тело побайтно равно ответу из п.43.
45. `GET /.well-known/oauth-authorization-server` → `200`, `issuer == "$(H)"`, эндпоинты `…/oauth/authorize|token|register`, `code_challenge_methods_supported == ["S256"]`, `token_endpoint_auth_methods_supported == ["none"]`, `client_id_metadata_document_supported == true`, `authorization_response_iss_parameter_supported == true`, `response_modes_supported == ["query"]`.
46. `GET /.well-known/nonsense` → `404 {"error":"not_found"}` и `Content-Type: application/json` (не HTML SPA).
47. `POST /oauth/register` c `{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"client_name":"Claude","token_endpoint_auth_method":"none"}` → `201`, `client_id` начинается с `sah-client-`, `grant_types == ["authorization_code","refresh_token"]`, `response_types == ["code"]`, `client_id_issued_at` — целое; заголовки `Cache-Control: no-store`, `Pragma: no-cache`.
48. `POST /oauth/register` c `{"redirect_uris":["http://evil.example/cb"]}` → `400`, `error == "invalid_redirect_uri"`, `error_description == "http://evil.example/cb must be https, or http on a loopback address."`.
49. `POST /oauth/register` c `{"redirect_uris":[]}` → `400`, `error == "invalid_redirect_uri"`; c `{"redirect_uris":["https://a/b"],"token_endpoint_auth_method":"client_secret_basic"}` → `400`, `error == "invalid_client_metadata"`; тело `[]` → `400`, `error_description == "Body must be a JSON object."`.
50. `GET /oauth/authorize?client_id=bogus` → `400` HTML, содержащий `This app is not registered with the Hub`.
51. `GET /oauth/authorize` c валидным `client_id`, но `redirect_uri`, не входящим в зарегистрированные → `400` HTML `The app asked to return to an address it did not register.`, **без** `Location`.
52. `GET /oauth/authorize?...&response_type=token` → `302` на `redirect_uri` c `error=unsupported_response_type`, `iss=$(H)`, сохранённым `state`.
53. `GET /oauth/authorize?...&response_type=code` без `code_challenge` → `302` c `error=invalid_request`, `error_description == "PKCE with code_challenge_method=S256 is required."`; c `code_challenge_method=plain` — то же.
54. `GET /oauth/authorize?...&resource=https://other.example/mcp` → `302` c `error=invalid_target`; без `resource` — `200` HTML (страница входа, если нет cookie).
55. `GET /oauth/authorize` с валидными параметрами и **без** cookie → `200` HTML-форма входа с `action="/oauth/authorize"` и `X-Frame-Options: DENY`; с cookie активного пользователя → `200` HTML-страница согласия с кнопками `allow`/`deny`.
56. `POST /oauth/authorize` c несуществующим `request_id` → `400` HTML `This sign-in request expired. Start connecting again from the app.`.
57. `POST /oauth/authorize` (`action=login`) с неверным паролем → `401` HTML `Incorrect email or password.`; с pending-аккаунтом → `401` HTML `This account is not active yet.`; с верным активным — `200` страница согласия + `Set-Cookie`.
58. `POST /oauth/authorize` (`action=deny`) без cookie сессии → `403` HTML `Sign in on this page to approve the connection.`.
59. Полный happy-path: `action=login` → `action=allow` (тем же jar) → `303` на `redirect_uri` c `code`, `state`, `iss=$(H)`; затем `POST /oauth/token` (`grant_type=authorization_code`, `code`, `client_id`, `redirect_uri`, `code_verifier`) → `200`, `token_type == "Bearer"`, `expires_in == 3600`, `scope == "mcp"`, `access_token` начинается с `sah-oat-`, `refresh_token` — с `sah-ort-`.
60. Повторный обмен того же `code` → `400`, `error == "invalid_grant"`, `error_description == "The authorization code is invalid or was already used."`.
61. Обмен с неверным `code_verifier` → `400`, `error == "invalid_grant"`, `error_description == "PKCE verification failed."`.
62. `POST /oauth/token` с `Content-Type: application/json` → `400`, `error == "invalid_request"`, `error_description == "Use application/x-www-form-urlencoded."`.
63. `POST /oauth/token` с неизвестным `client_id` → `401`, `error == "invalid_client"`, `error_description == "Unknown client_id."`.
64. `POST /oauth/token` с `grant_type=password` → `400`, `error == "unsupported_grant_type"`.
65. `POST /oauth/token` (`grant_type=refresh_token`) тем же refresh-токеном → `200` с новой парой токенов; повторное использование старого refresh → `400`, `error_description == "The refresh token is invalid, expired or already used."` (ротация).
66. `GET /mcp` c `Authorization: Bearer <access_token>` → запрос авторизован (не `401` от гейта); тот же токен на `GET /v1/models` → `401` (OAuth ограничен `/mcp`); тот же токен на `GET /api/auth/me` → `401`.
67. После `PATCH /api/admin/users/{id} {"status":"rejected"}` для владельца OAuth-токена запрос `/mcp` с его access-token → `401`; `refresh` → `400 invalid_grant` `"The account is no longer active."`.
68. WebSocket `GET /ws/metrics` без cookie → handshake закрывается кодом `1008`, соединение не устанавливается.
69. `GET /` без cookie → `200` HTML `index.html` с `Cache-Control: no-cache`; `GET /assets/missing-abc123.js` без cookie → `404` (ответ статики, не `index.html`).
70. `GET /api/auth/state` при `SPARK_AI_HUB_AUTH_ENABLED=false` → `200`, `auth_enabled == false`, `user == null`; при этом `GET /api/auth/me` → `401`.
