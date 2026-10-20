# Этап 2 — что осталось и как это делать

Состояние на `v0.1.0`. Этап 2 из `docs/PLAN.md` = перепись `sah` на Go, интеграция с
llama-swap и идеи из `docs/IDEAS.md`. llama-swap сделан, остальное ниже — с честной
отметкой «что уже есть», «чем не изобретать велосипед» и «как проверять».

Правило этого файла: **у каждой идеи сначала ищется готовый инструмент**, и только
потом пишется свой код. Всё, что можно вызвать как бинарь и разобрать `--json`, своим
не пишем.

| # | Идея | Что есть | Цена | Приоритет |
|---|------|----------|------|-----------|
| 0 | Менеджер бинарей (`internal/tools`) | три разных загрузчика | 2–3 дня | фундамент |
| 1 | Страница бенчмарков | точечный замер по рецепту | 2–3 дня | высокий |
| 2 | Spark Doctor | — | 2 дня | высокий |
| 3 | Провенанс рецептов и синк-отчёт | источники, копии, compose | 3 дня | высокий |
| 4 | Свой прокси вместо контейнера Caddy | генератор Caddyfile | 3–4 дня | высокий |
| 5 | Прогноз памяти в spark-swap | шкалы, замер | 1–2 дня | средний |
| 6 | ~~Docker ↔ Podman~~ | только docker | — | перенесено в этап 3 |
| 7 | Общий кеш медиамоделей | — | 2 дня | средний |
| 8 | Реестр сопроводительных сервисов | — | 4–5 дней | низкий |
| 9 | `sah` на Go | Python-скрипт на 3.4k строк | 3–4 дня | низкий |

---

## 0. Менеджер бинарей (`internal/tools`) — общий фундамент

### Как движки устроены сейчас (это менять не надо)

`internal/engines` — централизованная установка **одного** релиза движка, а не
экземпляр на рецепт. На диске:

```
~/.config/gospark/engines/tensorfold/
  1.1.1/            распакованный релиз ashhart/TensorFold (bin, lib, LICENSES)      86 МБ
  current -> 1.1.1  символическая ссылка, единственная версия (старые удаляются)
  builds/<slug>/    пропатченный клиент конкретного рецепта из его образа             ~106 МБ
  runtime/          17 ГБ CUDA/torch/python, вынутые из builder-образа ОДИН раз
  options.json      флаги serve на весь движок (--cortheon, --dashboard)
```

- Релиз качается с GitHub (`tensorfold-<ver>-linux-aarch64.tar.gz` + `.sha256`),
  распаковывается в `<version>/`, ссылка `current` переключается одним `rename`.
  Версия держится ровно одна: `removeOtherVersions` удаляет прежнюю, чтобы апгрейд не
  копил гигабайты.
- Что рецепт может менять — только `native.engine_build {image, artifacts, command}`:
  образ **уже содержит** патченный движок (свой бранч/патчи — это знание репозитория
  модели, хаб патчи не накладывает), из него `docker cp` вынимается клиент в
  `builds/<slug>/`. Рецепт без `engine_build` идёт на релизе из `current`.
  `native.runtime_image` даёт только CUDA-рантайм, клиент остаётся релизным.
- `native` запускает один бинарь через `systemd --user` (транзиентный юнит
  `gospark-native-<slug>`), аргументы — из рецепта плюс флаги движка.
- Итог по трём локальным рецептам: `minicpm-v47-native` — build + runtime;
  `qwen38-flash-next-native` — build + runtime; `qwen38-27b-uncensored-native` — только
  runtime, клиент релизный.

**Вывод по вопросу «централизованно или экземпляр на рецепт»:** централизованно и уже
так. Дублировать релиз на каждый рецепт бессмысленно (86 МБ × N и 17 ГБ рантайма × N),
а различаются рецепты не версией движка, а образом, из которого собран их клиент.
Экземпляр на рецепт существует ровно там, где он нужен — `builds/<slug>/`.

**Три реальных пробела (дёшево закрыть попутно):**

