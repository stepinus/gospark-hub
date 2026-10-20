# Нативная карточка: формат, стадии install / run, рантайм

Состояние на `v0.1.0`. Разбор по коду `internal/engines`, `internal/native`,
`internal/api/containers` и по тому, что лежит на диске этой машины.

## 1. Что движку действительно нужно

Из `RUNTIME.md` самого релиза (`~/.config/gospark/engines/tensorfold/1.1.1/RUNTIME.md`):

- бинарь `bin/tensorfold-native` (83 МБ) самодостаточен: `--version`, `--help` и текстовая
  выдача не требуют ни модели, ни Python-пакетов, ни драйвера;
- Linux-сборка рассчитана на glibc ≥ 2.28, `libcuda.so.1` даёт драйвер;
- `lib/libtfgrammar.so` (2.8 МБ) лежит в самом релизе;
- сторонние `.so` нужны только под фичи: **cuBLASLt** — для `--vision`, **cuBLAS** — для
  Drama3, **NCCL** — для tensor-parallel рангов, плюс опциональные декодеры
  (turbojpeg, webp, tfvideo), `libibverbs` — для RDMA;
- **«None of those paths requires an installed Python package»** — Python нужен только
  для генерации capture, а на рантайме — исключительно vision-хелперу:
  `python/tensorfold/vision/native_helper.py` импортирует `torch` (CUDA),
  `transformers`, `numpy`, `PIL`.

Факты с диска:

| что | размер |
| --- | --- |
| релиз движка `1.1.1/` | 86 МБ |
| клиенты рецептов `builds/` (2 шт) | 106 МБ |
| `runtime/pylibs` (весь dist-packages образа) | 8.2 ГБ |
| `runtime/vlib/sys` (целиком `/usr/lib/aarch64-linux-gnu` образа) | 5.4 ГБ |
| `runtime/vlib/cuda` (CUDA 13.3 из образа) | 2.4 ГБ |
| `runtime/vlib/hpcx` | 466 МБ |
| **итого runtime** | **≈17 ГБ** |

Из этих 17 ГБ vision-хелперу нужны `torch` (1.4 ГБ), `nvidia-*` (675 МБ),
`transformers` + `numpy` + `pillow` — **2.2 ГБ на пятерых**. Остальное — целый rootfs
образа, скопированный «на всякий случай».

Плюс на этой машине уже стоит `cuda-toolkit-13-2`: `libcublasLt.so.13` и
`libcublas.so.13` есть в `ldconfig`, `libcuda.so.1` — от драйвера, `libibverbs` — из
`rdma-core`. То есть для **текстовых** рецептов не нужно ни одного байта сверх релиза.

**Вывод.** Карточке нужно уметь сказать «рантайм не нужен» и «рантайм нужен вот такой»,
а не «тащи весь образ, вдруг пригодится».

## 2. Формат карточки v2: две стадии вместо одного блока

Смысл: `install` описывает, **что положить на диск** (медленно, один раз, с логом и
прогрессом), `run` — **как запустить** уже установленное (быстро, повторяемо).
Сейчас эти вещи перемешаны в `native:`, и `install` для нативного рецепта — это
`ensureRuntime` + `ensureEngineBuild`, спрятанные внутри «установки рецепта».

```yaml
kind: native
engine: tensorfold              # имя движка (как сейчас)
engine_version: "1.1.1"         # НОВОЕ, необязательное: пин версии релиза

install:                        # стадия install
  client:                       # опционально: пропатченный клиент рецепта
    from_image: tensorfold-qwen38:zig-db28187   # образ — источник артефактов
    artifacts:                  # путь в образе -> куда (имя каталога в клиенте)
      bin:    /opt/tensorfold/native/bin
      share:  /opt/tensorfold/share
      fatbin: /opt/tensorfold/fatbin
      python: /opt/tensorfold/python
    build: build-engine.sh      # если образа нет — команда из каталога рецепта
  runtime:                      # опционально: python нужен только под vision
    kind: venv                  # venv | image-rootfs | none  (по умолчанию none)
    python: "3.12"
    lock: requirements.lock     # торч/трансформеры/нумпи/пиллоу с точными версиями
    source: index               # index | wheelhouse | image
  libs: []                      # опционально: nccl, turbojpeg, webp — если нет в системе

run:
  model: "hf:nvidia/Qwen3.8-Flash-Next-FP4-Local@b9ce18a6887e18c089fdea8509a33e63429a3300"
  args: [ ... ]                 # как сейчас
  env:
    TENSORFOLD_CUDA_KERNELS: "{client}/share/tensorfold/cuda/sm121"
    TENSORFOLD_VISION_PYTHONPATH: "{client}/python"
  port: 8888
  health: /v1/models            # НОВОЕ: чем проверять готовность (сейчас это делает UI вручную)
```

