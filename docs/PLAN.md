# gospark-hub — план переноса Spark AI Hub на Go

Цель этапа 1: **полный** перенос функционала `~/spark-ai-hub` (FastAPI, 9 954 строки,
52 эндпоинта, WS) на Go с сохранением интерфейса. Ни одна фича не теряется, контракт
API 1:1, фронтенд копируется файлами без правок.

Апстрим: `~/spark-ai-hub` @ `6eb4372`, MIT. Наш репозиторий: `~/Workspace/gospark-hub`,
бинарник `sahd`.

## Жёсткие требования

1. **Контракт API строго 1:1.** Пути, тела, коды, ошибки, cookie-сессия, `/v1`, `/mcp`,
   OAuth — байт в байт. Эталон: `docs/contract/openapi.json` (снят с живого хаба) плюс
   `docs/inventory/*.md` (поведение, которого в openapi нет: WS, побочные эффекты).
2. **Фронтенд — копия файлов** из `frontend/src`, `frontend/public` без изменений. Любая
   правка JS — сигнал, что контракт разошёлся, и чинится бэкенд.
3. **SQLite — та же схема байт в байт** (9 таблиц из `docs/contract/schema.sql`), чтобы
   открывался файл живого хаба. Никаких «улучшений» схемы на этапе 1.
4. **Рецепты — данные.** `registry/recipes` (148 рецептов) и `registry/covers` копируются
   как есть; образы `ghcr.io/waxacabytes/*` не пересобираются. Формат рецепта не меняем,
   чтобы тянуть обновления апстрима.
5. **`sah` CLI не переписываем.** Каталог `sah/` отдаётся статикой (`/sah`, `/sah/sah`),
   `/sah/install.sh` — с подстановкой кандидатов, `/sah/version` — sha256 файла. Перепись
   на Go — этап 2.
6. **Media/MCP в первом этапе**, но тесты на моках: реальные установки на 20–40 ГБ не гоняем.
7. **Изоляция от живого хаба.** Порты: публичный **9100**, демон **9110**. Префикс docker
   (`SAH_PROJECT`, сеть, имена контейнеров) параметризован, по умолчанию `gospark-hub`,
   чтобы тестовый хаб не трогал контейнеры продакшн-хаба на :9000. Каталог данных — свой.
8. Продуктовые принципы апстрима (`docs/UPSTREAM-MANIFEST.md`) сохраняются: offline-first,
   веса внутри образа, полное удаление, один порт, никаких своих портов у приложений.

## Стек

- `net/http` + `ServeMux` с паттернами (Go 1.27), без веб-фреймворка.
- `modernc.org/sqlite` (чистый Go, arm64, без cgo).
- `gopkg.in/yaml.v3` для рецептов и compose.
- `github.com/coder/websocket` для `/ws/*`.
- Docker — официальный Go SDK (`github.com/moby/moby/client` + `api/types`).
- Caddy остаётся Caddy: контейнер `caddy:2-alpine`, конфиг генерируется нами в
  `data/Caddyfile` тем же алгоритмом, что `daemon/services/proxy_service.py`.

## Дерево

```
cmd/sahd/            main: конфиг, сборка роутера, запуск
cmd/parity/          паритетный прогон против живого хаба
internal/config/     env-настройки (аналог daemon/config.py)
internal/store/      sqlite: схема, запросы, сессии, usage, media
internal/auth/       cookie-сессии, API-ключи, bcrypt, middleware
internal/registry/   загрузка и кэш рецептов, схема YAML
internal/dockerx/    docker-клиент, install/launch/stop/remove/purge, build-логи
internal/proxy/      Caddyfile, ensure-сеть, ensure-контейнер
internal/monitor/    метрики GPU/RAM/диск/темп, /ws/metrics
internal/media/      uploads, media store, thumbnails, короткие ссылки
internal/mcp/        /mcp JSON-RPC, инструменты, артефакты
internal/openai/     /v1/{path}, /v1/messages, count_tokens
internal/web/        роутер, статика dist, SPA-fallback, /sah, /cover-sources
frontend/            копия апстрима (src, public, сборка в dist)
registry/            recipes, covers
sah/                 оригинальный CLI
docs/                PLAN, INVENTORY, contract/, inventory/
```

## Фазы

Каждая фаза — отдельная ветка `phase-N-<имя>`, вливается только после прогона своих
проверок. Ворота фазы: `go build ./... && go vet ./... && go test ./...`.