1. **Версия не пинится.** Рецепт не может сказать `engine_version: 1.1.1`, все идут на
   `current` — обновление движка двигает все рецепты сразу, и старая версия удаляется.
   Лечение: необязательное поле `native.engine_version`, `PathFor(engine, version)` с
   фолбэком на `current`, хранить 2 версии вместо одной.
2. **Сборка не помнит, из чего собрана.** `build.json` знает `recipe/engine/image/built_at`,
   но не версию движка и не дайджест образа. После апгрейда непонятно, клиент устарел
   или нет. Лечение: писать `engine_version` + `image_id` (он уже есть локально —
   `docker image inspect`), в UI бейдж «собран на 1.1.1, движок 1.1.2».
3. **Нет уборки.** `builds/` живёт вечно, даже если рецепт удалён.

### Собственно менеджер (то, ради чего раздел)

Скачивание «релиза с GitHub» написано уже дважды и вот-вот понадобится в третий раз:

| где | что качает | строк |
| --- | --- | --- |
| `internal/engines/engines.go` | TensorFold tarball + `.sha256` | ~250 (`downloadFile`, `fetchText`, `extractTarGz`, `latestVersion`, `assetURL`) |
| `internal/llamaswap/manager.go` | llama-swap tarball + checksums | ~150 (`download`, `verify`, `unpack`) |
| нужен | `vllm-bench` — одиночный бинарь `vllm-bench-aarch64-linux-musl`, не тарбол | — |
| нужен | `spark-doctor` — git clone + venv | — |

#### Дизайн

Один пакет, каталог и один установщик. Три вида установки:

```go
type Kind string // "tarball" | "binary" | "venv"

type Tool struct {
    ID      string // "tensorfold", "llama-swap", "vllm-bench", "spark-doctor"
    Name    string // как показывать
    Kind    Kind
    Repo    string // owner/name на GitHub (для tarball/binary)
    Asset   string // шаблон имени ассета: "tensorfold-%s-%s.tar.gz", "vllm-bench-%s-linux-musl"
    Binary  string // путь к исполняемому внутри распаковки: "bin/tensorfold-native"
    Checks  string // ".sha256" | "SHA256SUMS" | "" (тогда sha256 пишем свой в installed.yaml)
    GitURL  string // для venv: URL репозитория
}
```

`tools.Manager` (поля `Home`, `HTTP`, `GitHubAPI`, `DownloadBase`, `Run` — как у
`engines.Manager`, чтобы тесты подменяли httptest-сервером):

- `Install(ctx, id, version)` — temp-каталог внутри `Home/<id>`, скачивание, сверка
  sha256, распаковка/установка, `<version>/`, атомарная замена `current`, запись
  `installed.yaml` (`version, asset, sha256, installed_at`), удаление версий сверх
  лимита (по умолчанию 2 — пин рецепта должен во что-то попадать);
- `Latest(ctx, id)` — GitHub `releases/latest`, кэш 5 минут, провал кэшируется;
- `Installed(id) (version, ok)`, `Path(id) (string, error)`, `List() []Status`,
  `Delete(id) error`;
- `Update(ctx, id)` — как в `engines.Update`: `changed=false`, если уже последняя.

Раскладка: `~/.config/gospark/tools/<id>/<version>/` + `current` + `installed.yaml`.
`engines` переезжает на неё как есть (его `Home` = `tools/tensorfold`), поэтому
миграция — `os.Rename` существующего каталога плюс `installed.yaml` в первом запуске.

#### Что остаётся в `engines` после рефакторинга

Только доменное: `Options` (флаги serve), `Builds` (`EnsureBuild`, `BuildPath`,
`ListBuilds`), `EnsureRuntime`, `PathFor` с пином версии. Скачивание, `latestVersion`,
распаковка, сверка контрольной суммы — удаляются (~250 строк минус). `internal/llamaswap`
теряет `download/verify/unpack` (~150 строк), оставляет `config.yaml`, `measurements.json`,
управление юнитом.

#### API и UI