Подстановки в `run.args` / `run.env` (сейчас их делает `engines.ExpandEnv` только для
`engine_build.env`): `{engine}` — каталог релиза движка, `{client}` — каталог клиента,
`{runtime}` — корень выбранного рантайма, `{libs}` — каталог сторонних библиотек,
`{model}` — разрешённый путь к весам.

Старое `native:` **читается как есть**: загрузчик конвертирует
`vision: true` → `install.runtime.kind: image-rootfs`, `engine_build` → `install.client`,
`runtime_image` → образ для рантайма. Ни один из 148 вендоренных рецептов править не надо.

## 3. Как это описывает наши три модели

| рецепт | install.client | install.runtime | что окажется на диске |
| --- | --- | --- | --- |
| `minicpm-v47-native` (текст) | из `tensorfold-minicpm:zig-0b1fd2b` | `none` | клиент 33 МБ, **рантайм не нужен** |
| `qwen38-27b-uncensored-native` (vision, сток-клиент) | — | `venv`, py3.12 + torch/transformers/numpy/pillow | ~2.4 ГБ |
| `qwen38-flash-next-native` (vision + патчи) | из `tensorfold-qwen38:zig-db28187` + `python/` | `venv` (тот же flavor) | клиент 74 МБ + общий рантайм 2.4 ГБ |

Рантайм — **flavor на движок**, а не на рецепт: ключ = хеш (python + lock), каталог
`engines/tensorfold/runtimes/<flavor>/`, рецепты с одинаковым lock делят его. Два qwen
выше берут один и тот же flavor, второй платит ноль.

Состояние стадий — `data/installs/<slug>.json`:

```json
{"recipe":"qwen38-flash-next-native","engine":"tensorfold","engine_version":"1.1.1",
 "stages":{
   "client":{"ok":true,"path":"…/builds/qwen38-flash-next-native","image":"tensorfold-qwen38:zig-db28187","image_id":"sha256:…","at":"…"},
   "runtime":{"ok":true,"flavor":"py312-torch2.13-nv","path":"…/runtimes/py312-torch2.13-nv","bytes":2400000000},
   "libs":{"ok":true,"items":[]}}}
```

`launch` больше не «проверяет, есть ли файлы» — он требует, чтобы все объявленные стадии
были `ok`, иначе 409 с именем недостающей стадии (UI показывает кнопку Install на этой
стадии, а не «Engine runtime is not installed»).

## 4. Чем инкапсулировать рантайм

| вариант | размер | плюсы | минусы |
| --- | --- | --- | --- |
| **rootfs из образа** (сейчас) | 17 ГБ | офлайн, ровно то, что проверяли в образе | размер, привязка к builder-образу, непрозрачно, дублирует системный CUDA |
| **uv venv** (рекомендую) | ~2.4 ГБ | lock-файл с точными версиями, обновляется независимо от образа, `uv` — один статический бинарь (уже стоит, `0.11.27`), `uv python` даёт CPython 3.12 без rootfs | нужен источник колёс (индекс или wheelhouse), первый install тянет сеть |
| `pip --target` без venv | 2.2 ГБ | то же, без venv | ручное управление `PYTHONPATH`, нет lock-механизма |
| conda / micromamba | ~4–5 ГБ | готовые CUDA-пакеты | ещё один менеджер сред, тяжелее uv |
| Nix | ~2 ГБ | герметично и воспроизводимо | кривая обучения, долгий install, на этой машине не стоит |
| системный python + apt | 0 | качать нечего | версии не те: GB10 нужен CUDA 13 и torch cp312 с sm_121 |

Итог: **`venv` через uv** — дефолт для vision-рецептов; `image-rootfs` оставляем как
явный fallback (`kind: image-rootfs`) для чужих рецептов, где в образе нестандартная
сборка; `none` — для текстовых. Своей реализации «окружений» не пишем: venv, `--python`,
lock и кеш колёс делает uv, мы только вызываем.

