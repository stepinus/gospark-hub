# Задача: фаза 5 — система и метрики

Читай сначала: `docs/PLAN.md`, `docs/inventory/containers.md` (разделы про метрики,
connect, hf-token), `docs/contract/openapi.json`. Источник истины —
`~/spark-ai-hub/daemon/routers/system.py`, `daemon/services/monitor_service.py`,
`daemon/services/connect_service.py` (только чтение).

## Что сделать

Пакет `internal/monitor` + роуты `internal/api/system`.

1. `GET /api/system/metrics` — тот же JSON: gpu (использование, память, температура,
   имя модели?), ram (всего/занято/свободно), disk (всего/занято), uptime/load — ровно
   те поля и единицы, что в апстриме. Источник GPU — `nvidia-smi --query-gpu=...`
   (как в апстриме), память/диск — `/proc` и `statfs`, без сторонних библиотек.
2. `WS /ws/metrics` — та же каденция и тот же кадр (проверить в
   `daemon/routers/system.py`: интервал, поля, первое сообщение сразу после connect).
   Использовать `github.com/coder/websocket` (уже в `go.mod`).
3. `GET /api/system/connect` — кандидаты адресов: `X-Forwarded-Host`/`Host` запроса
   первым, затем mDNS-имя хоста, затем tailscale-адрес, если `tailscale` есть в PATH;
   структура ответа как в апстриме (`docs/inventory/containers.md`).
4. `GET/POST /api/system/hf-token` и `GET /api/system/hf-access/{slug}` — где лежит
   токен, маскирование в ответе, коды ошибок.
5. Тесты на парсеры: nvidia-smi вывод → структура, `/proc/meminfo` → байты,
   парсер tailscale — на фикстурах, без обращения к железу.

## Границы

- Не трогать `internal/web/*`, `cmd/sahd/main.go`, `internal/auth/*`, `frontend/*`.
- Экспортировать `func Register(r *web.Router, deps Deps)`; `Deps` — конфиг, логгер,
  реестр (для hf-access нужен рецепт).

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- `GET /api/system/metrics` на этой машине отдаёт правдоподобные значения (проверить
  вручную: `curl` после запуска `sahd`, сравнить с `nvidia-smi` и `free -m`).
- Коммит в текущую ветку: `phase 5: метрики и система`.
