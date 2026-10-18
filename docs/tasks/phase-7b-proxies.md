# Задача: фаза 7b — прокси моделей `/v1`

Читай сначала: `docs/PLAN.md`, `docs/inventory/registry-media.md` (раздел про `/v1`),
`~/spark-ai-hub/daemon/routers/{openai_proxy,anthropic_proxy}.py`,
`daemon/services/proxy_service.py`, `tests/test_{openai,anthropic}_proxy.py` (только чтение).

## Что сделать

Пакет `internal/modelproxy` + роуты `internal/api/models`.

1. `ANY /v1/{path}` — OpenAI-совместимый прокси: как апстрим выбирает целевой контейнер
   (по какому рецепту/модели), какие заголовки пробрасывает и какие подменяет,
   как обрабатывает `model` в теле, стриминг (`text/event-stream`) без буферизации,
   таймауты, ошибки и их тела.
2. `POST /v1/messages` и `POST /v1/messages/count_tokens` — Anthropic-совместимый
   прокси с теми же преобразованиями.
3. Учёт `usage_events`: ключ из `Authorization`/`X-Api-Key`, модель, токены из ответа
   (включая стриминговые `usage`-чанки), `route`. Отказ при отсутствии ключа — как в
   апстриме.
4. Тесты: httptest-«модель» на 127.0.0.1, проверяющая заголовки/тело/стрим; отдельно —
   ответ без usage, ошибка апстрима, неизвестная модель.

## Границы

- Не трогать `cmd/sahd/main.go`, `internal/web/*`, `internal/auth/*`, `internal/registry/*`,
  `internal/dockerx/*`, `internal/media/*`, `internal/api/*`, `frontend/*`.
- Экспортировать `func Register(r *web.Router, deps Deps)`; `Deps` — конфиг, реестр,
  БД, логгер, dockerx (чтобы узнать, какой контейнер запущен).

## Готово когда

- `go build ./... && go vet ./... && go test ./...` зелёные.
- Стриминг проверен тестом: чанки доходят до клиента по мере поступления.
- Коммит в текущую ветку: `phase 7b: прокси моделей`.