Версии для lock (сняты с текущего рабочего рантайма, он проверен на GB10):

```
torch==2.13.0a0+9186a08b2c.nv26.7.59513937   # NVIDIA-сборка, cp312, linux_aarch64
transformers==5.17.0
numpy==2.1.0
pillow==12.3.0
```

Оговорка: это NVIDIA-овская сборка torch (`+9186a08b2c.nv26.07`), её может не быть в
PyPI. Поэтому у `runtime` есть `source`: `index` (pypi.nvidia.com / download.pytorch.org),
`wheelhouse` (локальный каталог колёс — можно один раз сохранить из образа), `image`
(вынуть `*.whl` из `/opt/transfer` образа и поставить из них). Если ни один источник не
даёт эту версию — берём публичный `torch` cu130 для aarch64 и проверяем vision-запрос;
это проверка, а не догадка.

## 5. Что меняется в демоне

1. `internal/registry` — схема v2 (`NativeInstall`, `NativeRun`) + конверсия старого блока
   в v2 (чистая функция, покрыть тестами на всех вендоренных рецептах).
2. `internal/engines/runtimes.go` — вместо одного `runtime/` на движок:
   `RuntimeFlavor(engine, py, lock) (dir, env, error)`, `EnsureVenv(ctx, flavor, lock,
   source)` (вызовы `uv venv --python 3.12 <dir>` и `uv pip install --python
   <dir>/bin/python -r <lock>`, лог в job), `EnsureImageRootfs` — текущая логика как есть.
   `RuntimeEnv` собирает `PYTHONPATH`/`LD_LIBRARY_PATH` от выбранного flavor; `vlib` для
   venv-flavor не нужен — CUDA берётся из `torch/lib` и системы.
3. Стадии: `internal/api/containers` (сейчас `install` для нативного рецепта делает
   `ensureRuntime` + `ensureEngineBuild`) → отдельные `GET /api/recipes/{slug}/install`
   (состояние стадий) и `POST …/install` с телом `{"stage":"runtime"|"client"|"all"}`;
   `launch` проверяет `installs/<slug>.json`.
4. UI: в `RecipeDetail` — карточка «Install» со стадиями (client / runtime / libs), у
   каждой версия, размер, кнопка и лог; на `Engines` — список flavor'ов рантайма с
   размером и удалением (сегодня там нечего удалять — 17 ГБ лежат молча).
5. Уборка: flavor без потребителей помечается в UI («не используется ни одним
   рецептом»), удаляется кнопкой. Автоматически не удаляем — как и с весами.

## 6. Миграция без простоя

1. Схема v2 + конверсия: старые рецепты продолжают работать на `image-rootfs`.
2. Собрать `py312-torch2.13-nv` рядом (2.4 ГБ), переключить два qwen-рецепта на
   `runtime.kind: venv`, проверить vision-запрос (картинка → ответ) и текст.
3. После зелёного — удалить `engines/tensorfold/runtime/` (17 ГБ). На этой машине это
   освобождает ~14.5 ГБ.
4. `minicpm-v47-native` перевести на `install.runtime.kind: none` — рантайм ему не нужен
   вообще (текстовый чекпойнт).

## 7. Шаги и проверки

| шаг | файлы | проверка |
| --- | --- | --- |
| 1. Схема v2 + конверсия v1 | `internal/registry/native_spec.go`, тесты | все вендоренные рецепты парсятся, старый блок даёт тот же `Spec`, что и сейчас |
| 2. flavor-рантаймы | `internal/engines/runtimes.go` | `go test ./internal/engines/...`; venv создаётся на пустом HOME (uv, lock), повторный вызов ничего не делает |
| 3. install-стадии и API | `internal/api/containers`, `internal/store` | `GET/POST …/install`; `launch` без стадии → 409 с именем стадии |
| 4. UI стадий | `RecipeDetail.jsx`, `Engines.jsx` | Install с логом, размеры, удаление flavor'а |
| 5. миграция трёх рецептов | `registry/recipes/*` | vision: PNG через `/v1/chat/completions` даёт ответ; RAM процесса ≈ 2.4 ГБ рантайма + веса |
| 6. уборка 17 ГБ | — | `du -sh engines/tensorfold/*` после удаления |

Цена: 3–4 дня на шаги 1–4, ещё день на миграцию и замеры. Первый шаг (схема + конверсия)
не меняет поведение вообще — его можно вливать отдельно.
