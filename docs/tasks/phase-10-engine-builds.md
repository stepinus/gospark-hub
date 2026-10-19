# Фаза 10 — патченый клиент движка под модель

## Что выяснилось

Стоковый релиз TensorFold 1.1.1 этот чекпоинт почти не умеет: он загрузил веса за 143 с, но
отказался от четырёх потоков (31,14 ГиБ KV против бюджета 18,82) и от vision
(`vision_tower.*` против `model.visual.*` в чекпоинте NVIDIA).

В репозитории модели (`MiaAI-Lab/Qwen3.8-Flash-Next-Single-DGX-Spark-TensorFold`) есть
`patches/` — пятнадцать патчей к TensorFold (ветка `zig-flashnext`), из которых собирается
`tensorfold-native` **v0.6.5** с моделью внутри: `--kv-dtype fp8`, ModelOpt NVFP4/mixed, vision,
PLE на SSD, MTP. Проверено вручную: такой бинарь грузит веса за 52 с, держит 4 потока на полное
окно 262 144 (1 056 байт на позицию на слой, 14,51 ГиБ) и отвечает за 1,7 с.

Значит движок — не «один релиз на машину», а **клиент, собранный под модель**. Задача: система
создаёт такую папку и запускает рецепт на ней.

## Модель данных

Рецепт объявляет сборку:

```yaml
kind: native
native:
  engine: tensorfold
  engine_build:
    # Образ, в котором уже собран патченый движок (его собирает prepare.sh репозитория
    # модели). Если образа нет, а есть command — хаб сначала выполняет command.
    image: "tensorfold-qwen38:zig-db28187"
    artifacts: ["native/bin", "share", "python", "fatbin"]
    command: ""            # необязательно; путь к скрипту сборки относительно каталога рецепта
    env:
      TENSORFOLD_CUDA_KERNELS: "share/tensorfold/cuda/sm121"
      TENSORFOLD_VISION_PYTHONPATH: "python"
      PYTHONPATH: "python"
  model: "hf:..."
  args: [...]
```

Папка клиента: `<ENGINE_HOME>/tensorfold/builds/<slug>/`, внутри — скопированные артефакты
(`bin/tensorfold-native`, `share/`, `python/`, `fatbin/`) и `build.json`:

```json
{"recipe":"qwen38-flash-next-native","engine":"tensorfold","image":"tensorfold-qwen38:zig-db28187","built_at":"..."}
```

Пути в `env` относительные — раннер подставляет абсолютный путь папки клиента.

## Поведение

- `POST /api/recipes/{slug}/install` для нативного рецепта с `engine_build`: если папки нет —
  `docker image inspect` образа; при отсутствии образа и наличии `command` выполнить его
  (`bash -lc`, `cwd` = каталог рецепта), затем `docker create` + `docker cp` каждого артефакта,
  `docker rm`, записать `build.json`. Ответ `{"slug":"…","status":"installed","engine_build":"…"}`.
  Без образа и без команды — 409 `{"detail":"Engine image tensorfold-qwen38:… is not built"}`.
- `DELETE /api/recipes/{slug}/install`: остановить процесс и снести папку клиента;
  `{"status":"removed"}`.
- `launch`: если у рецепта есть `engine_build` — запускать `<builds>/<slug>/bin/tensorfold-native`
  и подставить env из блока (относительные пути → абсолютные); если папки нет — 409
  `{"detail":"Engine client is not built"}`. Без `engine_build` — поведение как сейчас (стоковый
  релиз из `current`).
- `GET /api/recipes/{slug}`: у нативного рецепта появляется
  `"engine_build": {"ready": true, "image": "…", "path": "…"}` (только если блок объявлен).
- `GET /api/engines`: у каждого движка добавить `builds: [{"recipe":"…","path":"…","image":"…"}]`.

## Файлы

- `internal/engines/build.go` — `EnsureBuild`, `RemoveBuild`, `ListBuilds`, чтение/запись
  `build.json`. Тесты: поддельный docker (`LookPath`/`Run`), временный `Home`.
- `internal/native/native.go` — `Spec.Env` уже есть; нужен `Spec.WorkDir` не нужен, но env с
  относительными путями разворачивается до вызова. Ничего не ломать в стоковом пути.
- `internal/api/containers/register.go` — ветки install/remove/launch для сборки, поле
  `engine_build` в детали.
- `internal/api/engines/register.go` — `builds` в списке.
- `internal/registry/*` — разбор `native.engine_build`.

## Запреты

- Не трогать `frontend/` (это отдельная правка).
- Не менять поведение контейнерных рецептов.
- Не собирать docker-образ внутри хаба из патчей: сборка — это `command` из репозитория модели
  (`scripts/prepare.sh`), хаб его только запускает. Патчи остаются знанием репозитория модели.
- Не добавлять зависимостей.

## Готово

`go build ./... && go vet ./... && go test ./...` зелёные; тесты на: разбор блока, разворот
относительных путей env, `docker cp` артефактов (поддельный docker), 409 при отсутствии образа,
409 при launch без собранного клиента, удаление папки.
