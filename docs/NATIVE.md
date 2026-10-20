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
    source:                     # 1) собрать на хосте из исходников
      repo: https://github.com/jkuepker/TensorFold
      ref: 0b1fd2bcebd30d8bfc3e22e8b6e744676b5e4679
      toolchain: [zig@0.17.0]   # ставится менеджером бинарей, не системой
      build: "zig build -Dnvcc=/usr/local/cuda/bin/nvcc -Doptimize=fast --prefix {out} -j{nproc} fatbins install native"
    from_image: tensorfold-qwen38:zig-db28187   # 2) legacy-fallback: вынуть из образа
    keep_image: false          # образ — сборочная среда; удаляем после копирования
    artifacts:                  # что копировать, если путь через образ
      bin:    /opt/tensorfold/native/bin
      share:  /opt/tensorfold/share
      fatbin: /opt/tensorfold/fatbin
      python: /opt/tensorfold/python
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

### Пространство: образ — сборочная среда, а не артефакт

Ради этого всё и затевается. Классический compose-рецепт тянет модель (30–50 ГБ) плюс
свой runtime-образ (ещё 20–40 ГБ), причём runtime у всех почти одинаковый: драйверы,
CUDA, python, а кода движка там сотни мегабайт. На этой машине это видно прямо:
`llama.cpp:server-cuda13` — 7 ГБ, `spark-comfyui` — 13 ГБ, и так на каждый рецепт.

Инвариант нативной карточки:

| что | где живёт | размер |
| --- | --- | --- |
| инструкция сборки (Dockerfile, `build-engine.sh`, пиннутый ref, sha zig) | каталог рецепта в репозитории | килобайты |
| собранный клиент | `engines/<engine>/builds/<slug>/` | 33–74 МБ |
| рантайм (только если нужен vision) | `engines/<engine>/runtimes/<flavor>/`, общий | ~2.4 ГБ |
| релиз движка | `tools/<engine>/<version>/` | 86 МБ |
| **образ, из которого всё это вынули** | **удаляется сразу после `docker cp`** | 0 |

Сейчас `EnsureBuild`/`EnsureRuntime` убирают только одноразовый контейнер
(`docker rm -f`), а образ остаётся — и именно он занимает десятки гигабайт. Поэтому в
`install.client` появляется `keep_image: false` (дефолт), и после копирования артефактов
делается `docker image rm <image>`; в `build.json` остаются `image_id` и дайджест, чтобы
было видно, из чего собран клиент, даже когда образа уже нет. Пересобрать — по инструкции
из рецепта (или `zig build` на хосте, если `source` задан).

Флажок `keep_image: true` — только для отладки сборки, и тогда UI показывает размер
образа и кнопку «удалить».

### Почему вообще был docker и можно ли без него

`Dockerfile` в каталоге рецепта — это не требование рантайма, а упаковка **сборки**:

```
FROM nvcr.io/nvidia/pytorch:26.07-py3
RUN … распаковать zig 0.17.0 …
RUN git fetch jkuepker/TensorFold@0b1fd2b
RUN zig build -Dnvcc=/usr/local/cuda/bin/nvcc -Doptimize=fast --prefix /opt/tensorfold fatbins install native
```

Всё, что нужно этой сборке, — **zig 0.17, nvcc и git**. Образ `pytorch:26.07` нужен был
только ради второго слоя — того самого python-рантайма, который мы теперь заменяем
venv'ом. На этой машине уже есть `nvcc` 13.2 (`/usr/local/cuda/bin/nvcc`) и git;
zig 0.17 — один статический архив с ziglang.org (`zig-aarch64-linux-0.17.0.tar.xz`,
sha256 зафиксирован в `build-engine.sh` рецепта, ссылка отвечает 200).

Поэтому порядок источников в `install.client` такой:

1. **собрать на хосте** (`source`): zig ставит менеджер бинарей (п. 0 в `STAGE2.md`),
   nvcc берётся из системного CUDA, репозиторий — по пиннутому `ref`. Docker не участвует.
2. **вынуть из образа** (`from_image`) — legacy и аварийный путь, пока рецепт не переписан.
3. опубликованный релиз движка (`engine_version`) — для рецептов без патчей, как сейчас.

Итог: docker перестаёт быть обязательным вообще — ни для движка, ни для клиента, ни для
рантайма. Образы остаются только как источник артефактов для старых рецептов; собирать их
заново не нужно (на этой машине их уже нет — `tensorfold-minicpm:zig-0b1fd2b` не
существует, а клиенты на диске живут).

Старое `native:` **читается как есть**: загрузчик конвертирует
`vision: true` → `install.runtime.kind: image-rootfs`, `engine_build` → `install.client`,
`runtime_image` → образ для рантайма. Ни один из 148 вендоренных рецептов править не надо.

### Что записано, а что нет (проверено)

