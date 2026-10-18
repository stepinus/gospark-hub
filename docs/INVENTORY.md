# Инвентарь функционала апстрима

Снято с живого хаба (`/openapi.json`: 50 путей, 22 схем)
и с исходников `~/spark-ai-hub` @ 6eb4372.

## Масштаб

| Часть | Объём |
|---|---|
| daemon (Python) | 9 954 строки, 12 роутеров, 64 HTTP-эндпоинтов + 3 WebSocket |
| сервисы | docker 1204, mcp 1153, image 608, anthropic 506, auth 505, video 497, openai 454, oauth 355, connect 360, proxy 304, media_store 244, mesh 233, web 243, link 114, registry 117, upload 118, audio 114, decision 129, monitor 90 |
| frontend | 8 690 строк React 19 + Vite + Tailwind 4, 29 jsx, 12 маршрутов, 169 обложек, 143 логотипа |
| registry | 148 рецептов (143 с GPU; 24 web, 124 api-only), 296 yaml |
| sah CLI | 3 425 строк Python (этап 2), отдаётся как есть |
| тесты | 5 файлов, test_mcp 79 КБ — спецификация MCP |

## Экраны фронтенда

`/` каталог · `/app/:slug` карточка · `/running` запущенное · `/files` файлы и медиа ·
`/agents` подключение клиентских агентов · `/system` метрики · `/users` пользователи ·
`/account` аккаунт · `/upload` загрузка · `/about`.

## HTTP-контракт

| путь | метод | назначение | доступ |
|---|---|---|---|
| `/.well-known/oauth-authorization-server` | GET | Authorization Server Metadata | нет |
| `/.well-known/oauth-protected-resource` | GET | Protected Resource Metadata | нет |
| `/.well-known/oauth-protected-resource/mcp` | GET | Protected Resource Metadata | нет |
| `/.well-known/{rest}` | GET | Well Known Missing | нет |
| `/api/admin/users` | GET | List Users | ключ/сессия |
| `/api/admin/users` | POST | Create User | ключ/сессия |
| `/api/admin/users/{user_id}` | DELETE | Delete User | ключ/сессия |
| `/api/admin/users/{user_id}` | PATCH | Update User | ключ/сессия |
| `/api/admin/users/{user_id}/usage` | GET | User Usage | ключ/сессия |
| `/api/auth/login` | POST | Login | ключ/сессия |
| `/api/auth/logout` | POST | Logout | ключ/сессия |
| `/api/auth/me` | GET | Me | ключ/сессия |
| `/api/auth/me` | PATCH | Update Me | ключ/сессия |
| `/api/auth/me/api-key/rotate` | POST | Rotate Api Key | ключ/сессия |
| `/api/auth/me/usage` | GET | My Usage | ключ/сессия |
| `/api/auth/register` | POST | Register | ключ/сессия |
| `/api/auth/state` | GET | Auth State | ключ/сессия |
| `/api/containers` | GET | List Containers | ключ/сессия |
| `/api/media` | GET | List Media | ключ/сессия |
| `/api/media/{name}` | DELETE | Delete Media | ключ/сессия |
| `/api/media/{name}/thumb` | GET | Media Thumb | ключ/сессия |
| `/api/media/{name}/thumb` | PUT | Set Render | ключ/сессия |
| `/api/recipes` | GET | List Recipes | ключ/сессия |
| `/api/recipes/{slug}` | DELETE | Remove | ключ/сессия |
| `/api/recipes/{slug}` | GET | Get Recipe Detail | ключ/сессия |
| `/api/recipes/{slug}/build-status` | GET | Build Status | ключ/сессия |
| `/api/recipes/{slug}/compose` | GET | Get Compose | ключ/сессия |
| `/api/recipes/{slug}/compose` | PUT | Put Compose | ключ/сессия |
| `/api/recipes/{slug}/compose/reset` | POST | Reset Compose | ключ/сессия |
| `/api/recipes/{slug}/install` | POST | Install | ключ/сессия |
| `/api/recipes/{slug}/launch` | POST | Launch | ключ/сессия |
| `/api/recipes/{slug}/launch-plan` | GET | Launch Plan | ключ/сессия |
| `/api/recipes/{slug}/purge` | POST | Purge | ключ/сессия |
| `/api/recipes/{slug}/stop` | POST | Stop | ключ/сессия |
| `/api/recipes/{slug}/update` | POST | Update | ключ/сессия |
| `/api/system/connect` | GET | Connect Info | ключ/сессия |
| `/api/system/hf-access/{slug}` | GET | Get Hf Access | ключ/сессия |
| `/api/system/hf-token` | GET | Get Hf Token | ключ/сессия |
| `/api/system/hf-token` | POST | Set Hf Token | ключ/сессия |
| `/api/system/metrics` | GET | Metrics | ключ/сессия |
| `/api/uploads` | POST | Upload | ключ/сессия |
| `/audio/{name}` | GET | Audio File | нет |
| `/images/{name}` | GET | Image File | нет |
| `/link/download/{token}` | GET | Download | ключ/сессия |
| `/link/upload/{token}` | POST | Upload Once | ключ/сессия |
| `/mcp` | DELETE | Mcp No Stream | ключ/сессия |
| `/mcp` | GET | Mcp No Stream | ключ/сессия |
| `/mcp` | POST | Mcp Post | ключ/сессия |
| `/meshes/{name}` | GET | Mesh File | нет |
| `/oauth/authorize` | GET | Authorize | нет |
| `/oauth/authorize` | POST | Authorize Decision | нет |
| `/oauth/register` | POST | Register | нет |
| `/oauth/token` | POST | Token | нет |
| `/sah/install.sh` | GET | Sah Install Script | нет |
| `/sah/version` | GET | Sah Version | нет |
| `/v1/messages` | POST | Messages | нет |
| `/v1/messages/count_tokens` | POST | Count Tokens | нет |
| `/v1/{path}` | DELETE | Proxy | нет |
| `/v1/{path}` | GET | Proxy | нет |
| `/v1/{path}` | PATCH | Proxy | нет |
| `/v1/{path}` | POST | Proxy | нет |
| `/v1/{path}` | PUT | Proxy | нет |
| `/videos/{name}` | GET | Video File | нет |
| `/{full_path}` | GET | Serve Spa | нет |

WebSocket: `GET /ws/build/{slug}`, `GET /ws/logs/{slug}`, `GET /ws/metrics`.

## Поведенческие контракты (по модулям)

- `docs/inventory/auth.md` — сессии, первый-становится-админом, одобрение, API-ключи, usage, OAuth
- `docs/inventory/containers.md` — install/launch/stop/remove/purge, compose, Caddy, метрики
- `docs/inventory/registry-media.md` — схема рецепта, каталог, медиа, MCP, /v1-прокси
