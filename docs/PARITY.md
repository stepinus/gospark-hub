# Паритетный прогон

Приёмка порта: наш демон и **второй экземпляр апстрима** на одинаковых данных, ответы
сравниваются по путям, ключам, типам и значениям. Живой хаб на :9000 не трогаем —
у него своя учётка и свои данные.

## Поднять эталонный апстрим

```
cd ~/spark-ai-hub
rm -rf /tmp/up-data && mkdir -p /tmp/up-data
SPARK_AI_HUB_DATA_DIR=/tmp/up-data SPARK_AI_HUB_DB_PATH=/tmp/up-data/hub.db \
SPARK_AI_HUB_PORT=9020 SPARK_AI_HUB_PROXY_ENABLED=false \
.venv/bin/python -m uvicorn daemon.main:app --host 127.0.0.1 --port 9020
```

## Поднять наш демон

```
cd ~/Workspace/gospark-hub
go build -o /tmp/sahd ./cmd/sahd
rm -rf /tmp/sahd-data && mkdir -p /tmp/sahd-data
PORT=9120 PUBLIC_PORT=9100 DATA_DIR=/tmp/sahd-data DB_PATH=/tmp/sahd-data/hub.db /tmp/sahd
```

## Прогнать сравнение

```
go run ./cmd/parity -upstream http://127.0.0.1:9020 -ours http://127.0.0.1:9120
```

Харнесс сам регистрирует первый аккаунт (он становится админом) на обеих сторонах и
логинится. Полезные флаги: `-only /api/recipes`, `-max-diff 20`.

## Что означает DIFF

- `Content-Type text/html ≠ application/json` — маршрут у нас ещё не реализован, запрос
  провалился в SPA-fallback.
- `только у апстрима` / `только у нас` — расхождение схемы, чинится в модели.
- Значения сравниваются без волатильных полей: метки времени, id, ключи, счётчики;
  порты, `sah-…`-ключи и путь до registry маскируются — они различаются по устройству.
- `(структура)` — кейс сверяется только по набору ключей и типам (метрики, доступ к HF).

## Состояние на 2026-10-18

Совпадают: `state`, `me`, `me/usage`, `users`, `recipes` (148 рецептов, поля и значения),
`recipes/{slug}` для web и api-only, 404 на неизвестный slug, `connect`, `metrics`,
`hf-token`, `hf-access`, `media`, `/sah/version`, SPA.

Осталось (фаза 3, docker): `launch-plan`, `compose`, `build-status`, `/api/containers`
и `has_leftovers` в списке рецептов.