| рецепт | источник клиента записан? |
| --- | --- |
| `minicpm-v47-native` | да: `build-engine.sh` + `Dockerfile` — `jkuepker/TensorFold@0b1fd2bcebd3…`, zig 0.17.0 с sha256 |
| `qwen38-flash-next-native` | нет: только тег образа `tensorfold-qwen38:zig-db28187` |
| `qwen38-27b-uncensored-native` | клиент стоковый, сборки нет — вопрос не стоит |

Образа на машине больше нет (`docker images` пуст по `tensorfold-*`), а репозиторий и
ref форка qwen38 нигде не записаны ни в рецепте, ни в `~/.cache/tensorfold-qwen38`, ни в
репозитории модели. Клиенты в `builds/` рабочие, но **пересобрать их сейчас не из чего** —
это цена того, что источник жил в теге образа. Вывод для формата: `install.client`
обязан объявлять `source` (repo+ref) или `from_image`; для новых рецептов — только
`source`, иначе через полгода клиент не воспроизвести. Пока ref не восстановлен, у
рецептов qwen38 остаётся `from_image: …` плюс пометка «собран ранее, источник утерян».

## 3. Как это описывает наши три модели

| рецепт | install.client | install.runtime | что окажется на диске |
| --- | --- | --- | --- |
| `minicpm-v47-native` (текст) | сборка `jkuepker/TensorFold@0b1fd2b` (zig, без docker) | `none` | клиент 33 МБ, **рантайм не нужен** |
| `qwen38-27b-uncensored-native` (vision, сток-клиент) | — | `venv`, py3.12 + torch/transformers/numpy/pillow | ~2.4 ГБ |
| `qwen38-flash-next-native` (vision + патчи) | сборка `jkuepker/TensorFold@db28187` (zig) | `venv` (тот же flavor) | клиент 74 МБ + общий рантайм 2.4 ГБ |

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
2. `internal/engines/clients.go` — сборка клиента на хосте: `git init/fetch --depth 1
   <repo> <ref>` во временный каталог, `zig build … --prefix <tmp-out>`, перенос в
   `builds/<slug>/`, `build.json` (repo, ref, zig version, nvcc version). Zig ставится
   менеджером бинарей в `tools/zig/current`; nvcc берётся из системы и проверяется
   (`nvcc --version` → ≥ 13). `from_image` остаётся как fallback, если `source` не задан.
3. `internal/engines/runtimes.go` — вместо одного `runtime/` на движок:
   `RuntimeFlavor(engine, py, lock) (dir, env, error)`, `EnsureVenv(ctx, flavor, lock,
   source)` (вызовы `uv venv --python 3.12 <dir>` и `uv pip install --python
   <dir>/bin/python -r <lock>`, лог в job), `EnsureImageRootfs` — текущая логика как есть.
   `RuntimeEnv` собирает `PYTHONPATH`/`LD_LIBRARY_PATH` от выбранного flavor; `vlib` для
   venv-flavor не нужен — CUDA берётся из `torch/lib` и системы.
4. Стадии: `internal/api/containers` (сейчас `install` для нативного рецепта делает
   `ensureRuntime` + `ensureEngineBuild`) → отдельные `GET /api/recipes/{slug}/install`
   (состояние стадий) и `POST …/install` с телом `{"stage":"runtime"|"client"|"all"}`;
   `launch` проверяет `installs/<slug>.json`.
5. UI: в `RecipeDetail` — карточка «Install» со стадиями (client / runtime / libs), у
   каждой версия, размер, кнопка и лог; на `Engines` — список flavor'ов рантайма с
   размером и удалением (сегодня там нечего удалять — 17 ГБ лежат молча).
6. Уборка: flavor без потребителей помечается в UI («не используется ни одним
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
| 1b. удаление образа после сборки | `internal/engines/build.go` (`keep_image`), `build.json.image_id` | после `install` в `docker images` нет образа рецепта, `builds/<slug>/` работает, `build.json` помнит `image_id` |
| 1a. сборка клиента на хосте | `internal/engines/clients.go`, `tools` (zig) | `zig build` из репозитория рецепта даёт рабочий `tensorfold-native --version`, `build.json` с ref и версиями zig/nvcc |
| 2. flavor-рантаймы | `internal/engines/runtimes.go` | `go test ./internal/engines/...`; venv создаётся на пустом HOME (uv, lock), повторный вызов ничего не делает |
| 3. install-стадии и API | `internal/api/containers`, `internal/store` | `GET/POST …/install`; `launch` без стадии → 409 с именем стадии |
| 4. UI стадий | `RecipeDetail.jsx`, `Engines.jsx` | Install с логом, размеры, удаление flavor'а |
| 5. миграция трёх рецептов | `registry/recipes/*` | vision: PNG через `/v1/chat/completions` даёт ответ; RAM процесса ≈ 2.4 ГБ рантайма + веса |
| 6. уборка 17 ГБ | — | `du -sh engines/tensorfold/*` после удаления |

Цена: 3–4 дня на шаги 1–4, ещё день на миграцию и замеры. Первый шаг (схема + конверсия)
не меняет поведение вообще — его можно вливать отдельно.
