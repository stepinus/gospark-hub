# Задача: фаза 4 — один порт (Caddy)

Читай сначала: `docs/PLAN.md`, `docs/inventory/containers.md` (раздел про Caddy),
`~/spark-ai-hub/daemon/services/proxy_service.py` (304 строки), готовый
`~/spark-ai-hub/data/Caddyfile` (2 975 строк — эталон вывода) и
`~/spark-ai-hub/frontend/src/lib/urls.js` (как фронт строит ссылки на `/run/{slug}/`).

## Что сделать

Пакет `internal/proxy` + подключение в `cmd/sahd/main.go` (правит только воркер этой фазы,
остальным файл трогать нельзя).

1. Генерация `data/Caddyfile` по тем же правилам, что апстрим: сеть, контейнер
   `caddy:2-alpine`, публичный порт, per-recipe блок `/run/{slug}/` с `handle_path`
   (срез префикса), `reverse_proxy <container_name>:<ui.port>`, `forward_auth` в
   `host.docker.internal:<daemon_port>/api/auth/me` с редиректом на `/` при 401/403,
   проб-токен в заголовке для health-check в обход авторизации, редирект с
   `/run/{slug}` на `/run/{slug}/` (308), `trusted_proxies` для `X-Forwarded-*`.
2. Эталон: наш сгенерированный файл должен совпадать с `~/spark-ai-hub/data/Caddyfile`
   построчно после нормализации токена и портов. Положить upstream-копию в
   `internal/proxy/testdata/upstream.Caddyfile` и сравнивать в тесте.
3. Поднять/обновить контейнер Caddy (`ensure_proxy`): те же аргументы `docker run`,
   те же маунты (`data/` в `/etc/caddy`), перезапуск при изменении конфига.
4. Поведение при `proxy_enabled=false`: демон сам слушает публичный порт (в апстриме
   это сломано — 9000 не слушает никто, см. `docs/inventory/containers.md` R3).
   Здесь паритет не нужен: демон обязан слушать публичный порт, иначе приложения
   недоступны. Зафиксировать это в комментарии и тесте.
5. Имена контейнеров и сеть — с префиксом `cfg.Project`.
6. Тесты: golden-файл Caddyfile, разбор сгенерированных маршрутов (slug → container:port),
   поведение при пустом реестре и при рецепте без `ui.proxy`.

## Границы

- Не трогать `internal/api/*`, `internal/auth/*`, `internal/registry/*`, `internal/media/*`,
  `internal/mcp/*`, `internal/modelproxy/*`, `frontend/*`. `cmd/sahd/main.go` — можно,
  только добавляя вызов `proxy.Ensure(...)`.
- Экспортировать `func Ensure(ctx, cfg, cache *registry.Cache, log) error` и
  `func Render(cfg, recipes) string`.

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- `go test ./internal/proxy -run Caddyfile` сравнивает с эталоном и проходит.
- Коммит в текущую ветку: `phase 4: один порт`.