- `GET /api/tools` → `[{id,name,kind,installed,version,latest,path,size}]` (admin);
- `POST /api/tools/{id}/install`, `POST /api/tools/{id}/update`, `DELETE /api/tools/{id}` —
  один job за раз, лог в тот же WS, что у установки движка;
- UI: блок «Hub tools» на странице **Engines** (там уже живёт «скачать/обновить/удалить
  бинарь», вторая ментальная модель не нужна): строки `Table` — имя, версия, размер,
  кнопки Install/Update/Delete. Кнопки llama-swap со страницы LlamaSwap остаются на
  месте, просто ходят в тот же менеджер; эндпоинты `/api/llamaswap/*` не меняются.

#### Шаги

1. `internal/tools/tools.go` + `catalog.go`: три вида установки, тесты на httptest
   (tarball, одиночный бинарь, неверная сумма, кэш latest, замена `current`, лимит
   версий).
2. Перевести `internal/engines` на `tools`: удалить загрузчик, оставить обёртку
   `Install/Update/Delete/Path` (API не меняется), добавить миграцию каталога.
   Существующие тесты engines должны остаться зелёными без правок.
3. Перевести `internal/llamaswap` на `tools` (id `llama-swap`).
4. Добавить в каталог `vllm-bench` (п. 1) и `spark-doctor` (п. 2), `GET /api/tools` + UI.
5. Пробелы движков: пин версии, `engine_version`/`image_id` в `build.json`, GC
   `builds/` для исчезнувших рецептов.

#### Проверка

- `go test ./internal/tools/... ./internal/engines/... ./internal/llamaswap/...`;
- на чистом `HOME` установка TensorFold через новый менеджер, затем запуск нативного
  рецепта — стартует из `tools/tensorfold/current/bin/tensorfold-native`;
- `GET /api/tools` показывает установленные бинари с версиями и размерами; повторная
  установка той же версии → 409;
- после `systemctl --user restart gospark-hub` состояние (`installed.yaml`) не теряется.

---

## 1. Страница бенчмарков

**Есть.** `internal/bench` меряет один прогон против живого эндпоинта
(`Measure`: TTFT, prefill по наклону двух запросов, decode, память процесса) и пишет
`data/benchmarks.json`; `POST /api/recipes/{slug}/benchmark`, `GET /api/recipes/benchmarks`;
в карточке рецепта — кнопка и строка результата на Running.

**Нет.** Матрицы «конкурентность × длина контекста», истории, сравнения рецептов,
прогресса прогона.

