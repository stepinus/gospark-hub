# Фаза 9 — движки без контейнера и нативные рецепты

## Зачем

Контейнер на каждую модель тащит рантайм (CUDA, Python, TensorFold) в каждый образ: 74 ГБ на
модель, из них 44 ГБ — не веса. Движок нужен один на машину, а рецепт модели — это только
конфиг запуска: путь к чекпоинту и флаги.

## Что появляется

1. **Страница Engines** (`/engines`): список движков, пока один — TensorFold. У каждого имя и три
   кнопки: «Установить» (когда не установлен), «Обновить», «Удалить».
2. **Тип рецепта `native`**: `recipe.yaml` без `docker-compose.yml`, с блоком `native`.
3. **Первый нативный рецепт** — Qwen3.8-Flash-Next на локальном чекпоинте, с теми же
   параметрами, с которыми он сейчас работает в контейнере.

## Хранение

```
~/.config/gospark/engines/          # ENGINE_HOME, переопределяется на время тестов
  tensorfold/
    1.1.1/                          # распакованный релиз: bin/tensorfold-native, lib/, VERSION
    current -> 1.1.1                # симлинк, установленная версия
```

Установка: скачать `tensorfold-<v>-linux-aarch64.tar.gz` + `.sha256` из релиза GitHub,
проверить сумму, распаковать во временный каталог, перенести в `tensorfold/<v>/`, переставить
симлинк, удалить прошлые версии. Удаление: снести весь `tensorfold/`.

## Формат нативного рецепта

```yaml
slug: qwen38-flash-next-native
kind: native
native:
  engine: tensorfold
  model: "hf:nvidia/Qwen3.8-Flash-Next-FP4-Local@b9ce18a6887e18c089fdea8509a33e63429a3300"
  args: ["--context", "262144", "--parallel", "4", "--vision"]
  env: {TF_FLASHNEXT_DRAFTS: "6"}
  port: 8888
```

- `kind` по умолчанию `container`; в JSON рецепта поле отдаётся только у нативных
  (`omitempty`) — иначе ломается паритет с апстримом.
- `model`: `hf:<repo>@<rev>` разворачивается в snapshot HF-кэша (`HF_HUB_CACHE`, иначе
  `~/.cache/huggingface/hub`, каталог `models--<repo с / → -->`, `snapshots/<rev>`); путь с `~`
  раскрывается; обычный путь берётся как есть.
- `port` — порт приложения на хосте; по умолчанию `ui.port`.

## Запуск

`systemd-run --user --unit=gospark-native-<slug> --collect`, вывод в
`<data>/logs/<slug>.log`:

```
systemd-run --user --unit=gospark-native-<slug> --collect \
  --property=StandardOutput=append:<data>/logs/<slug>.log \
  --property=StandardError=append:<data>/logs/<slug>.log \
  --setenv=TF_FLASHNEXT_DRAFTS=6 ... \
  <engine>/current/bin/tensorfold-native serve <model> --context ... --port 8888 --name "<served>"
```

- Статус: `systemctl --user is-active gospark-native-<slug>` → running.
- Стоп: `systemctl --user stop`.
- Движок не установлен → `409 {"detail":"Engine tensorfold is not installed"}`.
- Процесс переживает перезапуск демона; после перезагрузки машины не поднимается сам (как и
  контейнеры, которые Hub тоже не поднимает автоматически).

## Маршрутизация

Caddy живёт в контейнере и уже имеет `host.docker.internal:host-gateway`. Для нативного рецепта
апстрим в Caddyfile — `host.docker.internal:<native.port>`; остальное (probe, strip_prefix,
редирект) как у контейнерных.

## API

| Метод | Путь | Ответ |
|---|---|---|
| GET | `/api/engines` | `{"engines":[{"name","installed","version","latest","path"}]}` |
| POST | `/api/engines/{name}/install` | `{"name","version","installed":true}` (409 если уже стоит и версия та же) |
| POST | `/api/engines/{name}/update` | ставит последнюю; 409 если нативный рецепт этого движка запущен |
| DELETE | `/api/engines/{name}` | `{"deleted":true}`; 409 если нативный рецепт запущен |

Только для админа — как `/api/registries`.

Запуск и остановка нативного рецепта — существующие `POST /api/recipes/{slug}/launch` и
`/stop`. Установка (`/install`) для нативного рецепта — no-op `{"status":"installed"}`, чтобы
кнопки в UI не менялись.

`GET /api/recipes/{slug}` и список отдают у нативного рецепта `"kind":"native"` и блок `native`.

## Приёмка

1. `GET /api/engines` на чистом `ENGINE_HOME` → `installed:false`, `latest:1.1.1`.
2. `POST /api/engines/tensorfold/install` → каталог, симлинк, `tensorfold-native --version` = 1.1.1.
3. Повторный install той же версии → 409; `update` при той же версии → `{"version":"1.1.1","changed":false}`.
4. `DELETE /api/engines/tensorfold` → каталога нет, `installed:false`.
5. Нативный рецепт: launch без движка → 409; с движком → юнит активен, `/v1/models` через
   `127.0.0.1:<port>` отвечает; stop → юнита нет.
6. `/run/<slug>/v1/models` через Caddy отвечает — маршрут собран для нативного рецепта.
7. Страница `/engines` рисует карточку, кнопки меняются по состоянию.
8. Контейнерные рецепты не изменились: `go test ./...`, parity, `/api/recipes` без поля `kind`.