### Фаза 0 — каркас (1 воркер)
Конфиг с env-префиксом, SQLite-схема и миграция-создание, роутер, статика фронта с
SPA-fallback и 404 для ассетов, `/healthz`, `/sah`, `/cover-sources`, `/assets`.
Проверки: `sahd` поднимается, отдаёт index.html, ассет, `/sah/sah`.
Готово, когда фронт из браузера показывает экран входа (пусть с ошибками сети).

### Фаза 1 — auth/admin (1 воркер)
Первый пользователь — админ; регистрация + ожидание одобрения; вход, выход, `/api/auth/me`,
смена почты и пароля, ротация ключа `sah-…`, `/api/auth/state`, usage; админский CRUD
пользователей и `/api/admin/users/{id}/usage`; middleware сессии и API-ключа на `/api`,
`/ws`, `/v1`, `/mcp`; cookie с `Secure` по `X-Forwarded-Proto`; уборка протухших сессий.
Проверки: паритетный набор auth-ассертов из `docs/inventory/auth.md`.

### Фаза 2 — реестр рецептов (параллельно: A)
Загрузка 148 рецептов, кэш, `/api/recipes`, `/api/recipes/{slug}`, `/covers`,
`/cover-sources`, `/logos`, оверлеи установленного состояния из БД.
Проверки: JSON списка и детали побайтно совпадает с апстримом на том же registry.

### Фаза 3 — docker-жизнь (параллельно: B)
install/update (pull и build), build-log WS, build-status, launch-plan, launch со
сравнением VRAM и конфликтами, stop, remove, purge, compose get/put/reset,
`/api/containers`, `/ws/logs/{slug}`.
Проверки: интеграционный тест на лёгком рецепте (searxng/anythingllm) + моки докера.

### Фаза 4 — один порт (параллельно: C, после B)
Сеть, генерация `Caddyfile` (2975 строк на 148 рецептов), контейнер Caddy,
`forward_auth` в `/api/auth/me`, проб-токен, `strip_prefix`, переезд демона на 9110,
поведение при `proxy_enabled=false` и при мёртвом Caddy.
Проверки: приложение доступно на `/run/{slug}/` без своего порта; без сессии — 302.

### Фаза 5 — система и метрики (параллельно: D)
`/api/system/metrics`, `/ws/metrics`, `connect` (mDNS, tailscale, кандидаты),
`hf-token` get/set, `hf-access/{slug}`.

### Фаза 6 — файлы и медиа (параллельно: E)
`/api/uploads`, `/api/media` (список, удаление, превью), `media`, `media_links`,
`/link/upload/{token}`, `/link/download/{token}`, срок жизни и одноразовость.

### Фаза 7 — MCP и прокси моделей (параллельно: F)
`/mcp` JSON-RPC: полный список инструментов с их схемами, вызовы в контейнеры,
артефакты `/images|/videos|/audio|/meshes/{name}`; `/v1/{path}`, `/v1/messages`,
`/v1/messages/count_tokens`; учёт usage по ключу; стриминг.
Проверки: `tests/test_mcp.py` (79 КБ) как спецификация + моки.

### Фаза 8 — OAuth для `/mcp` (параллельно: G)
`/.well-known/*`, `/oauth/register`, `/oauth/token`, `/oauth/authorize` (GET+POST),
protected-resource метаданные со ссылкой на `/mcp`.

### Фаза 9 — паритет и приёмка
`cmd/parity`: поднять апстрим и нас на одинаковых данных, прогнать все эндпоинты,
диффовать JSON; чек-лист из трёх `docs/inventory/*.md`; ручной прогон UI по всем
экранам (каталог, карточка, запуск, логи, файлы, агенты, система, пользователи).

## Ветки и воркеры

- Один репозиторий, ветка на фазу, вливание — только мной после проверок.
- Максимум 3 параллельных pi-воркера, каждому свой worktree в `../gospark-hub-wt/<фаза>`.
- Параллелятся только фазы, не пересекающиеся по файлам: 2 (реестр), 3 (docker),
  5 (система), 6 (медиа). Фазы 0, 1, 4, 7, 8 — последовательные или после стабилизации.
- Конфликтные файлы (`internal/web/router.go`, `internal/config`, `cmd/sahd/main.go`)
  правит только воркер фазы 0-1; остальные регистрируют свои маршруты через
  функцию `Register(rt *Router)` в своём пакете.

## Вне скоупа этапа 1

Перепись `sah` на Go, интеграция с llama-swap, десктоп на Wails/Fyne, RTX Spark и
Strix Halo. Перечисленное — этап 2.