**Не изобретать.** Бенчмарк-раннер уже есть и он один на все движки:
[`vllm-project/vllm-bench`](https://github.com/vllm-project/vllm-bench) — отдельный
статический бинарь на Rust, `--backend openai` работает с любым OpenAI-совместимым
сервером (llama-swap, нативный vLLM/SGLang, контейнер), отдаёт `--save-result` в JSON,
умеет `--max-concurrency`, `--random-input-len`, `--percentile-metrics`. В релизе
`v0.1.0` есть `vllm-bench-aarch64-linux-musl` — ровно наша платформа.
Альтернативы, если понадобится: `vllm bench sweep` (внутри vLLM, только для vLLM) и
`guidellm` (pip, sweep по RPS). Свой раннер не пишем.

**План.**
1. Установка бинаря — менеджером из п. 0 (id `vllm-bench`), своего загрузчика здесь нет.
2. `internal/bench/run.go` — запуск `vllm-bench` как job'а: argv собирается из
   параметров страницы, stdout построчно уходит в WS-канал, по завершении читается
   JSON-файл результата. Явно: `--backend openai --base-url http://127.0.0.1:<port>
   --model <id> --dataset-name random --num-prompts N --max-concurrency C
   --save-result`.
3. Хранилище: таблица `bench_runs` (id, recipe_slug, model, engine, params_json,
   result_json, started_at, finished_at, status) в существующей SQLite. Прогресс — не
   в базе, а в памяти job-менеджера; в `data/benchmarks.json` остаётся только
   «последний точечный замер» для Running.
4. API `internal/api/bench`: `POST /api/bench/runs` (тело: slug, длина входа/выхода,
   список конкурентностей), `GET /api/bench/runs`, `GET /api/bench/runs/{id}`,
   `DELETE`, `GET /api/bench/tools` (что установлено), `POST /api/bench/tools/install`.
   Прогресс — `GET /ws/bench/{id}` рядом с существующими WS.
5. UI `frontend/src/pages/Benchmarks.jsx` (+ пункт в рельсе): выбор запущенного
   рецепта, поля «вход / выход / число запросов / конкурентности (1,2,4,8)», кнопка
   «Run», живой лог в `ScrollArea`, матрица результатов `Table` (ячейка = decode tok/s,
   hover — TTFT/ITL), сравнение до трёх прогонов рядом, «экспорт JSON».
6. Если бинаря нет или он упал — фолбэк на существующий `bench.Measure` с честной
   пометкой в UI «точечный замер, без матрицы».

**Проверка.** Прогон на поднятом нативном рецепте: матрица 3×3 заполняется, JSON
совпадает с файлом от `vllm-bench`; после рестарта демона прогоны видны в истории;
без установленного бинаря страница предлагает установку, а не падает.

---

## 2. Spark Doctor

**Есть.** Ничего.

**Не изобретать.** [`joeynyc/spark-doctor`](https://github.com/joeynyc/spark-doctor)
(MIT, Python 3.11+, read-only) — уже содержит правила DGX Spark: потребление под
нагрузкой, термотроттлинг, давление UMA/swap, готовность NVIDIA container runtime, две
тяжёлые модели, KV-cache OOM, Xid, скорость NIC, валидатор рецепта. CLI отдаёт ровно то,
что нужно GUI: `spark-doctor scan --json scan.json --markdown report.md`,
`spark-doctor doctor --from scan.json` (переиграть правила на сохранённом скане),
`spark-doctor recipe check recipe.yaml`. Коллекторы на Go **не переписываем** — это
сотни строк разбора `nvidia-smi`/`/proc`/`dmesg`, которые апстрим уже поддерживает.

**План.**
1. Установка: `internal/tools` из п. 0 клонирует репозиторий в
   `~/.config/gospark/tools/spark-doctor` и ставит его в venv рядом
   (`python3 -m venv .venv && .venv/bin/pip install -e .` — так велит README апстрима;
   в PyPI пакета нет). Путь к питону — в `tools.yaml`.
2. `internal/doctor/doctor.go`: `Scan(ctx)` → запуск `scan --json <tmp>`, разбор
   находок в структуру `{rule, severity, evidence, next_step, docs}`; `FromScan(file)`
   для повторного прогона правил; кэш последнего скана в `data/doctor.json`. Коды
   возврата апстрима — часть контракта: `0` чисто, `1` warning, `2` critical,
   `3` диагноз неполный (упал коллектор или правило) — «неполный» показывать отдельной
   плашкой, а не как «всё хорошо».
3. API `internal/api/doctor`: `POST /api/doctor/scan` (admin, job с логом),
   `GET /api/doctor/findings` (последний результат + время), `GET /api/doctor/history`
   (последние N сканов из `data/doctor-history.jsonl`).
4. UI `frontend/src/pages/Doctor.jsx`: список находок, сгруппированный по severity
   (`Badge` destructive / outline / secondary), у каждой — evidence моно-шрифтом и
   «следующий шаг»; кнопка «Scan» с логом; история — дата, число находок, что
   изменилось. Автофиксов нет — только инструкции, это свойство апстрима, а не
   недоделка.
5. Тюнинг-подсказки (вторая половина идеи) — отдельным шагом и **после** бенчмарков:
   рядом с полями `kv_cache_bytes` / `gpu_memory_utilization` / TTL показывать находку
   доктора, если она относится к этой модели (KV OOM, две модели). Связывать по тегу
   правила, а не эвристикой.

**Проверка.** Скан на живой машине: находки совпадают с `spark-doctor scan` в
терминале (тот же JSON); `recipe check` на заведомо кривом рецепте (TP=2 на одном GPU)
даёт ожидаемое правило.

---

## 3. Провенанс рецептов и отчёт синка

**Есть.** `internal/registry`: источники (`sources.yaml` с `url/ref/commit/synced_at`),
вендоренные клоны, `local/` без синка, «Create copy», редактирование compose и сброс к
оригиналу. API `internal/api/registries`: add/syncAll/syncOne/remove/listLocal.

**Нет.** Связи «копия → откуда и от какой версии», признака «оригинал уехал вперёд»,
отчёта «что приехало» после синка, редактирования полей рецепта (только compose).

**План.**
1. Рядом с каждой локальной копией — `origin.json`:
   `{source_id, source_url, slug, commit, recipe_sha256, copied_at}`. Пишется при
   «Create copy» (`internal/api/registries` + `registry.Manager.Fork`).
2. После синка источника — сверка: для каждой локальной копии считается sha256
   исходного `recipe.yaml` из нового коммита; расхождение → флаг `upstream_changed`
   в `GET /api/recipes/{slug}` и в списке локальных. Три состояния в UI: «актуальна»,
   «оригинал изменился», «оригинала больше нет».
3. Синк-отчёт вместо «synced»: до и после считать набор слагов источника + хеши, из
   разницы собрать `{added, changed, removed}` и вернуть в ответе `syncOne`/`syncAll`
   (уже возвращают JSON — расширяем поле, не ломая контракт). UI: раскрывающийся блок
   «что приехало» в Settings → Sources и ссылками на рецепты.
4. Редактор полей: `PUT /api/recipes/{slug}` уже пишет локальную копию — расширить на
   редактируемые поля (`title`, `description`, `port`, `memory_gb`, `env`, `params`,
   `quantization`, `context`), с валидацией по схеме `registry.Recipe` и запретом
   править vendored-рецепт (только копию). UI: секция «Edit» в `RecipeDetail` теми же
   `Field`/`Input`, что в Settings.
5. Кнопка «Pull» — уже есть (syncOne); добавить «обновить копию из оригинала»
   (перезапись с подтверждением и показом диффа `diff`-ом из `git diff --no-index`).

**Проверка.** `git -C <клон> log -1` до синка; после синка у копии появляется
`upstream_changed`, в UI — бейдж; «обновить копию» возвращает `origin.json` к новому
коммиту; vendored-рецепт через API не редактируется (403).

---

## 4. Свой прокси вместо контейнера Caddy

**Есть.** `internal/proxy`: генерация `data/Caddyfile` (≈3k строк на 148 рецептов),
сеть, контейнер `caddy:2-alpine`, проб-токен, `forward_auth` в `/api/auth/me`,
`PROXY_ENABLED=false` → демон сам слушает публичный порт.

**Нет.** Прокси внутри демона — то есть без контейнера и без генератора конфига.

**Не изобретать.** `net/http/httputil.ReverseProxy` умеет Upgrade/WebSocket, стриминг и
переписывание пути; сессия уже в процессе, подзапрос `forward_auth` не нужен. Caddy как
библиотека (`caddyserver/caddy/v2`) — только если понадобится его `handle_path`
поведение байт в байт; это +30–60 МБ к бинарю, поэтому не берём.

**План.**
1. `internal/proxy/builtin.go`: таблица маршрутов из реестра (та же, что кормит
   `render.go`) → `map[prefix]backend`. Один `http.Handler`:
   - `/app/{slug}/` → `container:port` (это текущая схема Caddy; 24 web-рецепта,
     один `strip_prefix`), путь переписывается `Rewrite`;
   - нет сессии → 302 на логин (тот же код, что сейчас в Caddy), `/healthz` и
     проб-запросы пропускаются;
   - `X-Forwarded-*` ставятся, `ModifyResponse` правит `Location` и `Set-Cookie`
     (`Path=/` → `Path=/app/{slug}/`), иначе приложение уводит на свой корень.
2. Конфиг `PROXY_MODE=caddy|builtin`, по умолчанию пока `caddy`; `builtin` держит
   публичный порт сам, контейнер не создаётся, `Caddyfile` не пишется.
3. Проверочный набор: три приложения — `web` с абсолютными путями (ComfyUI-подобное),
   `api-only`, и то, у которого `strip_prefix`. Для каждого: страница открывается,
   ассеты 200, редирект внутри приложения остаётся под префиксом, без сессии — 302.
4. После зелёного — переключить дефолт на `builtin`, Caddy оставить как опцию на один
   релиз (у кого-то Caddy уже поднят и настроен снаружи).

**Проверка.** Тот же сценарий под `PROXY_MODE=caddy` и `builtin`: статусы и заголовки
совпадают (сравнение curl'ом), контейнера `gospark-hub-proxy` в `docker ps` нет,
`go test ./internal/proxy/...` покрывает таблицу маршрутов и переписывание `Location`.

---

## 5. Прогноз памяти в spark-swap (остаток идеи)

**Есть.** Шкала памяти группы (использовано + ожидаемо), `Measure` пишет фактическую
память процесса в `measurements.json`, UI показывает «ожидаемо / факт» рядом.

**Нет.** Прогноза для моделей без `memory_gb` (по `params_b`, `quantization`,
`kv_cache_bytes`, контексту) и сохранения пары «прогноз / факт» для калибровки.

**План.**
1. `internal/llamaswap/estimate.go` — формула: вес = `params_b × bytes_per_param(quant)`
   + `kv_cache_bytes` (если задан) + оверхед 8–12%; при наличии `active_params_b`
   (MoE) считать по активным. `bytes_per_param`: fp16 2, q8 1, q4/fp4/nvfp4 0.5–0.6,
   int4 0.55.
2. Калибровка на факте: после `Measure` сохранять `{estimate_gb, actual_gb}` в
   `measurements.json`; коэффициент `actual/estimate` по последним замерам этого
   квантования подставлять в следующие прогнозы (и показывать в UI мелким шрифтом
   «прогноз × 1.07 по 3 замерам»).
3. UI: в строке группы и у модели — «≈X GB (прогноз)» вместо «—», тултип с разбивкой
   вес / KV / оверхед.

**Проверка.** Для трёх моделей сравнить прогноз с фактическим замером: расхождение в
пределах ±15% (иначе правим коэффициент, а не формулу на глаз).

---

## 6. Docker ↔ Podman — перенесено в этап 3

Пока не нужно: на машине только docker. Идея остаётся в `docs/IDEAS.md`, срок — этап 3
(`docs/PLAN.md`). Когда понадобится, план из прошлой редакции этого файла: конфиг
`CONTAINER_CLI`, проброс в `dockerx.Client.Bin`, `Probe` перед переключением, проверка
`podman compose` и rootless-маунтов.

## 7. Общий кеш медиамоделей

**Есть.** Ничего в хабе (на этой машине каталог и симлинк сделаны руками).

**План.**
1. Конфиг `MEDIA_MODELS_DIR` (по умолчанию пусто = выключено). Каталог на хосте,
   монтируется в контейнеры медиа-рецептов bind-mount'ом в тот путь, который рецепт
   объявляет (`models_mount` в рецепте, по умолчанию `/models`).
2. `internal/registry`: у рецепта — `kind: media` (аудио/видео/3D), чтобы каталог мог
   показать отдельную вкладку «медиа» и не смешивать с LLM.
3. `internal/api/models` (уже есть): `GET /api/media-models` — обход каталога, размеры,
   кто использует (по конфигам рецептов и маунтам запущенных контейнеров), ручное
   удаление. Refcount не делаем — синхронизация дороже пользы, это записано в IDEAS.
4. UI: вкладка «Media» в Store с таблицей файлов и колонкой «используется в».

**Проверка.** ComfyUI и второй медиа-рецепт видят один и тот же файл, в `docker inspect`
— один маунт, файл на диске один; удаление через UI убирает файл с диска.

---

## 8. Реестр сопроводительных сервисов

**Есть.** Ничего.

**План (сначала манифесты, потом код).**
1. `registry/services/<id>.yaml` — декларация: имя, описание, способ установки
   (`systemd-binary` | `docker` | `pip-venv`), артефакт и версия, порт(ы), тома, нужные
   ключи, health-URL, шаги удаления. Начать с трёх: OpenViking, SearXNG, Open WebUI.
2. `internal/services`: чтение манифестов, статус (`systemctl --user is-active`,
   `docker ps`, `curl health`), установка/удаление через те же примитивы, что уже есть:
   `internal/tools` (скачивание бинарей, п. 0), `internal/dockerx` (compose), шаблон
   unit'а из `internal/engines`.
3. API `internal/api/services`: `GET /api/services` (список + статус), `POST
   /api/services/{id}/install|remove|start|stop`, лог job'а в существующем WS.
4. UI: страница `Services.jsx` — карточки с `Badge` статуса (нет / установлен / запущен /
   упал), порт и кнопка «открыть», лог установки в `Sheet`.

**Проверка.** Установка SearXNG из UI с нуля (нет образа → есть контейнер → health 200),
затем удаление; OpenViking определяется как «запущен» без установки.

---

## 9. `sah` на Go

**Есть.** `sah/sah` — Python-скрипт 3.4k строк, отдаётся статикой (`/sah`, `/sah/sah`,
`/sah/install.sh`, `/sah/version`); конфиг — `~/.config/sah/hub` (список URL) и
`~/.config/sah/key`.

**План.**
1. `cmd/sah`: stdlib `flag` + подкоманды (внешний CLI-фреймворк не нужен), встроенная
   версия, `install.sh` ставит бинарь вместо скрипта.
2. Совместимость файлов: читать/писать те же `~/.config/sah/hub` и `key`, чтобы уже
   настроенные машины не перенастраивались. Discovery хаба — тот же список кандидатов
   (mDNS, tailscale, локальный порт), что в Python-версии.
3. Подкоманды по одной, каждая — отдельный коммит с паритетным тестом против
   Python-версии: `info`, `models`/`ps`, `env`, `mcp`, `integrations`/`clients`,
   `set-key`, `set-hub`, `refresh-hub`, затем `run`/passthrough (самая большая часть —
   подстановка env и запуск клиента).
4. Раздача: `/sah/sah` начинает отдавать бинарь под arm64, `/sah/version` — его sha256;
   Python-скрипт остаётся в репозитории, пока паритет не закрыт.

**Проверка.** `cmd/parity`-подобный прогон: для каждой подкоманды сравнить stdout/код
возврата Python и Go на одной машине (кроме времени и путей), `sah run -- <cmd>` реально
запускает клиент с нужным `OPENAI_BASE_URL`.

---

## Порядок и зависимости

1. **Тул-менеджер** (`internal/tools`, п. 0) — общий для п. 1, 2, 8; делать первым.
2. **Бенчмарки** (1) и **Doctor** (2) — независимы друг от друга, обе дают страницы,
   обе проверяются на живой машине.
3. **Провенанс и редактор рецептов** (3) — трогает `registry` и `RecipeDetail`,
   делать до подсказок тюнинга, потому что они правят те же поля.
4. **Свой прокси** (4) — отдельная ветка, риск в переписывании `Location`/cookie;
   паритетный прогон обязателен.
5. **Медиа-кеш** (7) — трогает `dockerx`, делать после пункта 4, если прокси уже свой.
6. **Сервисы** (8) — после тул-менеджера и `dockerx`, иначе будет дублирование.
7. **`sah` на Go** (9) — независим, но самый скучный; брать, когда остальное закрыто.

## Чего сознательно не делаем

- Своих коллекторов для доктора и своего бенчмарк-раннера — есть готовые.
- Refcount потребителей медиамоделей — дороже пользы (записано в IDEAS).
- Caddy как библиотеку — размер бинаря не оправдан для 24 маршрутов.
- Автофиксов в докторе — только инструкции, как в апстриме.
